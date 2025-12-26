package main

import "time"

// LockRequest: Incoming request to reserve stock
type LockRequest struct {
	SKU        string `json:"sku"`
	Quantity   int    `json:"quantity"`
	OrderRef   string `json:"order_ref"`
	LocationID string `json:"location_id"` // Optional
}

// SyncRequest: Merchant pushing their physical stock levels
type SyncRequest struct {
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"` // Total physical stock
}

// LockResponse: Success response
type LockResponse struct {
	LockID     string    `json:"lock_id"`
	Status     string    `json:"status"`
	ExpiresAt  time.Time `json:"expires_at"`
	LocationID string    `json:"location_id"`
}
