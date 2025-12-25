package main

import (
	"context"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Handler struct {
	DB *pgxpool.Pool
}

func (h *Handler) LockInventory(c *fiber.Ctx) error {
	if h.DB == nil {
		return fiber.NewError(500, "database not initialized")
	}

	var req LockRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(400, "invalid request body")
	}

	ctx := context.Background()

	tx, err := h.DB.Begin(ctx)
	if err != nil {
	    return fiber.NewError(500, err.Error())
	}
	defer tx.Rollback(ctx)
	
	lockID := uuid.New()
	
	// Use the correct column names from your table
	_, err = tx.Exec(ctx,
	    `INSERT INTO inventory_locks (lock_id, sku, qty, order_ref, expires_at) VALUES ($1, $2, $3, $4, $5)`,
	    lockID, req.SKU, req.Quantity, req.OrderRef, time.Now().Add(time.Duration(req.TTL)*time.Second),
	)
	if err != nil {
	    return fiber.NewError(500, "failed to insert lock: "+err.Error())
	}
	
	if err := tx.Commit(ctx); err != nil {
	    return fiber.NewError(500, err.Error())
	}
	
	return c.JSON(fiber.Map{
	    "lock_id": lockID,
	    "status":  "locked",
	})

}
