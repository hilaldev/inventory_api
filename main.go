package main

import (
	"log"
	"os"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"
)

func main() {
	if os.Getenv("RAPIDAPI_SECRET") == "" {
		log.Println("WARNING: RAPIDAPI_SECRET is missing. Requests will fail auth.")
	}

	// 1. Connect to DB
	db := ConnectDB()
	defer db.Close()

	// 2. AUTO-MIGRATE (Runs the SQL automatically)
	InitializeSchema(db)

	// 3. Start Workers
	StartBackgroundWorkers(db)
	go StartTrafficMonitor()

	// 4. Start Web Server
	app := fiber.New()
	app.Use(logger.New())
	app.Use(recover.New())

	api := app.Group("/api/v1", RapidAPIMiddleware(db), BotDefenseMiddleware())

	handler := &Handler{DB: db}
	
	api.Post("/inventory/sync", handler.SyncInventory)
	api.Post("/inventory/lock", handler.LockInventory)

	port := os.Getenv("PORT")
	if port == "" { port = "8080" }
	
	log.Println("Inventory API Live on port " + port)
	log.Fatal(app.Listen(":" + port))
}
