package backend

import (
	"testing"
	"time"

	"pulse/internal/telemetry"
)

func sampleAt(node string, sequence int64, offset time.Duration, version string) telemetry.Sample {
	base := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	return telemetry.Sample{NodeID: node, SessionID: "s", Sequence: sequence, CollectedAt: base.Add(offset), Stats: telemetry.Stats{Version: version}}
}

func TestFirstOccurrencesKeepsBatchOrder(t *testing.T) {
	got := firstOccurrences([]telemetry.Sample{sampleAt("a", 1, 0, "first"), sampleAt("a", 2, 0, "x"), sampleAt("a", 1, time.Second, "retry"), sampleAt("b", 1, 0, "other")})
	if len(got) != 3 || got[0].Version != "first" || got[1].Sequence != 2 || got[2].NodeID != "b" {
		t.Fatalf("unexpected samples: %+v", got)
	}
}

func TestLatestInsertedMatchesSequentialUpdates(t *testing.T) {
	samples := []telemetry.Sample{
		sampleAt("a", 1, 2*time.Second, "a-later"),
		sampleAt("a", 2, time.Second, "a-earlier"),
		sampleAt("b", 1, time.Second, "b-first"),
		sampleAt("b", 2, time.Second+300*time.Nanosecond, "b-same-microsecond"),
		sampleAt("a", 3, 5*time.Second, "a-duplicate"),
		sampleAt("c", 1, 0, "c-duplicate"),
	}
	inserted := map[sampleID]bool{identity(samples[0]): true, identity(samples[1]): true, identity(samples[2]): true, identity(samples[3]): true}
	got := latestInserted(samples, inserted)
	if len(got) != 2 || got[0].Version != "a-later" || got[1].Version != "b-first" {
		t.Fatalf("unexpected metadata updates: %+v", got)
	}
}
