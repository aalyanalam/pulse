package backend

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"pulse/internal/telemetry"
	"strconv"
	"time"
)

//go:embed schema.sql
var Schema string

type API struct{ DB *sql.DB }

func respond(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
func failure(w http.ResponseWriter, err error) {
	slog.Error("database request failed", "error", err)
	respond(w, 503, map[string]string{"error": "storage unavailable"})
}

func (a API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/fleet", a.fleet)
	mux.HandleFunc("GET /api/v1/fleet/summary", a.fleetSummary)
	mux.HandleFunc("POST /api/v1/nodes/{id}/heartbeat", a.heartbeat)
	mux.HandleFunc("GET /api/v1/nodes/{id}/incidents", a.incidents)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := a.DB.PingContext(ctx); err != nil {
			failure(w, err)
			return
		}
		respond(w, 200, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("POST /api/v1/telemetry/batches", a.ingest)
	mux.HandleFunc("GET /api/v1/nodes", a.nodes)
	mux.HandleFunc("GET /api/v1/nodes/{id}/metrics", a.metrics)
	mux.HandleFunc("GET /api/v1/nodes/{id}/compare", a.compare)
	return mux
}

func (a API) ingest(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	var batch telemetry.Batch
	if err := d.Decode(&batch); err != nil {
		respond(w, 400, map[string]string{"error": "invalid JSON batch"})
		return
	}
	if err := d.Decode(new(any)); err != io.EOF {
		respond(w, 400, map[string]string{"error": "expected one JSON value"})
		return
	}
	if err := batch.Validate(); err != nil {
		respond(w, 400, map[string]string{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	timer := ingestTimer{started: started}
	phase := time.Now()
	tx, err := a.DB.BeginTx(ctx, nil)
	timer.begin = time.Since(phase)
	if err != nil {
		failure(w, err)
		return
	}
	defer tx.Rollback()
	exec := func(query string, args ...any) (sql.Result, error) {
		phase := time.Now()
		result, err := tx.ExecContext(ctx, query, args...)
		timer.statement += time.Since(phase)
		timer.statements++
		return result, err
	}
	samples := firstOccurrences(batch.Samples)
	// Create each distinct node once, from its first sample in batch order.
	created := make(map[string]bool)
	for _, s := range samples {
		if created[s.NodeID] {
			continue
		}
		created[s.NodeID] = true
		if _, err := exec(`INSERT INTO nodes(id,version,latest_collected_at) VALUES($1,$2,$3) ON CONFLICT(id) DO NOTHING`, s.NodeID, s.Version, s.CollectedAt); err != nil {
			failure(w, err)
			return
		}
	}
	// One set-based statement instead of one round trip per sample. RETURNING
	// lists only rows that were new, so retried identities are excluded.
	phase = time.Now()
	insertedIDs, err := insertSamples(ctx, tx, samples)
	timer.statement += time.Since(phase)
	timer.statements++
	if err != nil {
		failure(w, err)
		return
	}
	for _, s := range latestInserted(samples, insertedIDs) {
		// Delayed samples must not overwrite metadata from newer observations.
		if _, err := exec(`UPDATE nodes SET version=$2,latest_collected_at=$3 WHERE id=$1 AND latest_collected_at < $3`, s.NodeID, s.Version, s.CollectedAt); err != nil {
			failure(w, err)
			return
		}
	}
	inserted := int64(len(insertedIDs))
	phase = time.Now()
	if err := tx.Commit(); err != nil {
		failure(w, err)
		return
	}
	timer.commit = time.Since(phase)
	respond(w, 202, map[string]int64{"inserted": inserted})
	timer.finish(len(batch.Samples))
}

func (a API) nodes(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	rows, err := a.DB.QueryContext(ctx, `SELECT id,version,latest_collected_at FROM nodes WHERE id > $1 ORDER BY id LIMIT 100`, r.URL.Query().Get("after"))
	if err != nil {
		failure(w, err)
		return
	}
	defer rows.Close()
	type node struct {
		ID                string    `json:"id"`
		Version           string    `json:"version"`
		LatestCollectedAt time.Time `json:"latest_collected_at"`
	}
	nodes := []node{}
	for rows.Next() {
		var n node
		if err := rows.Scan(&n.ID, &n.Version, &n.LatestCollectedAt); err != nil {
			failure(w, err)
			return
		}
		nodes = append(nodes, n)
	}
	if err := rows.Err(); err != nil {
		failure(w, err)
		return
	}
	respond(w, 200, nodes)
}

func (a API) metrics(w http.ResponseWriter, r *http.Request) {
	from, e1 := time.Parse(time.RFC3339Nano, r.URL.Query().Get("from"))
	to, e2 := time.Parse(time.RFC3339Nano, r.URL.Query().Get("to"))
	limit := 100
	var e3 error
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, e3 = strconv.Atoi(raw)
	}
	if e1 != nil || e2 != nil || e3 != nil || !to.After(from) || to.Sub(from) > 24*time.Hour || limit < 1 || limit > 1000 {
		respond(w, 400, map[string]string{"error": "require RFC3339 from/to spanning at most 24h and limit 1–1000"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	rows, err := a.DB.QueryContext(ctx, `SELECT node_id,session_id,sequence,collected_at,received_at,version,requests,errors,total_latency_ms,memory_bytes,app_process_id FROM samples WHERE node_id=$1 AND collected_at >= $2 AND collected_at < $3 ORDER BY collected_at,session_id,sequence LIMIT $4`, r.PathValue("id"), from, to, limit)
	if err != nil {
		failure(w, err)
		return
	}
	defer rows.Close()
	type stored struct {
		telemetry.Sample
		ReceivedAt time.Time `json:"received_at"`
	}
	samples := []stored{}
	for rows.Next() {
		var s stored
		if err := rows.Scan(&s.NodeID, &s.SessionID, &s.Sequence, &s.CollectedAt, &s.ReceivedAt, &s.Version, &s.Requests, &s.Errors, &s.TotalLatencyMS, &s.MemoryBytes, &s.AppProcessID); err != nil {
			failure(w, err)
			return
		}
		samples = append(samples, s)
	}
	if err := rows.Err(); err != nil {
		failure(w, err)
		return
	}
	respond(w, 200, samples)
}
