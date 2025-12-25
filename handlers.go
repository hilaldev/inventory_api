package main

import (
    "context"
    "time"

    "github.com/gofiber/fiber/v2"
    "github.com/google/uuid"
)

func LockInventory(c *fiber.Ctx) error {
    var req LockRequest
    if err := c.BodyParser(&req); err != nil {
        return c.Status(400).JSON(fiber.Map{"error": "invalid payload"})
    }

    tx, err := DB.Begin(context.Background())
    if err != nil {
        return c.SendStatus(500)
    }
    defer tx.Rollback(context.Background())

    var available, safety int
    err = tx.QueryRow(context.Background(),
        `SELECT available_qty, safety_qty 
         FROM inventory_items 
         WHERE sku=$1 
         FOR UPDATE`,
        req.SKU,
    ).Scan(&available, &safety)

    if err != nil {
        return c.Status(404).JSON(fiber.Map{"error": "sku not found"})
    }

    if available-safety < req.Quantity {
        return c.Status(409).JSON(fiber.Map{"error": "insufficient stock"})
    }

    lockID := uuid.New()
    expires := time.Now().Add(time.Duration(req.TTL) * time.Second)

    _, err = tx.Exec(context.Background(),
        `INSERT INTO inventory_locks 
         (lock_id, sku, qty, order_ref, expires_at)
         VALUES ($1,$2,$3,$4,$5)`,
        lockID, req.SKU, req.Quantity, req.OrderRef, expires,
    )
    if err != nil {
        return c.SendStatus(500)
    }

    _, err = tx.Exec(context.Background(),
        `UPDATE inventory_items
         SET reserved_qty = reserved_qty + $1,
             available_qty = available_qty - $1,
             updated_at = NOW()
         WHERE sku=$2`,
        req.Quantity, req.SKU,
    )
    if err != nil {
        return c.SendStatus(500)
    }

    tx.Commit(context.Background())

    return c.JSON(fiber.Map{
        "lock_id": lockID,
        "status":  "locked",
    })
}

func ConfirmInventory(c *fiber.Ctx) error {
    var req ConfirmRequest
    if err := c.BodyParser(&req); err != nil {
        return c.Status(400).JSON(fiber.Map{"error": "invalid payload"})
    }

    _, err := DB.Exec(context.Background(),
        `DELETE FROM inventory_locks WHERE lock_id=$1`,
        req.LockID,
    )

    if err != nil {
        return c.SendStatus(500)
    }

    return c.JSON(fiber.Map{"status": "confirmed"})
}

func ReleaseInventory(c *fiber.Ctx) error {
    var req ConfirmRequest
    if err := c.BodyParser(&req); err != nil {
        return c.Status(400).JSON(fiber.Map{"error": "invalid payload"})
    }

    tx, err := DB.Begin(context.Background())
    if err != nil {
        return c.SendStatus(500)
    }
    defer tx.Rollback(context.Background())

    var sku string
    var qty int
    err = tx.QueryRow(context.Background(),
        `SELECT sku, qty FROM inventory_locks WHERE lock_id=$1 FOR UPDATE`,
        req.LockID,
    ).Scan(&sku, &qty)

    if err != nil {
        return c.Status(404).JSON(fiber.Map{"error": "lock not found"})
    }

    _, err = tx.Exec(context.Background(),
        `DELETE FROM inventory_locks WHERE lock_id=$1`,
        req.LockID,
    )
    if err != nil {
        return c.SendStatus(500)
    }

    _, err = tx.Exec(context.Background(),
        `UPDATE inventory_items
         SET reserved_qty = reserved_qty - $1,
             available_qty = available_qty + $1,
             updated_at = NOW()
         WHERE sku=$2`,
        qty, sku,
    )
    if err != nil {
        return c.SendStatus(500)
    }

    tx.Commit(context.Background())

    return c.JSON(fiber.Map{"status": "released"})
}
