package main

import (
	"log"
	"os"

	"github.com/gofiber/fiber/v2"
)

func main() {
	// Ensure DATABASE_URL exists
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL is missing")
	}
	log.Println("DATABASE_URL detected")

	app := fiber.New()

	db := ConnectDB()
	handler := &Handler{DB: db}

	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ok"})
	})

	app.Post("/api/v1/inventory/lock", handler.LockInventory)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Println("Starting server on port", port)
	log.Fatal(app.Listen(":" + port))
}
