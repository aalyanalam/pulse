package main

import (
	"context"
	"database/sql"
	"expvar"
	_ "github.com/lib/pq"
	"log/slog"
	"net/http"
	_ "net/http/pprof" // Registers profiles on http.DefaultServeMux, served only on DEBUG_ADDR.
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
	if err := backend.Migrate(startup, db); err != nil {
		return err
	}
	if addr := os.Getenv("DEBUG_ADDR"); addr != "" {
		// Diagnostics stay off the public API mux. Compose publishes this port on
		// loopback only; pprof and pool statistics must not be exposed remotely.
		expvar.Publish("db", expvar.Func(func() any { return db.Stats() }))
		expvar.Publish("ingest", expvar.Func(backend.ReadIngestStats))
		go func() {
			if err := httpserver.Run(ctx, addr, http.DefaultServeMux); err != nil {
				slog.Error("debug server stopped", "error", err)
			}
		}()
	}
	evaluationCtx, stopEvaluation := context.WithCancel(ctx)
	defer stopEvaluation()
	evaluationDone := make(chan struct{})
	go func() { defer close(evaluationDone); backend.API{DB: db}.Evaluate(evaluationCtx) }()
	defer func() { stopEvaluation(); <-evaluationDone }()
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
