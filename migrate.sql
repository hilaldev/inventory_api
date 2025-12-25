CREATE TABLE inventory_items (
  sku TEXT PRIMARY KEY,
  total_qty INT NOT NULL,
  reserved_qty INT NOT NULL DEFAULT 0,
  available_qty INT NOT NULL,
  safety_qty INT NOT NULL DEFAULT 0,
  updated_at TIMESTAMP DEFAULT NOW()
);

CREATE TABLE inventory_locks (
  lock_id UUID PRIMARY KEY,
  sku TEXT NOT NULL,
  qty INT NOT NULL,
  order_ref TEXT NOT NULL,
  expires_at TIMESTAMP NOT NULL,
  created_at TIMESTAMP DEFAULT NOW()
);

CREATE INDEX idx_inventory_locks_sku ON inventory_locks(sku);
