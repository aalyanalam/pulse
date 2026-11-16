package backend

import (
	"context"
	"database/sql"
)

const regressionSchema = `
ALTER TABLE incidents ADD COLUMN version TEXT NOT NULL DEFAULT '';
CREATE INDEX samples_latest ON samples(node_id,collected_at DESC);
`

// Three adjacent, populated measurement intervals must agree. Process changes,
// counter resets, gaps, and unknown process identities invalidate the decision.
func evaluateRegressions(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `WITH recent AS (
 SELECT h.node_id,s.* FROM heartbeats h CROSS JOIN LATERAL (
 SELECT collected_at,app_process_id,version,requests,errors,total_latency_ms
 FROM samples WHERE node_id=h.node_id
 ORDER BY collected_at DESC,session_id DESC,sequence DESC LIMIT 4
 ) s WHERE h.last_seen > clock_timestamp()-interval '15 seconds'
 ), deltas AS (
 SELECT *, requests-lag(requests) OVER w AS n,
 errors-lag(errors) OVER w AS e,
 total_latency_ms-lag(total_latency_ms) OVER w AS latency,
 collected_at-lag(collected_at) OVER w AS gap,
 lag(app_process_id) OVER w AS previous_process,
 lag(version) OVER w AS previous_version
 FROM recent WINDOW w AS (PARTITION BY node_id ORDER BY collected_at)
 ), evidence AS (
 SELECT node_id, max(version) AS version,
 bool_and(e::double precision/n > 0.1) AS errors_bad,
 bool_and(e::double precision/n < 0.05) AS errors_good,
 bool_and(latency/n > 100) AS latency_bad,
 bool_and(latency/n < 50) AS latency_good
 FROM deltas WHERE n>=5 AND e>=0 AND e<=n AND latency>=0
 AND gap > interval '0 seconds' AND gap <= interval '10 seconds'
 AND app_process_id<>'' AND app_process_id=previous_process AND version=previous_version
 GROUP BY node_id HAVING count(*)=3 AND max(collected_at)>clock_timestamp()-interval '10 seconds'
 AND max(collected_at)<=clock_timestamp()
 ), decisions AS (
 SELECT node_id,version,'elevated_errors' AS kind,errors_bad AS bad,errors_good AS good FROM evidence
 UNION ALL
 SELECT node_id,version,'slow_responses',latency_bad,latency_good FROM evidence
 ), recovered AS (
 UPDATE incidents i SET resolved_at=clock_timestamp() FROM decisions d
 WHERE i.node_id=d.node_id AND i.kind=d.kind AND i.resolved_at IS NULL AND d.good
 RETURNING i.id
 )
 INSERT INTO incidents(node_id,kind,opened_at,version)
 SELECT node_id,kind,clock_timestamp(),version FROM decisions WHERE bad
 ON CONFLICT(node_id,kind) WHERE resolved_at IS NULL DO NOTHING`)
	return err
}
