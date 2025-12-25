package main

import (
    "log"

    "github.com/gofiber/fiber/v2"
)

func main() {
    port := os.Getenv("PORT")
    if port == "" {
        port = "3000"
    }
    log.Fatal(app.Listen(":" + port))

    if err := ConnectDB(); err != nil {
        log.Fatal(err)
    }

    app := fiber.New()

    api := app.Group("/api/v1")
    api.Post("/inventory/lock", LockInventory)
    api.Post("/inventory/confirm", ConfirmInventory)
    api.Post("/inventory/release", ReleaseInventory)

    log.Fatal(app.Listen(":3000"))
}
