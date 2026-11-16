package backend

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"
)

const heartbeatSchema = `
CREATE TABLE heartbeats (
 node_id TEXT PRIMARY KEY CHECK(octet_length(node_id) BETWEEN 1 AND 128),
 last_seen TIMESTAMPTZ NOT NULL,
 recovery_count INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE incidents (
 id BIGSERIAL PRIMARY KEY,
 node_id TEXT NOT NULL REFERENCES heartbeats(node_id),
 kind TEXT NOT NULL DEFAULT 'agent_unreachable',
 opened_at TIMESTAMPTZ NOT NULL,
 resolved_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX one_open_incident ON incidents(node_id) WHERE resolved_at IS NULL;
`

const applicationHealthSchema = `
ALTER TABLE heartbeats ADD COLUMN app_failures INTEGER NOT NULL DEFAULT 0;
ALTER TABLE heartbeats ADD COLUMN app_successes INTEGER NOT NULL DEFAULT 0;
DROP INDEX one_open_incident;
CREATE UNIQUE INDEX one_open_incident ON incidents(node_id,kind) WHERE resolved_at IS NULL;
`

// Heartbeats are live requests only. Telemetry ingestion never updates this table.
func (a API) heartbeat(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if len(id) == 0 || len(id) > 128 {
		respond(w, 400, map[string]string{"error": "node ID must contain 1–128 bytes"})
		return
	}
	var report struct {
		Healthy *bool `json:"application_healthy"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil && err != io.EOF {
		respond(w, 400, map[string]string{"error": "invalid heartbeat"})
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		respond(w, 400, map[string]string{"error": "expected one JSON value"})
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
	_, err = tx.ExecContext(ctx, `INSERT INTO heartbeats(node_id,last_seen) VALUES($1,clock_timestamp())
 ON CONFLICT(node_id) DO UPDATE SET
 app_failures=CASE WHEN heartbeats.last_seen < clock_timestamp()-interval '15 seconds' THEN 0 ELSE heartbeats.app_failures END,
 app_successes=CASE WHEN heartbeats.last_seen < clock_timestamp()-interval '15 seconds' THEN 0 ELSE heartbeats.app_successes END,
 recovery_count=CASE WHEN heartbeats.last_seen < clock_timestamp()-interval '15 seconds' THEN 1 ELSE LEAST(heartbeats.recovery_count+1,3) END,
 last_seen=clock_timestamp()`, id)
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE incidents SET resolved_at=clock_timestamp()
 WHERE node_id=$1 AND kind='agent_unreachable' AND resolved_at IS NULL AND (SELECT recovery_count FROM heartbeats WHERE node_id=$1)>=3`, id)
	}
	if err == nil {
		err = updateApplicationHealth(ctx, tx, id, report.Healthy)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		failure(w, err)
		return
	}
	respond(w, 200, map[string]string{"status": "received"})
}

// Evaluate opens incidents within roughly two seconds of the 15-second timeout.
// Row locks serialize decisions with concurrent heartbeat recovery.
func (a API) Evaluate(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		if err := a.evaluateOnce(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("heartbeat evaluation failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a API) evaluateOnce(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `WITH stale AS (
 SELECT node_id,last_seen FROM heartbeats WHERE last_seen < clock_timestamp()-interval '15 seconds' FOR UPDATE
 ) INSERT INTO incidents(node_id,opened_at)
 SELECT node_id,last_seen+interval '15 seconds' FROM stale
 ON CONFLICT(node_id,kind) WHERE resolved_at IS NULL DO NOTHING`)
	if err != nil {
		return err
	}
	if err := evaluateRegressions(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (a API) incidents(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	rows, err := a.DB.QueryContext(ctx, `SELECT id,kind,opened_at,resolved_at,version FROM incidents WHERE node_id=$1 ORDER BY id DESC LIMIT 100`, r.PathValue("id"))
	if err != nil {
		failure(w, err)
		return
	}
	defer rows.Close()
	type incident struct {
		Version    string     `json:"version"`
		ID         int64      `json:"id"`
		Kind       string     `json:"kind"`
		OpenedAt   time.Time  `json:"opened_at"`
		ResolvedAt *time.Time `json:"resolved_at"`
	}
	result := []incident{}
	for rows.Next() {
		var item incident
		var resolved sql.NullTime
		if err := rows.Scan(&item.ID, &item.Kind, &item.OpenedAt, &resolved, &item.Version); err != nil {
			failure(w, err)
			return
		}
		if resolved.Valid {
			item.ResolvedAt = &resolved.Time
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		failure(w, err)
		return
	}
	respond(w, 200, result)
}

func updateApplicationHealth(ctx context.Context, tx *sql.Tx, id string, healthy *bool) error {
	// An old agent with no health report breaks both consecutive observation streaks.
	_, err := tx.ExecContext(ctx, `UPDATE heartbeats SET
 app_failures=CASE WHEN $2::boolean=false THEN LEAST(app_failures+1,3) ELSE 0 END,
 app_successes=CASE WHEN $2::boolean=true THEN LEAST(app_successes+1,3) ELSE 0 END
 WHERE node_id=$1`, id, healthy)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO incidents(node_id,kind,opened_at)
 SELECT node_id,'application_unavailable',clock_timestamp() FROM heartbeats
 WHERE node_id=$1 AND app_failures>=3
 ON CONFLICT(node_id,kind) WHERE resolved_at IS NULL DO NOTHING`, id)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE incidents SET resolved_at=clock_timestamp()
 WHERE node_id=$1 AND kind='application_unavailable' AND resolved_at IS NULL
 AND (SELECT app_successes FROM heartbeats WHERE node_id=$1)>=3`, id)
	return err
}
