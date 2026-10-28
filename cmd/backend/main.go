package main

import (
	"context"
	"database/sql"
	_ "github.com/lib/pq"
	"log/slog"
	"os"
	"os/signal"
	"pulse/internal/backend"
	"pulse/internal/httpserver"
	"syscall"
	"time"
)

func run(ctx context.Context) error {
	db, err := sql.Open("postgres", httpserver.Env("DATABASE_URL", "postgres://pulse:pulse@localhost:5432/pulse?sslmode=disable"))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	startup, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := db.ExecContext(startup, backend.Schema); err != nil {
		return err
	}
	return httpserver.Run(ctx, httpserver.Env("LISTEN_ADDR", ":8080"), backend.API{DB: db}.Handler())
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx); err != nil {
		slog.Error("backend stopped", "error", err)
		os.Exit(1)
	}
}
