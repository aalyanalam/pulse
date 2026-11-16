// Package telemetry defines the wire format shared by the application, agent and API.
package telemetry

import (
	"fmt"
	"math"
	"time"
)

type Stats struct {
	AppProcessID   string  `json:"app_process_id"`
	Version        string  `json:"version"`
	Requests       int64   `json:"requests"`
	Errors         int64   `json:"errors"`
	TotalLatencyMS float64 `json:"total_latency_ms"`
	MemoryBytes    uint64  `json:"memory_bytes"`
}

// Counters are cumulative since application start, not per-interval rates.
type Sample struct {
	NodeID      string    `json:"node_id"`
	SessionID   string    `json:"session_id"`
	Sequence    int64     `json:"sequence"`
	CollectedAt time.Time `json:"collected_at"`
	Stats
}

type Batch struct {
	Samples []Sample `json:"samples"`
}

func (b Batch) Validate() error {
	if len(b.Samples) == 0 || len(b.Samples) > 100 {
		return fmt.Errorf("batch must contain 1–100 samples")
	}
	for _, s := range b.Samples {
		if s.NodeID == "" || len(s.NodeID) > 128 || s.SessionID == "" || len(s.SessionID) > 128 || s.Version == "" || len(s.Version) > 128 {
			return fmt.Errorf("node_id, session_id and version must contain 1–128 bytes")
		}
		if len(s.AppProcessID) > 128 {
			return fmt.Errorf("app_process_id exceeds 128 bytes")
		}
		if s.Sequence < 1 || s.CollectedAt.IsZero() || s.CollectedAt.After(time.Now().Add(5*time.Minute)) {
			return fmt.Errorf("invalid sequence or collection timestamp")
		}
		if s.Requests < 0 || s.Errors < 0 || s.Errors > s.Requests || s.TotalLatencyMS < 0 || math.IsNaN(s.TotalLatencyMS) || math.IsInf(s.TotalLatencyMS, 0) || s.MemoryBytes > math.MaxInt64 {
			return fmt.Errorf("invalid measurements")
		}
	}
	return nil
}
