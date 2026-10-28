package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"pulse/internal/telemetry"
	"testing"
	"time"
)

func TestCollectAndSend(t *testing.T) {
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(telemetry.Stats{Version: "v1", Requests: 7})
	}))
	defer app.Close()
	stats, err := collect(context.Background(), app.Client(), app.URL)
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b telemetry.Batch
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			t.Error(err)
		}
		if err := b.Validate(); err != nil {
			t.Error(err)
		}
		if len(b.Samples) != 1 || b.Samples[0].Requests != 7 {
			t.Error("measurement lost")
		}
		w.WriteHeader(202)
	}))
	defer api.Close()
	err = send(context.Background(), api.Client(), api.URL, telemetry.Sample{NodeID: "n", SessionID: "s", Sequence: 1, CollectedAt: time.Now(), Stats: stats})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCollectFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer server.Close()
	if _, err := collect(context.Background(), server.Client(), server.URL); err == nil {
		t.Fatal("accepted upstream error")
	}
}
