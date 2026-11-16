package backend

import (
	"context"
	"database/sql"
	"net/http"
	"time"
)

// fleetStatus defines each node's current status once, for both the fleet
// list and the fleet-wide summary. Status comes from heartbeats and open
// incidents, never from historical telemetry.
const fleetStatus = `WITH ids AS (SELECT id FROM nodes UNION SELECT node_id FROM heartbeats),
 fleet AS (SELECT ids.id, h.last_seen,
 CASE WHEN h.last_seen IS NULL THEN 'unknown' WHEN h.last_seen < clock_timestamp()-interval '15 seconds' THEN 'unreachable'
 WHEN EXISTS(SELECT 1 FROM incidents i WHERE i.node_id=ids.id AND i.resolved_at IS NULL) THEN 'incident'
 WHEN h.app_successes=0 THEN 'unknown' ELSE 'reporting' END AS status
 FROM ids LEFT JOIN heartbeats h ON h.node_id=ids.id)
`

// fleet lists up to 100 nodes after the `after` cursor. `q` keeps nodes whose
// ID contains the text (case-insensitive); `attention=1` keeps nodes with an
// open incident or no recent heartbeat.
func (a API) fleet(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	search := query.Get("q")
	if len(search) > 128 {
		respond(w, 400, map[string]string{"error": "q must contain at most 128 bytes"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	rows, err := a.DB.QueryContext(ctx, fleetStatus+`
 SELECT f.id,coalesce(n.version,''),n.latest_collected_at,f.last_seen,f.status,s.memory_bytes
 FROM fleet f LEFT JOIN nodes n ON n.id=f.id
 LEFT JOIN LATERAL(SELECT memory_bytes FROM samples WHERE node_id=f.id ORDER BY collected_at DESC LIMIT 1)s ON true
 WHERE f.id>$1 AND ($2='' OR strpos(lower(f.id),lower($2))>0)
 AND (NOT $3 OR f.status IN ('incident','unreachable'))
 ORDER BY f.id LIMIT 100`, query.Get("after"), search, query.Get("attention") == "1")
	if err != nil {
		failure(w, err)
		return
	}
	defer rows.Close()
	result := []map[string]any{}
	for rows.Next() {
		var id, version, status string
		var collected, seen sql.NullTime
		var memory sql.NullInt64
		if err := rows.Scan(&id, &version, &collected, &seen, &status, &memory); err != nil {
			failure(w, err)
			return
		}
		var c, h, m any
		if collected.Valid {
			c = collected.Time
		}
		if seen.Valid {
			h = seen.Time
		}
		if memory.Valid {
			m = memory.Int64
		}
		result = append(result, map[string]any{"id": id, "version": version, "status": status, "latest_collected_at": c, "last_seen": h, "memory_bytes": m})
	}
	if err := rows.Err(); err != nil {
		failure(w, err)
		return
	}
	respond(w, 200, result)
}

// fleetSummary counts every node by status, so overview totals describe the
// whole fleet rather than one page of it.
func (a API) fleetSummary(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	rows, err := a.DB.QueryContext(ctx, fleetStatus+`SELECT status,count(*) FROM fleet GROUP BY status`)
	if err != nil {
		failure(w, err)
		return
	}
	defer rows.Close()
	counts := map[string]int64{"reporting": 0, "incident": 0, "unreachable": 0, "unknown": 0}
	var total int64
	for rows.Next() {
		var status string
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			failure(w, err)
			return
		}
		counts[status] = count
		total += count
	}
	if err := rows.Err(); err != nil {
		failure(w, err)
		return
	}
	respond(w, 200, map[string]any{"total": total, "by_status": counts})
}
