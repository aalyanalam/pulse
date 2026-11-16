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
//
// Heartbeats cannot arrive while this backend or its database is unavailable,
// so staleness is measured from the later of the last heartbeat and the
// database time at which evaluation (re)started. An outage therefore never
// counts against agents; a genuinely silent agent is reported one timeout
// after evaluation resumes.
func (a API) Evaluate(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	var since time.Time
	for {
		if ctx.Err() != nil {
			return
		}
		var err error
		if since.IsZero() {
			err = a.DB.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&since)
		}
		if err == nil {
			err = a.evaluateOnce(ctx, since)
		}
		if err != nil && ctx.Err() == nil {
			since = time.Time{} // Measure staleness from recovery, not from before the failure.
			slog.Warn("heartbeat evaluation failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a API) evaluateOnce(parent context.Context, since time.Time) error {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `WITH stale AS (
 SELECT node_id,last_seen,greatest(last_seen,$1::timestamptz) AS observed_from FROM heartbeats
 WHERE greatest(last_seen,$1::timestamptz) < clock_timestamp()-interval '15 seconds' FOR UPDATE
 ) INSERT INTO incidents(node_id,opened_at,evidence)
 SELECT node_id,observed_from+interval '15 seconds',
 jsonb_build_object('rule','no heartbeat for 15 seconds','last_heartbeat_at',last_seen,'timeout_seconds',15,
 'evaluation_started_at',$1::timestamptz) FROM stale
 ON CONFLICT(node_id,kind) WHERE resolved_at IS NULL DO NOTHING`, since)
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
	rows, err := a.DB.QueryContext(ctx, `SELECT id,kind,opened_at,resolved_at,version,evidence FROM incidents WHERE node_id=$1 ORDER BY id DESC LIMIT 100`, r.PathValue("id"))
	if err != nil {
		failure(w, err)
		return
	}
	defer rows.Close()
	type incident struct {
		Version    string          `json:"version"`
		ID         int64           `json:"id"`
		Kind       string          `json:"kind"`
		OpenedAt   time.Time       `json:"opened_at"`
		ResolvedAt *time.Time      `json:"resolved_at"`
		Evidence   json.RawMessage `json:"evidence"`
	}
	result := []incident{}
	for rows.Next() {
		var item incident
		var resolved sql.NullTime
		// Scanning into []byte copies the driver's buffer; json.RawMessage would not.
		var evidence []byte
		if err := rows.Scan(&item.ID, &item.Kind, &item.OpenedAt, &resolved, &item.Version, &evidence); err != nil {
			failure(w, err)
			return
		}
		item.Evidence = evidence
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
	_, err = tx.ExecContext(ctx, `INSERT INTO incidents(node_id,kind,opened_at,evidence)
 SELECT node_id,'application_unavailable',clock_timestamp(),
 jsonb_build_object('rule','3 consecutive failed application health checks reported by the agent',
 'consecutive_failures',app_failures,'last_heartbeat_at',last_seen) FROM heartbeats
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
