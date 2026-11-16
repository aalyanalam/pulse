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
	"strconv"
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

func send(ctx context.Context, client *http.Client, url string, samples []telemetry.Sample) error {
	body, err := json.Marshal(telemetry.Batch{Samples: samples})
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
		return ingestStatus(res.StatusCode)
	}
	return nil
}

func run(ctx context.Context) error {
	interval, err := time.ParseDuration(httpserver.Env("COLLECT_INTERVAL", "3s"))
	if err != nil || interval <= 0 {
		return fmt.Errorf("COLLECT_INTERVAL must be a positive duration")
	}
	flush, err := time.ParseDuration(httpserver.Env("FLUSH_INTERVAL", "3s"))
	if err != nil || flush <= 0 {
		return fmt.Errorf("FLUSH_INTERVAL must be a positive duration")
	}
	capacity, err := strconv.Atoi(httpserver.Env("QUEUE_CAPACITY", "256"))
	if err != nil || capacity < 1 || capacity > 10000 {
		return fmt.Errorf("QUEUE_CAPACITY must be between 1 and 10000")
	}
	batchSize, err := strconv.Atoi(httpserver.Env("BATCH_SIZE", "32"))
	if err != nil || batchSize < 1 || batchSize > 100 {
		return fmt.Errorf("BATCH_SIZE must be between 1 and 100")
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	session := hex.EncodeToString(id[:])
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	queue := make(chan telemetry.Sample, capacity)
	backendURL := httpserver.Env("BACKEND_URL", "http://localhost:8080")
	appURL := httpserver.Env("APP_URL", "http://localhost:8081")
	nodeID := httpserver.Env("NODE_ID", "node-1")
	heartbeatDone := make(chan struct{})
	go func() { defer close(heartbeatDone); heartbeats(ctx, client, backendURL, nodeID, appURL) }()
	defer func() { <-heartbeatDone }()
	senderCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	done := make(chan deliveryCounts, 1)
	go func() {
		done <- deliver(senderCtx, queue, batchSize, flush, func(ctx context.Context, batch []telemetry.Sample) error {
			return send(ctx, client, backendURL, batch)
		})
	}()
	dropped := collectSamples(ctx, client, appURL, nodeID, session, interval, queue)
	close(queue)
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	var counts deliveryCounts
	select {
	case counts = <-done:
	case <-deadline.C:
		cancel()
		counts = <-done
	}
	slog.Info("agent delivery totals", "retries", counts.retries, "dropped_samples", dropped+counts.dropped)
	return nil
}

func collectSamples(ctx context.Context, client *http.Client, appURL, nodeID, session string, interval time.Duration, queue chan telemetry.Sample) int {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var sequence int64
	dropped := 0
	for ctx.Err() == nil {
		collectedAt := time.Now().UTC()
		stats, err := collect(ctx, client, appURL)
		if err == nil {
			sequence++
			sample := telemetry.Sample{NodeID: nodeID, SessionID: session, Sequence: sequence, CollectedAt: collectedAt, Stats: stats}
			if err := (telemetry.Batch{Samples: []telemetry.Sample{sample}}).Validate(); err != nil {
				dropped++
				slog.Warn("invalid sample dropped", "error", err, "collector_dropped_samples", dropped)
			} else if enqueue(queue, sample) {
				dropped++
				slog.Warn("agent queue full; oldest queued sample dropped", "collector_dropped_samples", dropped)
			}
		} else if ctx.Err() == nil {
			slog.Warn("sample collection failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return dropped
		case <-ticker.C:
		}
	}
	return dropped
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
