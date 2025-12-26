package main

import (
	"context"
	"log"
	"os"
	"sync/atomic"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// --- 1. CONFIGURATION & STATE ---

var (
	currentRPS      int64
	highTrafficMode int32 // 0 = Normal, 1 = Panic Mode
)

// --- 2. DATABASE CONNECT & SCHEMA ---

func ConnectDB() *pgxpool.Pool {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL is not set")
	}

	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		log.Fatal("Failed to parse config:", err)
	}
	config.MaxConns = 50 // High concurrency
	config.MinConns = 5

	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		log.Fatal("Failed to connect to DB:", err)
	}
	return pool
}

func InitializeSchema(db *pgxpool.Pool) {
	log.Println("Initializing Headless Schema...")
	sql := `
	CREATE EXTENSION IF NOT EXISTS "pgcrypto";

	-- MERCHANTS (Tenants)
	CREATE TABLE IF NOT EXISTS merchants (
		id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
		rapidapi_user TEXT UNIQUE NOT NULL,
		name TEXT NOT NULL,
		created_at TIMESTAMP DEFAULT NOW()
	);

	-- LOCATIONS
	CREATE TABLE IF NOT EXISTS locations (
		id TEXT NOT NULL, 
		merchant_id UUID NOT NULL REFERENCES merchants(id),
		name TEXT NOT NULL,
		PRIMARY KEY (merchant_id, id)
	);

	-- INVENTORY ITEMS (The Ledger)
	CREATE TABLE IF NOT EXISTS inventory_items (
		sku TEXT NOT NULL,
		merchant_id UUID NOT NULL,
		location_id TEXT NOT NULL,
		total_qty INT NOT NULL,           
		reserved_qty INT NOT NULL DEFAULT 0,
		available_qty INT NOT NULL,       
		safety_stock INT DEFAULT 0,
		updated_at TIMESTAMP DEFAULT NOW(),
		PRIMARY KEY (merchant_id, location_id, sku),
		FOREIGN KEY (merchant_id, location_id) REFERENCES locations(merchant_id, id)
	);

	-- LOCKS (Active Carts)
	CREATE TABLE IF NOT EXISTS inventory_locks (
		lock_id UUID PRIMARY KEY,
		merchant_id UUID NOT NULL,
		sku TEXT NOT NULL,
		location_id TEXT NOT NULL,
		qty INT NOT NULL,
		expires_at TIMESTAMP NOT NULL,
		created_at TIMESTAMP DEFAULT NOW()
	);

	-- EVENTS (Audit Log)
	CREATE TABLE IF NOT EXISTS inventory_events (
		id SERIAL PRIMARY KEY,
		merchant_id UUID NOT NULL,
		event_type TEXT NOT NULL,
		sku TEXT NOT NULL,
		change_qty INT NOT NULL,
		reason TEXT,
		created_at TIMESTAMP DEFAULT NOW()
	);

	CREATE INDEX IF NOT EXISTS idx_locks_expiry ON inventory_locks(expires_at);
	CREATE INDEX IF NOT EXISTS idx_merchants_rapidapi ON merchants(rapidapi_user);
	`
	_, err := db.Exec(context.Background(), sql)
	if err != nil {
		log.Fatal("Schema Init Failed: ", err)
	}
}

// --- 3. BACKGROUND WORKERS ---

func StartTrafficMonitor() {
	ticker := time.NewTicker(1 * time.Second)
	for range ticker.C {
		rps := atomic.SwapInt64(&currentRPS, 0)
		// If RPS > 100, Switch to Panic Mode (Short TTL)
		if rps > 100 {
			atomic.StoreInt32(&highTrafficMode, 1)
		} else {
			atomic.StoreInt32(&highTrafficMode, 0)
		}
	}
}

func StartJanitor(db *pgxpool.Pool) {
	ticker := time.NewTicker(5 * time.Second)
	ctx := context.Background()
	for range ticker.C {
		tx, err := db.Begin(ctx)
		if err != nil {
			continue
		}

		// 1. Log Expired Items
		tx.Exec(ctx, `
			INSERT INTO inventory_events (merchant_id, event_type, sku, change_qty, reason)
			SELECT merchant_id, 'RELEASE', sku, qty, 'Expired TTL'
			FROM inventory_locks WHERE expires_at < NOW()
		`)

		// 2. Return Stock to Available
		_, err = tx.Exec(ctx, `
			UPDATE inventory_items ii
			SET available_qty = available_qty + il.qty,
			    reserved_qty = reserved_qty - il.qty
			FROM inventory_locks il
			WHERE ii.sku = il.sku 
			  AND ii.location_id = il.location_id 
			  AND ii.merchant_id = il.merchant_id
			  AND il.expires_at < NOW()
		`)

		// 3. Delete Locks
		tx.Exec(ctx, "DELETE FROM inventory_locks WHERE expires_at < NOW()")
		tx.Commit(ctx)
	}
}

// --- 4. MIDDLEWARE (RapidAPI + Bot Defense) ---

func RapidAPIMiddleware(db *pgxpool.Pool) fiber.Handler {
	secret := os.Getenv("RAPIDAPI_SECRET")
	return func(c *fiber.Ctx) error {
		// Security Check
		if c.Get("X-RapidAPI-Proxy-Secret") != secret {
			return c.Status(403).JSON(fiber.Map{"error": "Unauthorized"})
		}

		user := c.Get("X-RapidAPI-User")
		if user == "" {
			return c.Status(400).JSON(fiber.Map{"error": "No User ID"})
		}

		// JIT Provisioning
		var merchantID string
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		err := db.QueryRow(ctx, "SELECT id FROM merchants WHERE rapidapi_user = $1", user).Scan(&merchantID)
		if err != nil {
			// Create Merchant & Default Location
			tx, _ := db.Begin(ctx)
			tx.QueryRow(ctx, "INSERT INTO merchants (name, rapidapi_user) VALUES ($1, $2) RETURNING id", "User "+user, user).Scan(&merchantID)
			tx.Exec(ctx, "INSERT INTO locations (id, merchant_id, name) VALUES ('default', $1, 'Main')", merchantID)
			tx.Commit(ctx)
		}
		c.Locals("merchant_id", merchantID)
		return c.Next()
	}
}

func BotDefense() fiber.Handler {
	limiter := limiter.New(limiter.Config{
		Max: 50, Expiration: 1 * time.Minute,
		KeyGenerator: func(c *fiber.Ctx) string { return c.IP() },
	})
	return func(c *fiber.Ctx) error {
		atomic.AddInt64(&currentRPS, 1)
		return limiter(c)
	}
}

// --- 5. HANDLERS (The Headless Logic) ---

// REQUEST MODELS
type StockUpdate struct {
	SKU string `json:"sku"`
	Qty int    `json:"quantity"`
}
type LockReq struct {
	SKU string `json:"sku"`
	Qty int    `json:"quantity"`
}
type CommitReq struct {
	LockID string `json:"lock_id"`
}

func SetupRoutes(app *fiber.App, db *pgxpool.Pool) {
	api := app.Group("/api/v1", RapidAPIMiddleware(db), BotDefense())

	// A. ADMIN: SET STOCK (Reset/Init)
	api.Post("/inventory/sync", func(c *fiber.Ctx) error {
		mid := c.Locals("merchant_id").(string)
		var req StockUpdate
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).SendString("Bad Body")
		}

		// Upsert Item (available = total - reserved)
		// This respects existing locks even during a reset
		query := `
			INSERT INTO inventory_items (sku, merchant_id, location_id, total_qty, available_qty)
			VALUES ($1, $2, 'default', $3, $3)
			ON CONFLICT (merchant_id, location_id, sku) DO UPDATE SET 
			total_qty = $3, available_qty = $3 - inventory_items.reserved_qty
		`
		_, err := db.Exec(context.Background(), query, req.SKU, mid, req.Qty)
		if err != nil {
			return c.Status(500).SendString(err.Error())
		}

		return c.JSON(fiber.Map{"status": "updated", "sku": req.SKU})
	})

	// B. USER: LOCK (Add to Cart)
	api.Post("/inventory/lock", func(c *fiber.Ctx) error {
		mid := c.Locals("merchant_id").(string)
		var req LockReq
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).SendString("Bad Body")
		}

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		tx, _ := db.Begin(ctx)
		defer tx.Rollback(ctx)

		// Atomic Decrement
		tag, err := tx.Exec(ctx, `
			UPDATE inventory_items SET available_qty = available_qty - $1, reserved_qty = reserved_qty + $1 
			WHERE sku = $2 AND merchant_id = $3 AND location_id = 'default' 
			AND (available_qty - safety_stock) >= $1`, req.Qty, req.SKU, mid)

		if err != nil || tag.RowsAffected() == 0 {
			return c.Status(409).JSON(fiber.Map{"error": "Oversold"})
		}

		// Create Lock
		lockID := uuid.New()
		ttl := 900 // 15 mins
		if atomic.LoadInt32(&highTrafficMode) == 1 {
			ttl = 120
		} // 2 mins if bot attack
		exp := time.Now().Add(time.Duration(ttl) * time.Second)

		tx.Exec(ctx, `INSERT INTO inventory_locks (lock_id, merchant_id, sku, location_id, qty, expires_at) VALUES ($1, $2, $3, 'default', $4, $5)`, lockID, mid, req.SKU, req.Qty, exp)
		tx.Exec(ctx, `INSERT INTO inventory_events (merchant_id, event_type, sku, change_qty, reason) VALUES ($1, 'LOCK', $2, $3, 'Cart Add')`, mid, req.SKU, req.Qty)

		tx.Commit(ctx)
		return c.JSON(fiber.Map{"lock_id": lockID, "expires_at": exp})
	})

	// C. SYSTEM: COMMIT (Payment Success - Finalize Sale)
	api.Post("/inventory/commit", func(c *fiber.Ctx) error {
		mid := c.Locals("merchant_id").(string)
		var req CommitReq
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).SendString("Bad Body")
		}

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		tx, _ := db.Begin(ctx)
		defer tx.Rollback(ctx)

		// 1. Get Lock Details
		var sku string
		var qty int
		err := tx.QueryRow(ctx, "SELECT sku, qty FROM inventory_locks WHERE lock_id = $1 AND merchant_id = $2", req.LockID, mid).Scan(&sku, &qty)
		if err != nil {
			return c.Status(404).JSON(fiber.Map{"error": "Lock not found or expired"})
		}

		// 2. Reduce Total Qty (Permanent Sale), Reduce Reserved Qty (Clear hold)
		// Note: We do NOT add back to Available. It is gone.
		_, err = tx.Exec(ctx, `
			UPDATE inventory_items SET total_qty = total_qty - $1, reserved_qty = reserved_qty - $1 
			WHERE sku = $2 AND merchant_id = $3 AND location_id = 'default'`, qty, sku, mid)

		// 3. Delete Lock
		tx.Exec(ctx, "DELETE FROM inventory_locks WHERE lock_id = $1", req.LockID)

		// 4. Log Sale
		tx.Exec(ctx, `INSERT INTO inventory_events (merchant_id, event_type, sku, change_qty, reason) VALUES ($1, 'SALE', $2, $3, 'Committed')`, mid, sku, -qty)

		tx.Commit(ctx)
		return c.JSON(fiber.Map{"status": "sold", "sku": sku})
	})
}

// --- 6. ENTRY POINT ---

func main() {
	if os.Getenv("RAPIDAPI_SECRET") == "" {
		log.Println("WARNING: No Secret Set")
	}

	db := ConnectDB()
	defer db.Close()
	InitializeSchema(db)

	go StartJanitor(db)
	go StartTrafficMonitor()

	app := fiber.New()
	app.Use(logger.New())
	app.Use(recover.New())

	SetupRoutes(app, db)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Fatal(app.Listen(":" + port))
}
