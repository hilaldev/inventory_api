package main

type LockRequest struct {
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
	OrderRef string `json:"order_ref"`
	TTL      int    `json:"ttl"` // seconds
}

type ConfirmRequest struct {
	LockID string `json:"lock_id"`
}
