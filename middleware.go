package main

import (
	"context"
	"os"
	"sync/atomic"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RapidAPIMiddleware: Handles Authentication and JIT Provisioning
func RapidAPIMiddleware(db *pgxpool.Pool) fiber.Handler {
	proxySecret := os.Getenv("RAPIDAPI_SECRET")

	return func(c *fiber.Ctx) error {
		// 1. Security Check
		if c.Get("X-RapidAPI-Proxy-Secret") != proxySecret {
			return c.Status(403).JSON(fiber.Map{"error": "Unauthorized Proxy Access"})
		}

		// 2. Identify User
		rapidUser := c.Get("X-RapidAPI-User")
		if rapidUser == "" {
			return c.Status(400).JSON(fiber.Map{"error": "Missing User Identity"})
		}

		// 3. JIT Provisioning
		// Check if user exists in our DB, if not create them.
		var merchantID string
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		err := db.QueryRow(ctx, "SELECT id FROM merchants WHERE rapidapi_user = $1", rapidUser).Scan(&merchantID)

		if err != nil {
			// Create New Account
			tx, _ := db.Begin(ctx)
			err = tx.QueryRow(ctx, 
				"INSERT INTO merchants (name, rapidapi_user) VALUES ($1, $2) RETURNING id", 
				"User "+rapidUser, rapidUser,
			).Scan(&merchantID)
			
			// Create Default Location
			tx.Exec(ctx, 
				"INSERT INTO locations (id, merchant_id, name, priority) VALUES ('default', $1, 'Main Warehouse', 0)", 
				merchantID,
			)
			tx.Commit(ctx)
		}

		c.Locals("merchant_id", merchantID)
		return c.Next()
	}
}

// BotDefenseMiddleware: Rate limiting and traffic counting
func BotDefenseMiddleware() fiber.Handler {
	limiter := limiter.New(limiter.Config{
		Max:        30, // 30 reqs/min per IP
		Expiration: 1 * time.Minute,
		KeyGenerator: func(c *fiber.Ctx) string {
			return c.IP()
		},
		LimitReached: func(c *fiber.Ctx) error {
			return c.Status(429).JSON(fiber.Map{"error": "Rate limit exceeded"})
		},
	})

	return func(c *fiber.Ctx) error {
		atomic.AddInt64(&currentRPS, 1) // Count traffic
		return limiter(c)
	}
}
