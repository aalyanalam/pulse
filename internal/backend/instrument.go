package backend

import (
	"sync/atomic"
	"time"
)

// ingestCounters accumulates cumulative ingestion timings for successful
// batches. Benchmarks read two snapshots and divide the deltas by the request
// count to attribute latency to transaction start (which includes waiting for
// a pooled connection), individual SQL statements and the commit.
type ingestCounters struct {
	requests, samples, statements                       atomic.Int64
	beginNanos, statementNanos, commitNanos, totalNanos atomic.Int64
}

var ingestStats ingestCounters

// IngestStats is a cumulative snapshot since process start.
type IngestStats struct {
	Requests         int64   `json:"requests"`
	Samples          int64   `json:"samples"`
	Statements       int64   `json:"statements"`
	BeginSeconds     float64 `json:"begin_seconds"`
	StatementSeconds float64 `json:"statement_seconds"`
	CommitSeconds    float64 `json:"commit_seconds"`
	HandlerSeconds   float64 `json:"handler_seconds"`
}

// ReadIngestStats returns the current cumulative ingestion counters.
func ReadIngestStats() any {
	seconds := func(v *atomic.Int64) float64 { return time.Duration(v.Load()).Seconds() }
	return IngestStats{
		Requests: ingestStats.requests.Load(), Samples: ingestStats.samples.Load(), Statements: ingestStats.statements.Load(),
		BeginSeconds: seconds(&ingestStats.beginNanos), StatementSeconds: seconds(&ingestStats.statementNanos),
		CommitSeconds: seconds(&ingestStats.commitNanos), HandlerSeconds: seconds(&ingestStats.totalNanos),
	}
}

// ingestTimer records the phases of one ingestion request.
type ingestTimer struct {
	started                  time.Time
	begin, statement, commit time.Duration
	statements               int64
}

func (t *ingestTimer) finish(samples int) {
	ingestStats.requests.Add(1)
	ingestStats.samples.Add(int64(samples))
	ingestStats.statements.Add(t.statements)
	ingestStats.beginNanos.Add(int64(t.begin))
	ingestStats.statementNanos.Add(int64(t.statement))
	ingestStats.commitNanos.Add(int64(t.commit))
	ingestStats.totalNanos.Add(int64(time.Since(t.started)))
}
