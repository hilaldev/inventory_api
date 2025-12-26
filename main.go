package main

import (
	"log"
	"os"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"
)

func main() {
	// 1. Environment Check
	if os.Getenv("RAPIDAPI_SECRET") == "" {
		log.Println("WARNING: RAPIDAPI_SECRET is missing. Requests will fail auth.")
	}

	// 2. Database & Workers
	db := ConnectDB()
	defer db.Close()

	StartBackgroundWorkers(db)
	go StartTrafficMonitor()

	// 3. Web Server
	app := fiber.New()
	app.Use(logger.New())
	app.Use(recover.New())

	// 4. Routes
	// All routes behind RapidAPI Auth & Bot Defense
	api := app.Group("/api/v1", RapidAPIMiddleware(db), BotDefenseMiddleware())

	handler := &Handler{DB: db}
	
	api.Post("/inventory/sync", handler.SyncInventory) // Setup Stock
	api.Post("/inventory/lock", handler.LockInventory) // Reserve Stock

	// 5. Start
	port := os.Getenv("PORT")
	if port == "" { port = "8080" }
	
	log.Println("Inventory API Live on port " + port)
	log.Fatal(app.Listen(":" + port))
}
