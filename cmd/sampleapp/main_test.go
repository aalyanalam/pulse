package main

import (
	"encoding/json"
	"net/http/httptest"
	"pulse/internal/telemetry"
	"sync"
	"testing"
)

func TestConcurrentWorkStats(t *testing.T) {
	h := handler("test-v1")
	var workers sync.WaitGroup
	for i := 0; i < 25; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "/work", nil))
			if w.Code != 200 {
				t.Errorf("work: %d", w.Code)
			}
		}()
	}
	workers.Wait()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/stats", nil))
	var stats telemetry.Stats
	if err := json.Unmarshal(w.Body.Bytes(), &stats); err != nil {
		t.Fatal(err)
	}
	if stats.Requests != 25 || stats.Errors != 0 || stats.Version != "test-v1" || stats.TotalLatencyMS <= 0 || stats.MemoryBytes == 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}
