package backend

import (
	"context"
	"database/sql"
	"net/http"
	"time"
)

func (a API) fleet(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	rows, err := a.DB.QueryContext(ctx, `WITH ids AS (SELECT id FROM nodes UNION SELECT node_id FROM heartbeats)
 SELECT ids.id,coalesce(n.version,''),n.latest_collected_at,h.last_seen,
 CASE WHEN h.last_seen IS NULL THEN 'unknown' WHEN h.last_seen < clock_timestamp()-interval '15 seconds' THEN 'unreachable'
 WHEN EXISTS(SELECT 1 FROM incidents i WHERE i.node_id=ids.id AND i.resolved_at IS NULL) THEN 'incident'
 WHEN h.app_successes=0 THEN 'unknown' ELSE 'reporting' END,
 s.memory_bytes FROM ids LEFT JOIN nodes n ON n.id=ids.id LEFT JOIN heartbeats h ON h.node_id=ids.id
 LEFT JOIN LATERAL(SELECT memory_bytes FROM samples WHERE node_id=ids.id ORDER BY collected_at DESC LIMIT 1)s ON true
 WHERE ids.id>$1 ORDER BY ids.id LIMIT 100`, r.URL.Query().Get("after"))
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
