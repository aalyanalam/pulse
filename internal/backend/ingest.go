package backend

import (
	"context"
	"database/sql"
	"time"

	"github.com/lib/pq"
	"pulse/internal/telemetry"
)

type sampleID struct {
	node, session string
	sequence      int64
}

func identity(s telemetry.Sample) sampleID { return sampleID{s.NodeID, s.SessionID, s.Sequence} }

// firstOccurrences drops repeated identities within a batch, keeping the first
// occurrence, which is the sample a sequential insert would have stored.
func firstOccurrences(samples []telemetry.Sample) []telemetry.Sample {
	seen := make(map[sampleID]bool, len(samples))
	unique := make([]telemetry.Sample, 0, len(samples))
	for _, s := range samples {
		if id := identity(s); !seen[id] {
			seen[id] = true
			unique = append(unique, s)
		}
	}
	return unique
}

// insertSamples stores samples in a single statement and returns the
// identities that were new. The samples must not repeat an identity.
func insertSamples(ctx context.Context, tx *sql.Tx, samples []telemetry.Sample) (map[sampleID]bool, error) {
	n := len(samples)
	nodes, sessions, versions, processes, collected := make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n)
	sequences, requests, errs, memory := make([]int64, n), make([]int64, n), make([]int64, n), make([]int64, n)
	latency := make([]float64, n)
	for i, s := range samples {
		nodes[i], sessions[i], versions[i], processes[i] = s.NodeID, s.SessionID, s.Version, s.AppProcessID
		collected[i] = s.CollectedAt.Format(time.RFC3339Nano)
		sequences[i], requests[i], errs[i], memory[i] = s.Sequence, s.Requests, s.Errors, int64(s.MemoryBytes)
		latency[i] = s.TotalLatencyMS
	}
	rows, err := tx.QueryContext(ctx, `INSERT INTO samples(node_id,session_id,sequence,collected_at,version,requests,errors,total_latency_ms,memory_bytes,app_process_id)
SELECT * FROM unnest($1::text[],$2::text[],$3::bigint[],$4::timestamptz[],$5::text[],$6::bigint[],$7::bigint[],$8::float8[],$9::bigint[],$10::text[])
ON CONFLICT(node_id,session_id,sequence) DO NOTHING
RETURNING node_id,session_id,sequence`,
		pq.Array(nodes), pq.Array(sessions), pq.Array(sequences), pq.Array(collected), pq.Array(versions),
		pq.Array(requests), pq.Array(errs), pq.Array(latency), pq.Array(memory), pq.Array(processes))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	inserted := make(map[sampleID]bool, n)
	for rows.Next() {
		var id sampleID
		if err := rows.Scan(&id.node, &id.session, &id.sequence); err != nil {
			return nil, err
		}
		inserted[id] = true
	}
	return inserted, rows.Err()
}

// dbPrecision approximates PostgreSQL's microsecond timestamp precision so ties are
// decided as the database's latest_collected_at comparison would decide them.
func dbPrecision(t time.Time) time.Time { return t.Round(time.Microsecond) }

// latestInserted returns, per node, the newly inserted sample with the latest
// collection time; on equal times the earliest in batch order wins. Applying
// only this sample's metadata gives the same result as updating sequentially.
func latestInserted(samples []telemetry.Sample, inserted map[sampleID]bool) []telemetry.Sample {
	index := make(map[string]int)
	var latest []telemetry.Sample
	for _, s := range samples {
		if !inserted[identity(s)] {
			continue
		}
		if i, ok := index[s.NodeID]; !ok {
			index[s.NodeID] = len(latest)
			latest = append(latest, s)
		} else if dbPrecision(s.CollectedAt).After(dbPrecision(latest[i].CollectedAt)) {
			latest[i] = s
		}
	}
	return latest
}
