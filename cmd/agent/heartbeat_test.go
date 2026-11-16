package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestHeartbeatDoesNotDependOnApplication(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	client := &http.Client{Transport: handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(503)
			return
		}
		calls++
		var report struct {
			Healthy *bool `json:"application_healthy"`
		}
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
			t.Error(err)
		}
		if report.Healthy == nil || *report.Healthy {
			t.Error("failed application was not reported")
		}
		if r.Method != "POST" || r.URL.Path != "/api/v1/nodes/node-1/heartbeat" {
			t.Errorf("unexpected heartbeat %s %s", r.Method, r.URL.Path)
		}
		cancel()
		w.WriteHeader(200)
	})}}
	done := make(chan struct{})
	go func() { defer close(done); heartbeats(ctx, client, "http://backend", "node-1", "http://app") }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("heartbeat did not exit")
	}
	if calls != 1 {
		t.Fatalf("heartbeat calls = %d", calls)
	}
}

func TestApplicationHealthStatus(t *testing.T) {
	for _, status := range []int{200, 503, 404} {
		client := &http.Client{Transport: handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) })}}
		if got := applicationHealthy(context.Background(), client, "http://app"); got != (status == 200) {
			t.Errorf("status %d: healthy=%t", status, got)
		}
	}
}
