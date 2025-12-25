package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func ConnectDB() *pgxpool.Pool {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL is not set")
	}
	log.Println("Connecting to database with:", dsn)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatal("Failed to create pool:", err)
	}

	if err := pool.Ping(ctx); err != nil {
		log.Fatal("Failed to ping database:", err)
	}

	log.Println("Database connected successfully")
	return pool
}
