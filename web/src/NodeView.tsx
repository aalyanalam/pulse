import React, { useEffect, useMemo, useState } from "react";
import { Chart } from "./Chart";
import {
  FleetNode,
  Incident,
  Interval,
  Sample,
  ago,
  dateTime,
  duration,
  get,
  intervals,
  isRegression,
  kindLabel,
  ms,
  number,
  percent,
  poll,
} from "./api";
import { href } from "./route";
import { Icon, NodeSearch, Notice, Segmented, StatusBadge, Tile } from "./ui";

// Comparison link for a regression: the incident's version against whatever
// ran before it, over a window around the incident.
export function compareLink(node: string, i: Incident): string {
  const opened = Date.parse(i.opened_at);
  const end = i.resolved_at ? Date.parse(i.resolved_at) + 120000 : Date.now() + 60000;
  return href("compare", node, {
    candidate: i.version,
    from: new Date(opened - 15 * 60000).toISOString(),
    to: new Date(end).toISOString(),
  });
}

function Evidence({ incident }: { incident: Incident }) {
  const e = incident.evidence;
  if (!e?.rule) return <p className="subtle">Evidence not recorded (incident predates evidence capture).</p>;
  return (
    <div className="evidence">
      <p className="subtle">{e.rule}</p>
      {e.intervals?.length ? (
        <ul className="evidence-intervals" aria-label="Measured intervals that opened the incident">
          {e.intervals.map((v) => (
            <li key={v.ended_at}>
              <strong>{incident.kind === "elevated_errors" ? percent(v.error_rate) : ms(v.mean_latency_ms)}</strong>
              <span className="subtle">{number(v.requests)} req</span>
            </li>
          ))}
        </ul>
      ) : e.last_heartbeat_at ? (
        <p className="subtle">Last heartbeat {dateTime(e.last_heartbeat_at)}</p>
      ) : null}
    </div>
  );
}

export function IncidentList({ node, incidents }: { node: string; incidents: Incident[] }) {
  const [all, setAll] = useState(false);
  const shown = all ? incidents : incidents.slice(0, 6);
  if (!incidents.length) return <p className="empty">No incidents recorded for this node.</p>;
  return (
    <>
      <ol className="incidents">
        {shown.map((i) => (
          <li key={i.id} className={i.resolved_at ? "resolved" : "open"}>
            <span className={`status ${i.resolved_at ? "status-reporting" : "status-incident"}`}>
              <Icon name={i.resolved_at ? "check" : "alert"} size={14} />
              {i.resolved_at ? "Resolved" : "Open"}
            </span>
            <div className="incident-body">
              <div className="incident-title">
                <strong>{kindLabel[i.kind] ?? i.kind}</strong>
                {i.version && <span className="chip">{i.version}</span>}
              </div>
              <p className="subtle">
                Opened {dateTime(i.opened_at)} · {i.resolved_at ? "lasted" : "open for"}{" "}
                {duration(i.opened_at, i.resolved_at)}
              </p>
              <Evidence incident={i} />
            </div>
            {isRegression(i.kind) && i.version && (
              <a className="button" href={compareLink(node, i)}>
                Compare release <Icon name="arrow" size={14} />
              </a>
            )}
          </li>
        ))}
      </ol>
      {incidents.length > 6 && (
        <button type="button" className="link-button" onClick={() => setAll(!all)}>
          {all ? "Show recent only" : `Show all ${incidents.length} incidents`}
        </button>
      )}
    </>
  );
}

const WINDOWS = [5, 15, 30];

export function NodeView({ node, initialMinutes }: { node: string; initialMinutes?: number }) {
  const [minutes, setMinutes] = useState(WINDOWS.includes(initialMinutes ?? 0) ? initialMinutes! : 15);
  // Keep the chosen window in the link so it can be shared.
  useEffect(() => {
    const link = minutes === 15 ? href("node", node) : href("node", node, { minutes: String(minutes) });
    if (location.hash !== link) history.replaceState(null, "", link);
  }, [node, minutes]);
  const [samples, setSamples] = useState<Sample[] | null>(null);
  const [incidents, setIncidents] = useState<Incident[]>([]);
  const [info, setInfo] = useState<FleetNode | null>(null);
  const [error, setError] = useState("");
  const [now, setNow] = useState(Date.now());

  useEffect(() => {
    setSamples(null);
    setIncidents([]);
    setInfo(null);
  }, [node]);
  useEffect(
    () =>
      poll(async (signal) => {
        const end = Date.now();
        const q = new URLSearchParams({
          from: new Date(end - minutes * 60000).toISOString(),
          to: new Date(end + 1000).toISOString(),
          limit: "1000",
        });
        try {
          const id = encodeURIComponent(node);
          const [s, i, fleet] = await Promise.all([
            get<Sample[]>(`nodes/${id}/metrics?${q}`, signal),
            get<Incident[]>(`nodes/${id}/incidents`, signal),
            get<FleetNode[]>("fleet?q=" + id, signal),
          ]);
          setSamples(s);
          setIncidents(i);
          setInfo(fleet.find((f) => f.id === node) ?? null);
          setNow(end);
          setError("");
        } catch (e) {
          if (!signal.aborted) setError(String(e));
        }
      }),
    [node, minutes],
  );

  const totals = useMemo(() => {
    const valid = intervals(samples ?? []).filter((m): m is Interval => m !== null);
    const requests = valid.reduce((a, m) => a + m.requests, 0);
    const errors = valid.reduce((a, m) => a + m.errors, 0);
    const latency = valid.reduce((a, m) => a + m.latency, 0);
    return {
      requests,
      errorRate: requests ? errors / requests : null,
      latency: requests ? latency / requests : null,
    };
  }, [samples]);
  const open = incidents.filter((i) => !i.resolved_at);
  const windowStart = now - minutes * 60000;
  const latest = samples?.length ? samples[samples.length - 1] : null;

  return (
    <>
      <nav className="breadcrumb" aria-label="Breadcrumb">
        <a href={href("fleet")}>Fleet</a>
        <span aria-hidden="true">/</span>
        <span aria-current="page">{node}</span>
      </nav>
      <div className="page-head">
        <div>
          <h1 className="node-title">
            {node}
            {info && <StatusBadge status={info.status} />}
          </h1>
          <p className="subtle">
            {info?.version ? (
              <>
                Running <span className="chip">{info.version}</span> ·{" "}
              </>
            ) : null}
            Last heartbeat {ago(info?.last_seen ?? null, now)}
          </p>
        </div>
        <NodeSearch current={node} placeholder="Switch node…" />
      </div>

      {error && <Notice>Can’t load this node right now. Showing the last data received; retrying every 5 s.</Notice>}
      {open.map((i) => (
        <Notice key={i.id} tone="critical">
          <strong>{kindLabel[i.kind] ?? i.kind}</strong>
          {i.version && <> on {i.version}</>} — open for {duration(i.opened_at, null, now)}.{" "}
          {isRegression(i.kind) && i.version && <a href={compareLink(node, i)}>Compare this release →</a>}
        </Notice>
      ))}

      <div className="filters">
        <Segmented
          label="History window"
          value={minutes}
          onChange={setMinutes}
          options={[
            { value: 5, label: "Last 5 min" },
            { value: 15, label: "Last 15 min" },
            { value: 30, label: "Last 30 min" },
          ]}
        />
        <span className="subtle">
          {samples ? `${number(samples.length)} samples · refreshes every 5 s` : "Loading…"}
        </span>
      </div>

      {samples !== null && samples.length >= 1000 && (
        <Notice>Sample limit reached: only the earliest 1,000 samples are shown. Choose a shorter window.</Notice>
      )}
      {latest && now - Date.parse(latest.collected_at) > 15000 && (
        <Notice tone="info">
          No telemetry for {ago(latest.collected_at, now).replace(" ago", "")}. Charts show history, not current health.
        </Notice>
      )}

      <div className="tiles">
        <Tile label="Requests" value={number(totals.requests)} detail={`Measured in the last ${minutes} min`} />
        <Tile label="Error rate" value={percent(totals.errorRate)} detail="Errors ÷ requests" />
        <Tile label="Mean latency" value={ms(totals.latency)} detail="Request-weighted" />
        <Tile label="Open incidents" value={open.length} tone={open.length ? "critical" : "good"} />
      </div>

      {samples === null ? (
        <p className="empty">Loading measurements…</p>
      ) : (
        <div className="charts">
          <Chart
            title="Response latency"
            subtitle="Mean per 3-second interval · ms"
            field="latency"
            samples={samples}
            incidents={incidents}
            windowStart={windowStart}
            windowEnd={now}
          />
          <Chart
            title="Error rate"
            subtitle="Errors per request · %"
            field="errors"
            samples={samples}
            incidents={incidents}
            windowStart={windowStart}
            windowEnd={now}
          />
        </div>
      )}

      <section className="card">
        <header className="card-head">
          <div>
            <h2>Incidents</h2>
            <p className="subtle">Newest first, with the evidence that opened each one.</p>
          </div>
        </header>
        <IncidentList node={node} incidents={incidents} />
      </section>
    </>
  );
}
