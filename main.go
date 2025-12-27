package main

import (
	"context"
	"log"
	"os"
	"strconv"
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
	log.Println("Initializing Schema...")
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

// StartJanitor: UPDATED WITH AGGREGATION FIX
func StartJanitor(db *pgxpool.Pool) {
	ticker := time.NewTicker(5 * time.Second)
	ctx := context.Background()
	for range ticker.C {
		tx, err := db.Begin(ctx)
		if err != nil {
			continue
		}

		// 1. Aggregate Expired Qty per SKU (The Fix)
		// We calculate the total to release FIRST, then update the item ONCE.
		_, err = tx.Exec(ctx, `
			WITH expired_agg AS (
				SELECT merchant_id, location_id, sku, SUM(qty) as total_qty_to_release
				FROM inventory_locks
				WHERE expires_at < NOW()
				GROUP BY merchant_id, location_id, sku
			)
			UPDATE inventory_items ii
			SET available_qty = available_qty + ea.total_qty_to_release,
			    reserved_qty = reserved_qty - ea.total_qty_to_release
			FROM expired_agg ea
			WHERE ii.sku = ea.sku 
			  AND ii.location_id = ea.location_id 
			  AND ii.merchant_id = ea.merchant_id;
		`)

		// 2. Log Events
		tx.Exec(ctx, `
			INSERT INTO inventory_events (merchant_id, event_type, sku, change_qty, reason)
			SELECT merchant_id, 'RELEASE', sku, qty, 'Expired TTL'
			FROM inventory_locks WHERE expires_at < NOW()
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
			tx.Exec(ctx, "INSERT INTO locations (id, merchant_id, name) VALUES ('default', $1, 'Main Warehouse')", merchantID)
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

// --- 5. MODELS & HANDLERS ---

// Request Models
type LocationReq struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type StockUpdate struct {
	SKU        string `json:"sku"`
	Qty        int    `json:"quantity"`
	LocationID string `json:"location_id"`
}
type LockReq struct {
	SKU        string `json:"sku"`
	Qty        int    `json:"quantity"`
	LocationID string `json:"location_id"`
}
type CommitReq struct {
	LockID string `json:"lock_id"`
}

// Response Models
type InventoryItem struct {
	LocationID   string    `json:"location_id"`
	SKU          string    `json:"sku"`
	TotalQty     int       `json:"total_qty"`
	ReservedQty  int       `json:"reserved_qty"`
	AvailableQty int       `json:"available_qty"`
	UpdatedAt    time.Time `json:"updated_at"`
}
type ActiveLock struct {
	LockID     string    `json:"lock_id"`
	LocationID string    `json:"location_id"`
	SKU        string    `json:"sku"`
	Qty        int       `json:"qty"`
	ExpiresAt  time.Time `json:"expires_at"`
}
type InventoryEvent struct {
	EventType string    `json:"event_type"`
	ChangeQty int       `json:"change_qty"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"created_at"`
}

func SetupRoutes(app *fiber.App, db *pgxpool.Pool) {
	//  PUBLIC ENDPOINT (For RapidAPI Health Check) ---
	app.Get("/ping", func(c *fiber.Ctx) error {
		return c.Status(200).JSON(fiber.Map{
			"status":    "pong",
			"service":   "FlashLock API",
			"timestamp": time.Now().Unix(),
		})
	})

	api := app.Group("/api/v1", RapidAPIMiddleware(db), BotDefense())

	// --- 1. LOCATIONS MANAGEMENT ---

	// Create Location
	api.Post("/locations", func(c *fiber.Ctx) error {
		mid := c.Locals("merchant_id").(string)
		var req LocationReq
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).SendString("Bad Body")
		}

		_, err := db.Exec(context.Background(),
			"INSERT INTO locations (id, merchant_id, name) VALUES ($1, $2, $3) ON CONFLICT (merchant_id, id) DO UPDATE SET name=$3",
			req.ID, mid, req.Name)
		if err != nil {
			return c.Status(500).SendString(err.Error())
		}
		return c.JSON(fiber.Map{"status": "created", "location_id": req.ID})
	})

	// List Locations
	api.Get("/locations", func(c *fiber.Ctx) error {
		mid := c.Locals("merchant_id").(string)
		rows, _ := db.Query(context.Background(), "SELECT id, name FROM locations WHERE merchant_id=$1", mid)
		defer rows.Close()
		var locs []LocationReq
		for rows.Next() {
			var l LocationReq
			rows.Scan(&l.ID, &l.Name)
			locs = append(locs, l)
		}
		return c.JSON(locs)
	})

	// --- 2. WRITE ENDPOINTS ---

	// Sync Stock (supports location_id)
	api.Post("/inventory/sync", func(c *fiber.Ctx) error {
		mid := c.Locals("merchant_id").(string)
		var req StockUpdate
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).SendString("Bad Body")
		}

		loc := req.LocationID
		if loc == "" {
			loc = "default"
		} // Default fallback

		// Upsert Item (available = total - reserved)
		// This respects existing locks even during a reset
		query := `
			INSERT INTO inventory_items (sku, merchant_id, location_id, total_qty, available_qty)
			VALUES ($1, $2, $3, $4, $4)
			ON CONFLICT (merchant_id, location_id, sku) DO UPDATE SET 
			total_qty = $4, available_qty = $4 - inventory_items.reserved_qty
		`
		_, err := db.Exec(context.Background(), query, req.SKU, mid, loc, req.Qty)
		if err != nil {
			return c.Status(500).SendString("Error syncing. Does location exist?")
		}

		return c.JSON(fiber.Map{"status": "updated", "sku": req.SKU, "location": loc})
	})

	// LOCK (Add to Cart - supports location_id)
	api.Post("/inventory/lock", func(c *fiber.Ctx) error {
		mid := c.Locals("merchant_id").(string)
		var req LockReq
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).SendString("Bad Body")
		}

		loc := req.LocationID
		if loc == "" {
			loc = "default"
		}

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		tx, _ := db.Begin(ctx)
		defer tx.Rollback(ctx)

		// Atomic Decrement
		tag, err := tx.Exec(ctx, `
			UPDATE inventory_items SET available_qty = available_qty - $1, reserved_qty = reserved_qty + $1 
			WHERE sku = $2 AND merchant_id = $3 AND location_id = $4 
			AND (available_qty - safety_stock) >= $1`, req.Qty, req.SKU, mid, loc)

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

		tx.Exec(ctx, `INSERT INTO inventory_locks (lock_id, merchant_id, sku, location_id, qty, expires_at) VALUES ($1, $2, $3, $4, $5, $6)`, lockID, mid, req.SKU, loc, req.Qty, exp)
		tx.Exec(ctx, `INSERT INTO inventory_events (merchant_id, event_type, sku, change_qty, reason) VALUES ($1, 'LOCK', $2, $3, 'Cart Add')`, mid, req.SKU, req.Qty)

		tx.Commit(ctx)
		return c.JSON(fiber.Map{"lock_id": lockID, "expires_at": exp, "location": loc})
	})

	// COMMIT (Payment Success - Finalize Sale)
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

		// 1. Get Lock Details (Now includes location_id)
		var sku, loc string
		var qty int
		err := tx.QueryRow(ctx, "SELECT sku, qty, location_id FROM inventory_locks WHERE lock_id = $1 AND merchant_id = $2", req.LockID, mid).Scan(&sku, &qty, &loc)
		if err != nil {
			return c.Status(404).JSON(fiber.Map{"error": "Lock not found or expired"})
		}

		// 2. Reduce Total Qty (Permanent Sale), Reduce Reserved Qty (Clear hold)
		_, err = tx.Exec(ctx, `
			UPDATE inventory_items SET total_qty = total_qty - $1, reserved_qty = reserved_qty - $1 
			WHERE sku = $2 AND merchant_id = $3 AND location_id = $4`, qty, sku, mid, loc)

		// 3. Delete Lock
		tx.Exec(ctx, "DELETE FROM inventory_locks WHERE lock_id = $1", req.LockID)

		// 4. Log Sale
		tx.Exec(ctx, `INSERT INTO inventory_events (merchant_id, event_type, sku, change_qty, reason) VALUES ($1, 'SALE', $2, $3, 'Committed')`, mid, sku, -qty)

		tx.Commit(ctx)
		return c.JSON(fiber.Map{"status": "sold", "sku": sku})
	})

	// --- 3. READ ENDPOINTS ---

	// Get Inventory (Now returns array of locations)
	api.Get("/inventory/:sku", func(c *fiber.Ctx) error {
		mid := c.Locals("merchant_id").(string)
		sku := c.Params("sku")

		rows, err := db.Query(context.Background(),
			"SELECT location_id, total_qty, reserved_qty, available_qty, updated_at FROM inventory_items WHERE sku=$1 AND merchant_id=$2",
			sku, mid,
		)
		if err != nil {
			return c.Status(500).SendString("DB Error")
		}
		defer rows.Close()

		items := make([]InventoryItem, 0)
		for rows.Next() {
			var i InventoryItem
			i.SKU = sku
			rows.Scan(&i.LocationID, &i.TotalQty, &i.ReservedQty, &i.AvailableQty, &i.UpdatedAt)
			items = append(items, i)
		}

		return c.JSON(items)
	})

	// Get Locks
	api.Get("/locks", func(c *fiber.Ctx) error {
		mid := c.Locals("merchant_id").(string)
		limit, _ := strconv.Atoi(c.Query("limit", "20"))
		offset, _ := strconv.Atoi(c.Query("offset", "0"))

		rows, err := db.Query(context.Background(),
			"SELECT lock_id, location_id, sku, qty, expires_at FROM inventory_locks WHERE merchant_id=$1 ORDER BY created_at DESC LIMIT $2 OFFSET $3",
			mid, limit, offset,
		)
		if err != nil {
			return c.Status(500).SendString("DB Error")
		}
		defer rows.Close()

		locks := make([]ActiveLock, 0)
		for rows.Next() {
			var lock ActiveLock
			rows.Scan(&lock.LockID, &lock.LocationID, &lock.SKU, &lock.Qty, &lock.ExpiresAt)
			locks = append(locks, lock)
		}
		return c.JSON(locks)
	})

	// Get History
	api.Get("/inventory/:sku/history", func(c *fiber.Ctx) error {
		mid := c.Locals("merchant_id").(string)
		sku := c.Params("sku")
		limit, _ := strconv.Atoi(c.Query("limit", "50"))
		offset, _ := strconv.Atoi(c.Query("offset", "0"))

		rows, err := db.Query(context.Background(),
			"SELECT event_type, change_qty, reason, created_at FROM inventory_events WHERE merchant_id=$1 AND sku=$2 ORDER BY created_at DESC LIMIT $3 OFFSET $4",
			mid, sku, limit, offset,
		)
		if err != nil {
			return c.Status(500).SendString("DB Error")
		}
		defer rows.Close()

		events := make([]InventoryEvent, 0)
		for rows.Next() {
			var event InventoryEvent
			rows.Scan(&event.EventType, &event.ChangeQty, &event.Reason, &event.CreatedAt)
			events = append(events, event)
		}
		return c.JSON(events)
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
