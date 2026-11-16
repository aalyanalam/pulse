import React, { useEffect, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import "./style.css";
type Node = {
  id: string;
  version: string;
  status: string;
  last_seen: string | null;
  latest_collected_at: string | null;
  memory_bytes: number | null;
};
type Sample = {
  collected_at: string;
  version: string;
  app_process_id: string;
  requests: number;
  errors: number;
  total_latency_ms: number;
};
type EvidenceInterval = {
  ended_at: string;
  requests: number;
  errors: number;
  error_rate: number;
  mean_latency_ms: number;
};
type Evidence = {
  rule?: string;
  intervals?: EvidenceInterval[];
  last_heartbeat_at?: string;
  consecutive_failures?: number;
};
type Incident = {
  id: number;
  kind: string;
  version: string;
  opened_at: string;
  resolved_at: string | null;
  evidence?: Evidence;
};

// One readable line describing the observations that opened an incident.
function evidenceText(i: Incident): string {
  const e = i.evidence;
  if (!e?.rule) return "Evidence not recorded (incident predates evidence capture)";
  if (e.intervals?.length) {
    const values = e.intervals.map((v) =>
      i.kind === "elevated_errors"
        ? (v.error_rate * 100).toFixed(1) + "%"
        : v.mean_latency_ms.toFixed(0) + " ms",
    );
    const requests = e.intervals.map((v) => v.requests).join(", ");
    return `${e.rule}: ${values.join(", ")} (requests per interval: ${requests})`;
  }
  if (e.last_heartbeat_at)
    return `${e.rule}; last heartbeat ${date(e.last_heartbeat_at)}`;
  return e.rule;
}
type Summary = {
  version: string;
  requests: number;
  error_rate: number | null;
  mean_latency_ms: number | null;
  observed_nodes: number;
  observed_seconds: number;
  from: string;
  to: string;
};
async function get<T>(path: string, signal?: AbortSignal): Promise<T> {
  const r = await fetch("/api/v1/" + path, {
    signal: signal ?? AbortSignal.timeout(10000),
  });
  if (!r.ok)
    throw new Error((await r.json()).error ?? `Request failed (${r.status})`);
  return r.json();
}
const fmt = (v: number | null, unit = "") =>
  v === null
    ? "Unavailable"
    : `${v.toLocaleString(undefined, { maximumFractionDigits: 2 })}${unit}`;
const date = (v: string | null) =>
  v ? new Date(v).toLocaleString() : "Never received";
const local = (d: Date) =>
  new Date(d.getTime() - d.getTimezoneOffset() * 60000)
    .toISOString()
    .slice(0, 16);
function Chart({
  samples,
  field,
  title,
}: {
  samples: Sample[];
  field: "latency" | "errors";
  title: string;
}) {
  const values = samples.map((s, i) => {
    const p = samples[i - 1];
    if (!p) return null;
    const gap = Date.parse(s.collected_at) - Date.parse(p.collected_at),
      n = s.requests - p.requests,
      e = s.errors - p.errors,
      l = s.total_latency_ms - p.total_latency_ms;
    return !s.app_process_id ||
      s.app_process_id !== p.app_process_id ||
      s.version !== p.version ||
      gap <= 0 ||
      gap > 10000 ||
      n <= 0 ||
      e < 0 ||
      e > n ||
      l < 0
      ? null
      : field === "latency"
        ? l / n
        : (e / n) * 100;
  });
  const max = Math.max(1, ...values.filter((v): v is number => v !== null));
  const start = Date.parse(samples[0]?.collected_at ?? "") || 0;
  const span = Math.max(
    1,
    (Date.parse(samples.at(-1)?.collected_at ?? "") || 0) - start,
  );
  const x = (i: number) =>
    40 + ((Date.parse(samples[i].collected_at) - start) / span) * 700;
  const y = (v: number) => 155 - (v / max) * 125;
  return (
    <section className="panel">
      <h3>{title}</h3>
      <p className="muted">
        {field === "latency"
          ? "Request-weighted mean · ms"
          : "Errors per request · %"}
      </p>
      {values.some((v) => v !== null) ? (
        <svg
          viewBox="0 0 780 195"
          role="img"
          aria-label={title + " over time; gaps indicate missing evidence"}
        >
          <text x="3" y="25">
            {max.toFixed(1)}
          </text>
          <text x="20" y="158">
            0
          </text>
          <line x1="40" y1="155" x2="750" y2="155" stroke="#dce3e7" />
          {values.map((v, i) =>
            v === null ? null : (
              <g key={i}>
                {i > 0 && values[i - 1] !== null && (
                  <line
                    x1={x(i - 1)}
                    y1={y(values[i - 1]!)}
                    x2={x(i)}
                    y2={y(v)}
                    stroke="#007e7c"
                    strokeWidth="2"
                  />
                )}
                <circle cx={x(i)} cy={y(v)} r="2" fill="#007e7c">
                  <title>
                    {date(samples[i].collected_at)}: {v.toFixed(2)}
                  </title>
                </circle>
              </g>
            ),
          )}
          <text x="40" y="185">
            {new Date(start).toLocaleTimeString()}
          </text>
          <text x="750" y="185" textAnchor="end">
            {new Date(start + span).toLocaleTimeString()}
          </text>
        </svg>
      ) : (
        <div className="empty">No measured requests in this window.</div>
      )}
      <p className="muted">
        Gaps include restarts, missing samples, and idle intervals.
      </p>
    </section>
  );
}
// Shareable links: #/node/<id> opens node details, #/compare/<id> release comparison.
function readRoute(): { tab: string; node: string } {
  const [, kind = "", id = ""] = location.hash.replace(/^#/, "").split("/");
  const tab =
    kind === "node"
      ? "Node details"
      : kind === "compare"
        ? "Release comparison"
        : "Overview";
  return { tab, node: decodeURIComponent(id) };
}
function routeFor(tab: string, node: string): string {
  const kind =
    tab === "Node details" ? "node" : tab === "Release comparison" ? "compare" : "";
  return kind ? `#/${kind}/${encodeURIComponent(node)}` : "#/";
}
function App() {
  const [nodes, setNodes] = useState<Node[]>([]),
    [selected, setSelected] = useState(() => readRoute().node),
    [tab, setTab] = useState(() => readRoute().tab),
    [error, setError] = useState(""),
    [loaded, setLoaded] = useState(false),
    [updated, setUpdated] = useState(""),
    [after, setAfter] = useState(""),
    [pages, setPages] = useState<string[]>([]);
  const [samples, setSamples] = useState<Sample[]>([]),
    [incidents, setIncidents] = useState<Incident[]>([]),
    [detailError, setDetailError] = useState(""),
    [detailLoading, setDetailLoading] = useState(false),
    [minutes, setMinutes] = useState(30);
  const [base, setBase] = useState("v1"),
    [candidate, setCandidate] = useState("v2-slow"),
    [from, setFrom] = useState(local(new Date(Date.now() - 3600000))),
    [to, setTo] = useState(local(new Date())),
    [from2, setFrom2] = useState(local(new Date(Date.now() - 3600000))),
    [to2, setTo2] = useState(local(new Date())),
    [comparison, setComparison] = useState<{
      node_id: string;
      baseline: Summary;
      candidate: Summary;
    } | null>(null),
    [compareError, setCompareError] = useState(""),
    [busy, setBusy] = useState(false);
  useEffect(() => {
    const hash = routeFor(tab, selected);
    if (location.hash !== hash) history.replaceState(null, "", hash);
  }, [tab, selected]);
  useEffect(() => {
    const follow = () => {
      const route = readRoute();
      setTab(route.tab);
      setSelected(route.node);
    };
    window.addEventListener("hashchange", follow);
    return () => window.removeEventListener("hashchange", follow);
  }, []);
  useEffect(() => {
    let stopped = false;
    let timer: ReturnType<typeof setTimeout>;
    const controller = new AbortController();
    setLoaded(false);
    setNodes([]);
    async function poll() {
      try {
        const result = await get<Node[]>(
          "fleet?after=" + encodeURIComponent(after),
          AbortSignal.any([controller.signal, AbortSignal.timeout(10000)]),
        );
        if (!stopped) {
          setNodes(result);
          setError("");
          setUpdated(new Date().toLocaleTimeString());
          setLoaded(true);
        }
      } catch (e) {
        if (!stopped) {
          setError(String(e));
          setLoaded(true);
        }
      } finally {
        if (!stopped) timer = setTimeout(poll, 5000);
      }
    }
    poll();
    return () => {
      stopped = true;
      controller.abort();
      clearTimeout(timer);
    };
  }, [after]);
  useEffect(() => {
    setComparison(null);
    setCompareError("");
    setSamples([]);
    setIncidents([]);
    setDetailError("");
    if (!selected) return;
    let stopped = false;
    let timer: ReturnType<typeof setTimeout>;
    const controller = new AbortController();
    setDetailLoading(true);
    async function poll() {
      try {
        const q = new URLSearchParams({
          from: new Date(Date.now() - minutes * 60000).toISOString(),
          to: new Date().toISOString(),
          limit: "1000",
        });
        const signal = AbortSignal.any([
          controller.signal,
          AbortSignal.timeout(10000),
        ]);
        const [s, i] = await Promise.all([
          get<Sample[]>(
            `nodes/${encodeURIComponent(selected)}/metrics?${q}`,
            signal,
          ),
          get<Incident[]>(
            `nodes/${encodeURIComponent(selected)}/incidents`,
            signal,
          ),
        ]);
        if (!stopped) {
          setSamples(s);
          setIncidents(i);
          setDetailError("");
        }
      } catch (e) {
        if (!stopped) setDetailError(String(e));
      } finally {
        if (!stopped) {
          setDetailLoading(false);
          timer = setTimeout(poll, 5000);
        }
      }
    }
    poll();
    return () => {
      stopped = true;
      controller.abort();
      clearTimeout(timer);
    };
  }, [selected, minutes]);
  async function compare(e?: React.FormEvent) {
    e?.preventDefault();
    setBusy(true);
    setComparison(null);
    setCompareError("");
    try {
      const q = new URLSearchParams({
        baseline_version: base,
        candidate_version: candidate,
        baseline_from: new Date(from).toISOString(),
        baseline_to: new Date(to).toISOString(),
        candidate_from: new Date(from2).toISOString(),
        candidate_to: new Date(to2).toISOString(),
      });
      setComparison(
        await get(`nodes/${encodeURIComponent(selected)}/compare?${q}`),
      );
    } catch (e) {
      setCompareError(String(e));
    } finally {
      setBusy(false);
    }
  }
  // The first visit to the comparison view (including #/compare/<id> links) runs the default comparison.
  const autoCompared = useRef(false);
  useEffect(() => {
    if (!autoCompared.current && tab === "Release comparison" && selected) {
      autoCompared.current = true;
      void compare();
    }
  }, [tab, selected]);
  const node = nodes.find((n) => n.id === selected);
  return (
    <>
      <header>
        <a href="#" className="brand" onClick={() => setTab("Overview")}>
          ∿ <strong>pulse</strong>
        </a>
        <span className="environment">LOCAL SIMULATION</span>
        <span className="connection">
          {error
            ? "● Disconnected"
            : "● " + (loaded ? "Connected" : "Connecting")}
        </span>
      </header>
      <main>
        <div className="eyebrow">INVESTIGATION WORKSPACE</div>
        <h1>Understand what changed.</h1>
        <p className="intro">
          Trace node health, investigate incidents, and compare releases.
        </p>
        <nav>
          {["Overview", "Node details", "Release comparison"].map((t) => (
            <button
              key={t}
              className={tab === t ? "active" : ""}
              onClick={() => setTab(t)}
            >
              {t}
            </button>
          ))}
        </nav>
        {error && (
          <div role="alert" className="warning">
            Connection lost. Displayed data may be stale. Retrying every 5
            seconds. {error}
          </div>
        )}
        {tab === "Overview" ? (
          <>
            <div className="cards">
              <div>
                <span>Nodes on this page</span>
                <strong>{nodes.length}</strong>
              </div>
              <div>
                <span>Need attention</span>
                <strong>
                  {
                    nodes.filter((n) =>
                      ["incident", "unreachable"].includes(n.status),
                    ).length
                  }
                </strong>
              </div>
              <div>
                <span>Reporting</span>
                <strong>
                  {nodes.filter((n) => n.status === "reporting").length}
                </strong>
              </div>
            </div>
            <section className="panel">
              <div className="sectionhead">
                <h2>Fleet overview</h2>
                <span className="muted">
                  Refreshed {updated || "—"} · every 5 seconds
                </span>
              </div>
              {!loaded ? (
                <p className="empty">Loading nodes…</p>
              ) : nodes.length === 0 ? (
                <p className="empty">
                  No nodes found. Start the sample application and agent to
                  collect telemetry.
                </p>
              ) : (
                <div className="scroll">
                  <table>
                    <thead>
                      <tr>
                        <th>Node</th>
                        <th>Status</th>
                        <th>Version</th>
                        <th>Go heap</th>
                        <th>Last contact</th>
                        <th>Telemetry collected</th>
                      </tr>
                    </thead>
                    <tbody>
                      {nodes.map((n) => (
                        <tr key={n.id}>
                          <td>
                            <button
                              className="link"
                              onClick={() => {
                                setSelected(n.id);
                                setTab("Node details");
                              }}
                            >
                              {n.id}
                            </button>
                          </td>
                          <td>
                            <span className={"badge " + n.status}>
                              {n.status}
                            </span>
                          </td>
                          <td>{n.version || "Unknown"}</td>
                          <td>
                            {fmt(
                              n.memory_bytes === null
                                ? null
                                : n.memory_bytes / 1048576,
                              " MB",
                            )}
                          </td>
                          <td>{date(n.last_seen)}</td>
                          <td>
                            {date(n.latest_collected_at)}
                            {n.latest_collected_at &&
                              Date.now() - Date.parse(n.latest_collected_at) >
                                15000 && <small>Stale telemetry</small>}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
              <div className="pagination">
                <button
                  disabled={!pages.length}
                  onClick={() => {
                    setAfter(pages.at(-1)!);
                    setPages(pages.slice(0, -1));
                  }}
                >
                  Previous page
                </button>
                <button
                  disabled={nodes.length < 100}
                  onClick={() => {
                    setPages([...pages, after]);
                    setAfter(nodes.at(-1)!.id);
                  }}
                >
                  Next page
                </button>
              </div>
            </section>
            <p className="muted">
              Reporting means recent contact and a successful application probe.
              It does not establish workload performance.
            </p>
          </>
        ) : (
          <>
            <div className="toolbar">
              <label>
                Investigate node
                <select
                  value={selected}
                  onChange={(e) => setSelected(e.target.value)}
                  disabled={busy}
                >
                  <option value="">Select a node</option>
                  {selected && !node && <option>{selected}</option>}
                  {nodes.map((n) => (
                    <option key={n.id}>{n.id}</option>
                  ))}
                </select>
              </label>
              {tab === "Node details" && (
                <label>
                  History window
                  <select
                    value={minutes}
                    onChange={(e) => setMinutes(Number(e.target.value))}
                  >
                    <option value={15}>Last 15 minutes</option>
                    <option value={30}>Last 30 minutes</option>
                  </select>
                </label>
              )}
            </div>
            {!selected ? (
              <div className="panel empty">
                Select a node to begin investigating.
              </div>
            ) : tab === "Node details" ? (
              <>
                {detailError && (
                  <p role="alert" className="warning">
                    History unavailable or stale. Retrying. {detailError}
                  </p>
                )}
                {detailLoading ? (
                  <p>Loading measurements…</p>
                ) : (
                  <>
                    <div className="sectionhead">
                      <h2>{selected}</h2>
                      <span className="muted">
                        {samples.length} samples · local time
                      </span>
                    </div>
                    {samples.length > 0 &&
                      Date.now() -
                        Date.parse(samples[samples.length - 1].collected_at) >
                        15000 && (
                        <p className="warning">
                          Telemetry is stale. Charts show historical
                          observations, not current health.
                        </p>
                      )}
                    {samples.length === 1000 && (
                      <p className="warning">
                        Sample limit reached. Only the earliest 1,000 samples
                        are shown; choose a shorter window.
                      </p>
                    )}
                    <div className="charts">
                      <Chart
                        samples={samples}
                        field="latency"
                        title="Response latency"
                      />
                      <Chart
                        samples={samples}
                        field="errors"
                        title="Error rate"
                      />
                    </div>
                    <section className="panel">
                      <h2>Version timeline</h2>
                      {samples
                        .filter(
                          (s, i) =>
                            i === 0 || s.version !== samples[i - 1].version,
                        )
                        .map((s) => (
                          <p key={s.collected_at}>
                            <span className="badge">{s.version}</span>{" "}
                            {date(s.collected_at)}
                          </p>
                        ))}
                      {!samples.length && (
                        <p>No version observations in this window.</p>
                      )}
                    </section>
                    <section className="panel">
                      <h2>Incident timeline</h2>
                      <p className="muted">Latest 100 incidents · all time</p>
                      {incidents.length ? (
                        incidents.map((i) => (
                          <article className="incident" key={i.id}>
                            <span
                              className={
                                "badge " +
                                (i.resolved_at ? "reporting" : "incident")
                              }
                            >
                              {i.resolved_at ? "Resolved" : "Open"}
                            </span>
                            <div>
                              <strong>{i.kind.replaceAll("_", " ")}</strong>
                              <p>
                                {i.version || "Version unknown"} · Opened{" "}
                                {date(i.opened_at)}
                              </p>
                              <p className="muted">
                                Evidence: {evidenceText(i)}
                              </p>
                              <p>
                                {i.resolved_at
                                  ? "Resolved " + date(i.resolved_at)
                                  : "Awaiting recovery evidence"}
                              </p>
                            </div>
                          </article>
                        ))
                      ) : (
                        <p>No recorded incidents.</p>
                      )}
                    </section>
                  </>
                )}
              </>
            ) : (
              <section className="panel">
                <h2>Compare releases</h2>
                <p className="muted">
                  Choose two versions and observation windows. All input times
                  are local; each window can span up to 24 hours.
                </p>
                <form onSubmit={compare}>
                  <div className="compareform">
                    <fieldset>
                      <legend>Baseline</legend>
                      <label>
                        Version
                        <input
                          required
                          maxLength={128}
                          value={base}
                          onChange={(e) => setBase(e.target.value)}
                        />
                      </label>
                      <label>
                        From
                        <input
                          required
                          type="datetime-local"
                          value={from}
                          onChange={(e) => setFrom(e.target.value)}
                        />
                      </label>
                      <label>
                        To
                        <input
                          required
                          type="datetime-local"
                          value={to}
                          onChange={(e) => setTo(e.target.value)}
                        />
                      </label>
                    </fieldset>
                    <fieldset>
                      <legend>Candidate</legend>
                      <label>
                        Version
                        <input
                          required
                          maxLength={128}
                          value={candidate}
                          onChange={(e) => setCandidate(e.target.value)}
                        />
                      </label>
                      <label>
                        From
                        <input
                          required
                          type="datetime-local"
                          value={from2}
                          onChange={(e) => setFrom2(e.target.value)}
                        />
                      </label>
                      <label>
                        To
                        <input
                          required
                          type="datetime-local"
                          value={to2}
                          onChange={(e) => setTo2(e.target.value)}
                        />
                      </label>
                    </fieldset>
                  </div>
                  <button className="primary" disabled={busy}>
                    {busy ? "Comparing…" : "Compare releases"}
                  </button>
                </form>
                {compareError && (
                  <p role="alert" className="warning">
                    {compareError}
                  </p>
                )}
                {comparison && comparison.node_id === selected && (
                  <div aria-live="polite" className="scroll">
                    <table>
                      <caption>Comparison results for {selected}</caption>
                      <thead>
                        <tr>
                          <th>Measurement</th>
                          <th>{comparison.baseline.version}</th>
                          <th>{comparison.candidate.version}</th>
                        </tr>
                      </thead>
                      <tbody>
                        {(
                          [
                            "requests",
                            "error_rate",
                            "mean_latency_ms",
                            "observed_nodes",
                            "observed_seconds",
                          ] as const
                        ).map((key) => (
                          <tr key={key}>
                            <th>
                              {
                                {
                                  requests: "Observed requests",
                                  error_rate: "Error rate",
                                  mean_latency_ms: "Mean latency",
                                  observed_nodes: "Observed nodes",
                                  observed_seconds: "Observed seconds",
                                }[key]
                              }
                            </th>
                            {[comparison.baseline, comparison.candidate].map(
                              (s, i) => (
                                <td key={i}>
                                  {fmt(
                                    s[key] === null
                                      ? null
                                      : key === "error_rate"
                                        ? s[key]! * 100
                                        : s[key],
                                    key === "error_rate"
                                      ? "%"
                                      : key === "mean_latency_ms"
                                        ? " ms"
                                        : "",
                                  )}
                                </td>
                              ),
                            )}
                          </tr>
                        ))}
                        <tr>
                          <th>Window</th>
                          {[comparison.baseline, comparison.candidate].map(
                            (s, i) => (
                              <td key={i}>
                                {date(s.from)} → {date(s.to)}
                              </td>
                            ),
                          )}
                        </tr>
                      </tbody>
                    </table>
                  </div>
                )}
                <p className="muted">
                  Unavailable means insufficient measured traffic. Restarts and
                  gaps are excluded. Version differences show correlation, not
                  proof of cause.
                </p>
              </section>
            )}
          </>
        )}
        <footer>
          Pulse / Telemetry & diagnostics{" "}
          <span>Simulated nodes · Go heap is not host memory</span>
        </footer>
      </main>
    </>
  );
}
createRoot(document.getElementById("root")!).render(<App />);
