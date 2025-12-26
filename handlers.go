package main

import (
	"context"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Handler struct {
	DB *pgxpool.Pool
}

// SyncInventory: Updates total physical stock (Overwrites)
func (h *Handler) SyncInventory(c *fiber.Ctx) error {
	merchantID := c.Locals("merchant_id").(string)
	
	var req SyncRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid body"})
	}

	ctx := context.Background()

	// Logic: available = new_total - existing_reserved
	query := `
		INSERT INTO inventory_items (sku, merchant_id, location_id, total_qty, available_qty)
		VALUES ($1, $2, 'default', $3, $3)
		ON CONFLICT (merchant_id, location_id, sku) 
		DO UPDATE SET 
			total_qty = $3,
			available_qty = $3 - inventory_items.reserved_qty,
			updated_at = NOW()
	`
	_, err := h.DB.Exec(ctx, query, req.SKU, merchantID, req.Quantity)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	return c.JSON(fiber.Map{"status": "synced", "sku": req.SKU})
}

// LockInventory: The Atomic Locking Logic
func (h *Handler) LockInventory(c *fiber.Ctx) error {
	merchantID := c.Locals("merchant_id").(string)

	var req LockRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid body"})
	}

	loc := req.LocationID
	if loc == "" { loc = "default" }

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tx, err := h.DB.Begin(ctx)
	if err != nil { return c.Status(500).JSON(fiber.Map{"error": "DB Tx Failed"}) }
	defer tx.Rollback(ctx)

	// 1. ATOMIC UPDATE
	// Decrement Available ONLY IF we have enough stock (and safety buffer)
	query := `
		UPDATE inventory_items 
		SET available_qty = available_qty - $1, 
		    reserved_qty = reserved_qty + $1 
		WHERE sku = $2 
		  AND merchant_id = $3 
		  AND location_id = $4
		  AND (available_qty - safety_stock) >= $1
	`
	tag, err := tx.Exec(ctx, query, req.Quantity, req.SKU, merchantID, loc)
	
	if err != nil || tag.RowsAffected() == 0 {
		return c.Status(409).JSON(fiber.Map{"error": "Oversold", "sku": req.SKU})
	}

	// 2. Create Lock Record
	lockID := uuid.New()
	ttl := GetDynamicTTL()
	expiresAt := time.Now().Add(time.Duration(ttl) * time.Second)

	_, err = tx.Exec(ctx,
		`INSERT INTO inventory_locks (lock_id, merchant_id, sku, location_id, qty, order_ref, expires_at) 
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		lockID, merchantID, req.SKU, loc, req.Quantity, req.OrderRef, expiresAt,
	)

	// 3. Queue Sync Job (For External Platforms)
	tx.Exec(ctx, `INSERT INTO sync_jobs (merchant_id, target_platform, sku) VALUES ($1, 'SHOPIFY', $2)`, merchantID, req.SKU)

	tx.Commit(ctx)

	return c.JSON(LockResponse{
		LockID: lockID.String(),
		Status: "LOCKED",
		ExpiresAt: expiresAt,
		LocationID: loc,
	})
}
