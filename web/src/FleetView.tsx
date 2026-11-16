import React, { useEffect, useState } from "react";
import { FleetNode, FleetSummary, ago, dateTime, get, poll } from "./api";
import { href } from "./route";
import { Icon, Notice, Segmented, StatusBadge, Tile } from "./ui";

export function FleetView() {
  const [summary, setSummary] = useState<FleetSummary | null>(null);
  const [nodes, setNodes] = useState<FleetNode[] | null>(null);
  const [error, setError] = useState("");
  const [search, setSearch] = useState("");
  const [query, setQuery] = useState("");
  const [attention, setAttention] = useState(false);
  const [cursors, setCursors] = useState<string[]>([""]); // Page start cursors.
  const [, tick] = useState(0);
  const after = cursors[cursors.length - 1];

  useEffect(() => {
    const timer = setTimeout(() => setQuery(search.trim()), 250);
    return () => clearTimeout(timer);
  }, [search]);
  useEffect(() => setCursors([""]), [query, attention]);
  useEffect(
    () =>
      poll(async (signal) => {
        const params = new URLSearchParams({ after, q: query });
        if (attention) params.set("attention", "1");
        try {
          const [s, n] = await Promise.all([
            get<FleetSummary>("fleet/summary", signal),
            get<FleetNode[]>("fleet?" + params, signal),
          ]);
          setSummary(s);
          setNodes(n);
          setError("");
        } catch (e) {
          if (!signal.aborted) setError(String(e));
        }
        tick((t) => t + 1); // Refresh relative times.
      }),
    [after, query, attention],
  );
  useEffect(() => setNodes(null), [after, query, attention]);

  const attentionCount = summary ? summary.by_status.incident + summary.by_status.unreachable : 0;
  const open = (id: string) => (location.hash = href("node", id));
  const first = (cursors.length - 1) * 100 + 1;

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Fleet</h1>
          <p className="subtle">Current status of every monitored node, from live heartbeats.</p>
        </div>
      </div>

      <div className="tiles">
        <Tile
          label="Needs attention"
          value={summary ? attentionCount : "–"}
          detail={attentionCount ? "Open incident or no heartbeat" : "No open incidents"}
          tone={attentionCount ? "critical" : "good"}
          onClick={() => setAttention(!attention)}
          pressed={attention}
        />
        <Tile label="Reporting" value={summary?.by_status.reporting ?? "–"} detail="Heartbeat and healthy probe" />
        <Tile label="Without heartbeat" value={summary?.by_status.unknown ?? "–"} detail="Telemetry-only or new nodes" />
        <Tile label="Total nodes" value={summary?.total ?? "–"} />
      </div>

      {error && <Notice>Can’t reach the backend. Showing the last data received; retrying every 5 s.</Notice>}

      <section className="card">
        <div className="filters">
          <label className="search">
            <Icon name="search" />
            <input
              type="search"
              placeholder="Search nodes by ID"
              aria-label="Search nodes by ID"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
            />
          </label>
          <Segmented
            label="Status filter"
            value={attention ? "attention" : "all"}
            onChange={(v) => setAttention(v === "attention")}
            options={[
              { value: "all", label: "All nodes" },
              { value: "attention", label: "Needs attention" },
            ]}
          />
        </div>

        {nodes === null ? (
          <p className="empty">Loading nodes…</p>
        ) : nodes.length === 0 ? (
          <p className="empty">
            {query || attention
              ? "No nodes match these filters."
              : "No nodes yet. Start the sample application and agent (docker compose up) to see node-1 here."}
          </p>
        ) : (
          <div className="table-scroll">
            <table className="fleet">
              <thead>
                <tr>
                  <th>Node</th>
                  <th>Status</th>
                  <th>Version</th>
                  <th>Last heartbeat</th>
                  <th>Latest telemetry</th>
                  <th className="num">Go heap</th>
                </tr>
              </thead>
              <tbody>
                {nodes.map((n) => {
                  const stale = n.latest_collected_at && Date.now() - Date.parse(n.latest_collected_at) > 15000;
                  return (
                    <tr key={n.id} className="row-link" onClick={() => open(n.id)}>
                      <td>
                        <a href={href("node", n.id)} onClick={(e) => e.stopPropagation()}>
                          {n.id}
                        </a>
                      </td>
                      <td>
                        <StatusBadge status={n.status} />
                      </td>
                      <td>{n.version || "—"}</td>
                      <td title={n.last_seen ? dateTime(n.last_seen) : undefined}>{ago(n.last_seen)}</td>
                      <td title={n.latest_collected_at ? dateTime(n.latest_collected_at) : undefined}>
                        {ago(n.latest_collected_at)}
                        {stale && <span className="subtle"> · stale</span>}
                      </td>
                      <td className="num">
                        {n.memory_bytes === null ? "—" : (n.memory_bytes / 1048576).toFixed(1) + " MB"}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}

        <div className="pager">
          <span className="subtle">
            {nodes?.length ? `Showing ${first}–${first + nodes.length - 1}` : ""}
            {summary && !query && !attention ? ` of ${summary.total}` : ""}
          </span>
          <div>
            <button type="button" disabled={cursors.length < 2} onClick={() => setCursors(cursors.slice(0, -1))}>
              Previous
            </button>
            <button
              type="button"
              disabled={!nodes || nodes.length < 100}
              onClick={() => nodes && setCursors([...cursors, nodes[nodes.length - 1].id])}
            >
              Next
            </button>
          </div>
        </div>
      </section>
      <p className="footnote">
        Status comes from heartbeats, never from telemetry, so delayed data can’t make a silent node look healthy.
        Go heap is the application’s allocated heap, not host memory.
      </p>
    </>
  );
}
