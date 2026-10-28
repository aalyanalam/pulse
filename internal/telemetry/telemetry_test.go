package telemetry

import (
	"testing"
	"time"
)

func TestValidate(t *testing.T) {
	s := Sample{NodeID: "node-1", SessionID: "session", Sequence: 1, CollectedAt: time.Now(), Stats: Stats{Version: "v1", Requests: 2, Errors: 1}}
	if err := (Batch{Samples: []Sample{s}}).Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Sample){func(s *Sample) { s.NodeID = "" }, func(s *Sample) { s.Sequence = 0 }, func(s *Sample) { s.Errors = 3 }, func(s *Sample) { s.TotalLatencyMS = -1 }, func(s *Sample) { s.CollectedAt = time.Now().Add(time.Hour) }} {
		bad := s
		mutate(&bad)
		if (Batch{Samples: []Sample{bad}}).Validate() == nil {
			t.Fatalf("accepted invalid sample: %+v", bad)
		}
	}
	if (Batch{}).Validate() == nil {
		t.Fatal("accepted empty batch")
	}
}
