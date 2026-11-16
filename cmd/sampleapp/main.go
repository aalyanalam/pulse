package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"pulse/internal/httpserver"
	"pulse/internal/telemetry"
	"runtime"
	"sync"
	"syscall"
	"time"
)

func handler(version string) http.Handler {
	var mu sync.Mutex
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		panic(err)
	}
	var started int64
	stats := telemetry.Stats{Version: version, AppProcessID: fmt.Sprintf("%x", id)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /work", func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		mu.Lock()
		started++
		fail := version == "v2-errors" && started%3 == 0
		mu.Unlock()
		if version == "v2-slow" {
			timer := time.NewTimer(200 * time.Millisecond)
			select {
			case <-timer.C:
			case <-r.Context().Done():
				timer.Stop()
				return
			}
		}
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
		}
		value := []byte("pulse")
		for i := 0; i < 1000; i++ {
			sum := sha256.Sum256(value)
			value = sum[:]
		}
		_, err := fmt.Fprintf(w, "%x\n", value)
		mu.Lock()
		stats.Requests++
		if err != nil || fail {
			stats.Errors++
		}
		stats.TotalLatencyMS += float64(time.Since(start).Nanoseconds()) / 1e6
		mu.Unlock()
	})
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, r *http.Request) {
		var memory runtime.MemStats
		runtime.ReadMemStats(&memory)
		mu.Lock()
		snapshot := stats
		mu.Unlock()
		snapshot.MemoryBytes = memory.Alloc
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snapshot)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	return mux
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := httpserver.Run(ctx, httpserver.Env("LISTEN_ADDR", ":8081"), handler(httpserver.Env("APP_VERSION", "v1"))); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
