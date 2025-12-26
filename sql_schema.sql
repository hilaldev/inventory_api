-- 1. Enable UUID extension
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- 2. MERCHANTS (Linked to RapidAPI Users)
CREATE TABLE IF NOT EXISTS merchants (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    rapidapi_user TEXT UNIQUE NOT NULL, -- The ID from RapidAPI
    name TEXT NOT NULL,
    created_at TIMESTAMP DEFAULT NOW()
);

-- 3. LOCATIONS (Scoped by Merchant)
CREATE TABLE IF NOT EXISTS locations (
    id TEXT NOT NULL, 
    merchant_id UUID NOT NULL REFERENCES merchants(id),
    name TEXT NOT NULL,
    priority INT DEFAULT 0,
    PRIMARY KEY (merchant_id, id)
);

-- 4. INVENTORY ITEMS (The Ledger)
CREATE TABLE IF NOT EXISTS inventory_items (
    sku TEXT NOT NULL,
    merchant_id UUID NOT NULL,
    location_id TEXT NOT NULL,
    total_qty INT NOT NULL,           -- Physical count in warehouse
    reserved_qty INT NOT NULL DEFAULT 0, -- Locked by our API
    available_qty INT NOT NULL,       -- Sellable count
    safety_stock INT DEFAULT 0,       -- Buffer
    updated_at TIMESTAMP DEFAULT NOW(),
    PRIMARY KEY (merchant_id, location_id, sku),
    FOREIGN KEY (merchant_id, location_id) REFERENCES locations(merchant_id, id)
);

-- 5. LOCKS (Active Reservations)
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

-- 6. EVENTS (Audit Trail)
CREATE TABLE IF NOT EXISTS inventory_events (
    id SERIAL PRIMARY KEY,
    merchant_id UUID NOT NULL,
    event_type TEXT NOT NULL, -- LOCK, RELEASE, SYNC
    sku TEXT NOT NULL,
    location_id TEXT,
    change_qty INT NOT NULL,
    reason TEXT,
    created_at TIMESTAMP DEFAULT NOW()
);

-- 7. SYNC JOBS (Async Updates)
CREATE TABLE IF NOT EXISTS sync_jobs (
    id SERIAL PRIMARY KEY,
    merchant_id UUID NOT NULL,
    target_platform TEXT NOT NULL,
    sku TEXT NOT NULL,
    status TEXT DEFAULT 'PENDING',
    created_at TIMESTAMP DEFAULT NOW()
);

-- INDEXES (Performance & Multi-Tenancy)
CREATE INDEX idx_merchants_rapidapi ON merchants(rapidapi_user);
CREATE INDEX idx_locks_expiry ON inventory_locks(expires_at);
CREATE INDEX idx_locks_merchant ON inventory_locks(merchant_id);
