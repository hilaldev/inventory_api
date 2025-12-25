package main

import (
    "context"
    "os"

    "github.com/jackc/pgx/v5/pgxpool"
)

var DB *pgxpool.Pool

func ConnectDB() error {
    dsn := os.Getenv("DATABASE_URL")
    pool, err := pgxpool.New(context.Background(), dsn)
    if err != nil {
        return err
    }
    DB = pool
    return nil
}
