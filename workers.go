package main

import (
	"context"
	"log"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// --- TRAFFIC MONITOR (Bot Defense) ---

var (
	currentRPS      int64
	highTrafficMode int32 // 0 = Normal, 1 = Panic Mode
)

func StartTrafficMonitor() {
	ticker := time.NewTicker(1 * time.Second)
	for range ticker.C {
		rps := atomic.SwapInt64(&currentRPS, 0)
		// If requests > 100/sec, trigger Panic Mode (Short TTL)
		if rps > 100 {
			atomic.StoreInt32(&highTrafficMode, 1)
		} else {
			atomic.StoreInt32(&highTrafficMode, 0)
		}
	}
}

// GetDynamicTTL determines lock duration based on traffic load
func GetDynamicTTL() int {
	if atomic.LoadInt32(&highTrafficMode) == 1 {
		return 120 // 2 Minutes (Panic Mode)
	}
	return 900 // 15 Minutes (Standard)
}

// --- WORKERS ---

func StartBackgroundWorkers(db *pgxpool.Pool) {
	go janitorLoop(db)
	go syncEngineLoop(db)
}

// janitorLoop: Cleans up expired locks across ALL merchants
func janitorLoop(db *pgxpool.Pool) {
	ticker := time.NewTicker(5 * time.Second)
	ctx := context.Background()

	for range ticker.C {
		tx, err := db.Begin(ctx)
		if err != nil {
			continue
		}

		// 1. Reclaim Stock
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

		// 2. Delete Expired Locks
		_, err = tx.Exec(ctx, "DELETE FROM inventory_locks WHERE expires_at < NOW()")
		
		tx.Commit(ctx)
	}
}

// syncEngineLoop: Simulates pushing updates to external platforms
func syncEngineLoop(db *pgxpool.Pool) {
	ticker := time.NewTicker(1 * time.Second)
	ctx := context.Background()

	for range ticker.C {
		rows, _ := db.Query(ctx, "SELECT id, target_platform, sku FROM sync_jobs WHERE status = 'PENDING' LIMIT 10")
		
		for rows.Next() {
			var id int
			var target, sku string
			rows.Scan(&id, &target, &sku)

			go func(jobID int) {
				// Simulate API latency
				time.Sleep(100 * time.Millisecond)
				db.Exec(context.Background(), "UPDATE sync_jobs SET status = 'COMPLETED' WHERE id = $1", jobID)
			}(id)
		}
		rows.Close()
	}
}
