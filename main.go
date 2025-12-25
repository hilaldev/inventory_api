package main

import (
	"log"
	"os"

	"github.com/gofiber/fiber/v2"
)

func main() {
	// Declare the app
	app := fiber.New()

	// Routes
	api := app.Group("/api/v1")
	api.Post("/inventory/lock", LockInventory)
	api.Post("/inventory/confirm", ConfirmInventory)
	api.Post("/inventory/release", ReleaseInventory)

	// Listen on PORT
	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}

	log.Fatal(app.Listen(":" + port))
}
