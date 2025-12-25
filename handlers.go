package main

import (
	"context"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Handler struct {
	DB *pgxpool.Pool
}

func (h *Handler) LockInventory(c *fiber.Ctx) error {
	ctx := context.Background()

	tx, err := h.DB.Begin(ctx)
	if err != nil {
		return fiber.NewError(500, err.Error())
	}
	defer tx.Rollback(ctx)

	lockID := uuid.New()

	_, err = tx.Exec(ctx,
		`INSERT INTO inventory_locks (id) VALUES ($1)`,
		lockID,
	)
	if err != nil {
		return fiber.NewError(500, err.Error())
	}

	if err := tx.Commit(ctx); err != nil {
		return fiber.NewError(500, err.Error())
	}

	return c.JSON(fiber.Map{
		"lock_id": lockID,
		"status":  "locked",
	})
}
