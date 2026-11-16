package backend

import (
	"net/http/httptest"
	"pulse/internal/telemetry"
	"testing"
	"time"
)

func TestComparisonWeightedAndRestartSafe(t *testing.T) {
	start := time.Now()
	sample := func(sec int, process, version string, n, e int64, l float64) telemetry.Sample {
		return telemetry.Sample{CollectedAt: start.Add(time.Duration(sec) * time.Second), Stats: telemetry.Stats{AppProcessID: process, Version: version, Requests: n, Errors: e, TotalLatencyMS: l}}
	}
	samples := []telemetry.Sample{sample(0, "a", "v1", 0, 0, 0), sample(3, "a", "v1", 10, 1, 100), sample(6, "a", "v1", 40, 7, 1000), sample(9, "b", "v1", 100, 0, 10000), sample(12, "b", "v1", 102, 0, 10020)}
	w := summarizeWindow(comparisonWindow{Version: "v1"}, samples)
	if w.Requests != 42 || w.Errors != 7 || w.Intervals != 3 || *w.MeanLatencyMS != 1020.0/42 || *w.ErrorRate != 7.0/42 {
		t.Fatalf("unexpected summary: %+v", w)
	}
	for _, pair := range [][]telemetry.Sample{
		{sample(0, "", "v1", 0, 0, 0), sample(3, "", "v1", 10, 1, 100)},
		{sample(0, "a", "v1", 10, 1, 100), sample(3, "a", "v1", 0, 0, 0)},
		{sample(0, "a", "v1", 0, 0, 0), sample(11, "a", "v1", 10, 1, 100)},
		{sample(0, "a", "v2", 0, 0, 0), sample(3, "a", "v1", 10, 1, 100)},
		{sample(0, "a", "v1", 0, 0, 0), sample(0, "a", "v1", 10, 1, 100)},
	} {
		w := summarizeWindow(comparisonWindow{Version: "v1"}, pair)
		if w.Intervals != 0 || w.ErrorRate != nil || w.MeanLatencyMS != nil {
			t.Fatalf("invalid interval accepted: %+v", w)
		}
	}
	w = summarizeWindow(comparisonWindow{Version: "v1"}, []telemetry.Sample{sample(0, "a", "v1", 0, 0, 0), sample(3, "a", "v1", 0, 0, 0)})
	if w.Intervals != 1 || w.ErrorRate != nil {
		t.Fatal("idle interval must have unknown rate")
	}
}

func TestComparisonRejectsUnboundedWindows(t *testing.T) {
	for _, query := range []string{"", "?baseline_version=v1&baseline_from=2026-01-01T00:00:00Z&baseline_to=2026-01-03T00:00:00Z"} {
		w := httptest.NewRecorder()
		(API{}).Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/nodes/n/compare"+query, nil))
		if w.Code != 400 {
			t.Fatalf("status %d", w.Code)
		}
	}
}
