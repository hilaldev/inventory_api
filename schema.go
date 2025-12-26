package main

import (
	"context"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"
)

func InitializeSchema(db *pgxpool.Pool) {
	log.Println("Checking database schema...")

	schema := `
	-- 1. Enable UUID extension
	CREATE EXTENSION IF NOT EXISTS "pgcrypto";

	-- 2. MERCHANTS
	CREATE TABLE IF NOT EXISTS merchants (
		id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
		rapidapi_user TEXT UNIQUE NOT NULL,
		name TEXT NOT NULL,
		created_at TIMESTAMP DEFAULT NOW()
	);

	-- 3. LOCATIONS
	CREATE TABLE IF NOT EXISTS locations (
		id TEXT NOT NULL, 
		merchant_id UUID NOT NULL REFERENCES merchants(id),
		name TEXT NOT NULL,
		priority INT DEFAULT 0,
		PRIMARY KEY (merchant_id, id)
	);

	-- 4. INVENTORY ITEMS
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

	-- 5. LOCKS
	CREATE TABLE IF NOT EXISTS inventory_locks (
		lock_id UUID PRIMARY KEY,
		merchant_id UUID NOT NULL REFERENCES merchants(id),
		sku TEXT NOT NULL,
		location_id TEXT NOT NULL,
		qty INT NOT NULL,
		order_ref TEXT NOT NULL,
		expires_at TIMESTAMP NOT NULL,
		created_at TIMESTAMP DEFAULT NOW()
	);

	-- 6. EVENTS
	CREATE TABLE IF NOT EXISTS inventory_events (
		id SERIAL PRIMARY KEY,
		merchant_id UUID NOT NULL,
		event_type TEXT NOT NULL,
		sku TEXT NOT NULL,
		location_id TEXT,
		change_qty INT NOT NULL,
		reason TEXT,
		created_at TIMESTAMP DEFAULT NOW()
	);

	-- 7. SYNC JOBS
	CREATE TABLE IF NOT EXISTS sync_jobs (
		id SERIAL PRIMARY KEY,
		merchant_id UUID NOT NULL,
		target_platform TEXT NOT NULL,
		sku TEXT NOT NULL,
		status TEXT DEFAULT 'PENDING',
		created_at TIMESTAMP DEFAULT NOW()
	);

	-- INDEXES
	CREATE INDEX IF NOT EXISTS idx_merchants_rapidapi ON merchants(rapidapi_user);
	CREATE INDEX IF NOT EXISTS idx_locks_expiry ON inventory_locks(expires_at);
	CREATE INDEX IF NOT EXISTS idx_locks_merchant ON inventory_locks(merchant_id);
	`

	_, err := db.Exec(context.Background(), schema)
	if err != nil {
		log.Fatal("Failed to migrate database schema: ", err)
	}

	log.Println("Database schema initialized successfully.")
}
