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
	return mux
}

func (a API) ingest(w http.ResponseWriter, r *http.Request) {
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
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		failure(w, err)
		return
	}
	defer tx.Rollback()
	var inserted int64
	for _, s := range batch.Samples {
		_, err = tx.ExecContext(ctx, `INSERT INTO nodes(id,version,latest_collected_at) VALUES($1,$2,$3) ON CONFLICT(id) DO NOTHING`, s.NodeID, s.Version, s.CollectedAt)
		if err != nil {
			failure(w, err)
			return
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO samples(node_id,session_id,sequence,collected_at,version,requests,errors,total_latency_ms,memory_bytes) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(node_id,session_id,sequence) DO NOTHING`, s.NodeID, s.SessionID, s.Sequence, s.CollectedAt, s.Version, s.Requests, s.Errors, s.TotalLatencyMS, int64(s.MemoryBytes))
		if err != nil {
			failure(w, err)
			return
		}
		count, err := result.RowsAffected()
		if err != nil {
			failure(w, err)
			return
		}
		inserted += count
		if count > 0 {
			_, err = tx.ExecContext(ctx, `UPDATE nodes SET version=$2,latest_collected_at=$3 WHERE id=$1 AND latest_collected_at < $3`, s.NodeID, s.Version, s.CollectedAt)
			if err != nil {
				failure(w, err)
				return
			}
		}
	}
	if err := tx.Commit(); err != nil {
		failure(w, err)
		return
	}
	respond(w, 202, map[string]int64{"inserted": inserted})
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
	rows, err := a.DB.QueryContext(ctx, `SELECT node_id,session_id,sequence,collected_at,received_at,version,requests,errors,total_latency_ms,memory_bytes FROM samples WHERE node_id=$1 AND collected_at >= $2 AND collected_at < $3 ORDER BY collected_at,session_id,sequence LIMIT $4`, r.PathValue("id"), from, to, limit)
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
		if err := rows.Scan(&s.NodeID, &s.SessionID, &s.Sequence, &s.CollectedAt, &s.ReceivedAt, &s.Version, &s.Requests, &s.Errors, &s.TotalLatencyMS, &s.MemoryBytes); err != nil {
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
