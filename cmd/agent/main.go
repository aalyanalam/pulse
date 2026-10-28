package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"pulse/internal/httpserver"
	"pulse/internal/telemetry"
	"syscall"
	"time"
)

func collect(ctx context.Context, client *http.Client, url string) (telemetry.Stats, error) {
	var stats telemetry.Stats
	req, err := http.NewRequestWithContext(ctx, "GET", url+"/stats", nil)
	if err != nil {
		return stats, err
	}
	res, err := client.Do(req)
	if err != nil {
		return stats, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return stats, fmt.Errorf("stats returned HTTP %d", res.StatusCode)
	}
	err = json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&stats)
	return stats, err
}

func send(ctx context.Context, client *http.Client, url string, sample telemetry.Sample) error {
	body, err := json.Marshal(telemetry.Batch{Samples: []telemetry.Sample{sample}})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url+"/api/v1/telemetry/batches", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
	if res.StatusCode != http.StatusAccepted {
		return fmt.Errorf("ingest returned HTTP %d", res.StatusCode)
	}
	return nil
}

func run(ctx context.Context) error {
	interval, err := time.ParseDuration(httpserver.Env("COLLECT_INTERVAL", "3s"))
	if err != nil || interval <= 0 {
		return fmt.Errorf("COLLECT_INTERVAL must be a positive duration")
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var sequence int64
	for {
		collectedAt := time.Now().UTC()
		stats, err := collect(ctx, client, httpserver.Env("APP_URL", "http://localhost:8081"))
		if err == nil {
			sequence++
			sample := telemetry.Sample{NodeID: httpserver.Env("NODE_ID", "node-1"), SessionID: hex.EncodeToString(id[:]), Sequence: sequence, CollectedAt: collectedAt, Stats: stats}
			err = send(ctx, client, httpserver.Env("BACKEND_URL", "http://localhost:8080"), sample)
		}
		if err != nil && ctx.Err() == nil {
			slog.Warn("sample lost", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx); err != nil {
		slog.Error("agent stopped", "error", err)
		os.Exit(1)
	}
}
