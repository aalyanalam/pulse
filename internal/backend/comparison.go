package backend

import (
	"context"
	"net/http"
	"net/url"
	"pulse/internal/telemetry"
	"time"
)

type comparisonWindow struct {
	Version         string    `json:"version"`
	From            time.Time `json:"from"`
	To              time.Time `json:"to"`
	Requests        int64     `json:"requests"`
	Errors          int64     `json:"errors"`
	ErrorRate       *float64  `json:"error_rate"`
	MeanLatencyMS   *float64  `json:"mean_latency_ms"`
	Intervals       int       `json:"intervals"`
	ObservedSeconds float64   `json:"observed_seconds"`
	ObservedNodes   int       `json:"observed_nodes"`
}

func parseComparisonWindow(q url.Values, prefix string) (comparisonWindow, bool) {
	w := comparisonWindow{Version: q.Get(prefix + "_version")}
	var e1, e2 error
	w.From, e1 = time.Parse(time.RFC3339Nano, q.Get(prefix+"_from"))
	w.To, e2 = time.Parse(time.RFC3339Nano, q.Get(prefix+"_to"))
	return w, e1 == nil && e2 == nil && len(w.Version) > 0 && len(w.Version) <= 128 && w.To.After(w.From) && w.To.Sub(w.From) <= 24*time.Hour
}

// Only complete adjacent intervals inside the window count. Do not infer traffic
// across restarts, unknown identities, counter resets, or long collection gaps.
func summarizeWindow(w comparisonWindow, samples []telemetry.Sample) comparisonWindow {
	var latency float64
	for i := 1; i < len(samples); i++ {
		p, s := samples[i-1], samples[i]
		gap := s.CollectedAt.Sub(p.CollectedAt)
		n, e, l := s.Requests-p.Requests, s.Errors-p.Errors, s.TotalLatencyMS-p.TotalLatencyMS
		if s.Version != w.Version || p.Version != w.Version || s.AppProcessID == "" || s.AppProcessID != p.AppProcessID || gap <= 0 || gap > 10*time.Second || n < 0 || e < 0 || e > n || l < 0 || (n == 0 && l != 0) {
			continue
		}
		w.Intervals++
		w.ObservedSeconds += gap.Seconds()
		w.Requests += n
		w.Errors += e
		latency += l
	}
	if w.Intervals > 0 {
		w.ObservedNodes = 1
	}
	if w.Requests > 0 {
		rate, mean := float64(w.Errors)/float64(w.Requests), latency/float64(w.Requests)
		w.ErrorRate = &rate
		w.MeanLatencyMS = &mean
	}
	return w
}

func (a API) compare(w http.ResponseWriter, r *http.Request) {
	baseline, bok := parseComparisonWindow(r.URL.Query(), "baseline")
	candidate, cok := parseComparisonWindow(r.URL.Query(), "candidate")
	if !bok || !cok {
		respond(w, 400, map[string]string{"error": "require baseline_version/from/to and candidate_version/from/to; RFC3339 windows must span at most 24h"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	// One query gives both windows a consistent database snapshot. Never silently
	// truncate evidence: a dense window must be narrowed by the caller.
	rows, err := a.DB.QueryContext(ctx, `SELECT collected_at,version,app_process_id,requests,errors,total_latency_ms FROM samples WHERE node_id=$1 AND ((collected_at >= $2 AND collected_at < $3) OR (collected_at >= $4 AND collected_at < $5)) ORDER BY collected_at,session_id,sequence LIMIT 20001`, r.PathValue("id"), baseline.From, baseline.To, candidate.From, candidate.To)
	if err != nil {
		failure(w, err)
		return
	}
	defer rows.Close()
	samples := make([]telemetry.Sample, 0)
	for rows.Next() {
		var s telemetry.Sample
		if err := rows.Scan(&s.CollectedAt, &s.Version, &s.AppProcessID, &s.Requests, &s.Errors, &s.TotalLatencyMS); err != nil {
			failure(w, err)
			return
		}
		samples = append(samples, s)
	}
	if err := rows.Err(); err != nil {
		failure(w, err)
		return
	}
	if len(samples) > 20000 {
		respond(w, 422, map[string]string{"error": "too many samples; narrow comparison windows"})
		return
	}
	summarize := func(window comparisonWindow) comparisonWindow {
		selected := make([]telemetry.Sample, 0)
		for _, s := range samples {
			if !s.CollectedAt.Before(window.From) && s.CollectedAt.Before(window.To) {
				selected = append(selected, s)
			}
		}
		return summarizeWindow(window, selected)
	}
	respond(w, 200, map[string]any{"node_id": r.PathValue("id"), "baseline": summarize(baseline), "candidate": summarize(candidate)})
}
