import React, { useEffect, useMemo, useState } from "react";
import { Comparison, Incident, Sample, Summary, dateTime, get, isRegression, ms, number, percent } from "./api";
import { Route, href } from "./route";
import { Icon, NodeSearch, Notice, Segmented } from "./ui";

const PRESETS = [
  { value: "15", label: "15 min" },
  { value: "60", label: "1 h" },
  { value: "360", label: "6 h" },
  { value: "1440", label: "24 h" },
  { value: "custom", label: "Custom" },
];
// datetime-local inputs hold whole minutes in local time.
const toInput = (t: number) => new Date(t - new Date(t).getTimezoneOffset() * 60000).toISOString().slice(0, 16);
const fromInput = (v: string) => new Date(v).toISOString();
// Round the end up to the next minute so the newest data is never cut off.
const nextMinute = () => Math.ceil((Date.now() + 1) / 60000) * 60000;

// Versions in order of first appearance, then sensible defaults: the most recent
// regression's version against the version that ran just before it.
function suggest(samples: Sample[], incidents: Incident[]) {
  const versions: string[] = [];
  samples.forEach((s) => !versions.includes(s.version) && versions.push(s.version));
  incidents.forEach((i) => i.version && !versions.includes(i.version) && versions.push(i.version));
  const regression = incidents.find((i) => isRegression(i.kind) && i.version);
  const candidate = regression?.version ?? samples[samples.length - 1]?.version ?? "";
  const first = samples.findIndex((s) => s.version === candidate);
  const before = first > 0 ? samples[first - 1].version : versions.find((v) => v !== candidate) ?? "";
  return { versions, candidate, baseline: before !== candidate ? before : "" };
}

function change(metric: "latency" | "errors", base: Summary, cand: Summary) {
  if (metric === "latency") {
    if (base.mean_latency_ms === null || cand.mean_latency_ms === null || base.mean_latency_ms === 0) return null;
    const ratio = cand.mean_latency_ms / base.mean_latency_ms;
    const text = ratio >= 2 ? `${ratio.toFixed(ratio >= 10 ? 0 : 1)}× slower` : ratio <= 0.5 ? `${(1 / ratio).toFixed(1)}× faster` : `${ratio >= 1 ? "+" : ""}${((ratio - 1) * 100).toFixed(0)}%`;
    return { text, tone: ratio > 1.2 ? "worse" : ratio < 0.83 ? "better" : "same" };
  }
  if (base.error_rate === null || cand.error_rate === null) return null;
  const diff = (cand.error_rate - base.error_rate) * 100;
  return {
    text: `${diff >= 0 ? "+" : ""}${diff.toFixed(1)} pts`,
    tone: diff > 1 ? "worse" : diff < -1 ? "better" : "same",
  };
}

function Change({ value }: { value: ReturnType<typeof change> }) {
  if (!value) return <span className="subtle">—</span>;
  const label = value.tone === "worse" ? "Worse" : value.tone === "better" ? "Better" : "Similar";
  return (
    <span className={`delta delta-${value.tone}`}>
      {value.tone !== "same" && <Icon name={value.tone === "worse" ? "alert" : "check"} size={14} />}
      <strong>{value.text}</strong> <span className="subtle">{label}</span>
    </span>
  );
}

export function CompareView({ route }: { route: Route }) {
  const node = route.node;
  const p = route.params;
  const [samples, setSamples] = useState<Sample[]>([]);
  const [incidents, setIncidents] = useState<Incident[]>([]);
  const [loaded, setLoaded] = useState(false);

  // Window: explicit from/to in the link, otherwise a preset ending now.
  const [preset, setPreset] = useState(p.get("from") ? "custom" : p.get("range") ?? "60");
  const end = p.get("to") ? Date.parse(p.get("to")!) : nextMinute();
  const start = p.get("from") ? Date.parse(p.get("from")!) : end - Number(p.get("range") ?? 60) * 60000;
  const [from, setFrom] = useState(toInput(start));
  const [to, setTo] = useState(toInput(end));
  const [separate, setSeparate] = useState(Boolean(p.get("cfrom")));
  const [cfrom, setCfrom] = useState(toInput(p.get("cfrom") ? Date.parse(p.get("cfrom")!) : start));
  const [cto, setCto] = useState(toInput(p.get("cto") ? Date.parse(p.get("cto")!) : end));
  const [base, setBase] = useState(p.get("base") ?? "");
  const [candidate, setCandidate] = useState(p.get("candidate") ?? "");
  const [result, setResult] = useState<Comparison | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  // Load recent history for version suggestions and defaults.
  useEffect(() => {
    if (!node) return;
    const controller = new AbortController();
    const q = new URLSearchParams({
      from: new Date(Math.max(start, end - 3600000)).toISOString(),
      to: new Date(end).toISOString(),
      limit: "1000",
    });
    Promise.all([
      get<Sample[]>(`nodes/${encodeURIComponent(node)}/metrics?${q}`, controller.signal),
      get<Incident[]>(`nodes/${encodeURIComponent(node)}/incidents`, controller.signal),
    ])
      .then(([s, i]) => {
        setSamples(s);
        setIncidents(i);
        const d = suggest(s, i);
        setCandidate((c) => c || d.candidate);
        setBase((b) => b || d.baseline);
      })
      .catch(() => undefined)
      .finally(() => setLoaded(true));
    return () => controller.abort();
    // Reload suggestions when the node or link changes, not on every form edit.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [node, route.params.toString()]);

  const versions = useMemo(() => suggest(samples, incidents).versions, [samples, incidents]);

  const windows = () => {
    if (preset !== "custom") {
      const e = nextMinute();
      const s = e - Number(preset) * 60000;
      return { bf: new Date(s).toISOString(), bt: new Date(e).toISOString(), cf: new Date(s).toISOString(), ct: new Date(e).toISOString() };
    }
    return {
      bf: fromInput(from),
      bt: fromInput(to),
      cf: fromInput(separate ? cfrom : from),
      ct: fromInput(separate ? cto : to),
    };
  };

  async function run() {
    if (!node || !base || !candidate) return;
    const w = windows();
    setBusy(true);
    setError("");
    try {
      const q = new URLSearchParams({
        baseline_version: base,
        candidate_version: candidate,
        baseline_from: w.bf,
        baseline_to: w.bt,
        candidate_from: w.cf,
        candidate_to: w.ct,
      });
      setResult(await get<Comparison>(`nodes/${encodeURIComponent(node)}/compare?${q}`));
    } catch (e) {
      setResult(null);
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }
  // Run automatically once the defaults are known, so links show results on arrival.
  const [autoRan, setAutoRan] = useState(false);
  useEffect(() => {
    if (!autoRan && loaded && base && candidate) {
      setAutoRan(true);
      void run();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [loaded, base, candidate]);

  const shareable = () => {
    const w = windows();
    const params: Record<string, string> = { base, candidate };
    if (preset === "custom") {
      Object.assign(params, { from: w.bf, to: w.bt });
      if (separate) Object.assign(params, { cfrom: w.cf, cto: w.ct });
    } else params.range = preset;
    return href("compare", node, params);
  };

  if (!node)
    return (
      <>
        <div className="page-head">
          <div>
            <h1>Compare releases</h1>
            <p className="subtle">Pick a node to compare two of its versions.</p>
          </div>
        </div>
        <section className="card empty-card">
          <NodeSearch view="compare" />
        </section>
      </>
    );

  const latency = result && change("latency", result.baseline, result.candidate);
  const errors = result && change("errors", result.baseline, result.candidate);

  return (
    <>
      <nav className="breadcrumb" aria-label="Breadcrumb">
        <a href={href("fleet")}>Fleet</a>
        <span aria-hidden="true">/</span>
        <a href={href("node", node)}>{node}</a>
        <span aria-hidden="true">/</span>
        <span aria-current="page">Compare releases</span>
      </nav>
      <div className="page-head">
        <div>
          <h1>Compare releases</h1>
          <p className="subtle">
            Same-process measurements only; restarts and gaps are excluded. A difference shows correlation, not proof
            of cause.
          </p>
        </div>
        <NodeSearch current={node} view="compare" placeholder="Switch node…" />
      </div>

      <form
        className="card compare-form"
        onSubmit={(e) => {
          e.preventDefault();
          history.replaceState(null, "", shareable());
          void run();
        }}
      >
        <div className="versions">
          <label>
            Baseline version
            <input required list="versions" value={base} onChange={(e) => setBase(e.target.value)} placeholder="e.g. v1" />
          </label>
          <Icon name="arrow" size={20} />
          <label>
            Candidate version
            <input
              required
              list="versions"
              value={candidate}
              onChange={(e) => setCandidate(e.target.value)}
              placeholder="e.g. v2-slow"
            />
          </label>
          <datalist id="versions">
            {versions.map((v) => (
              <option key={v} value={v} />
            ))}
          </datalist>
        </div>
        <div className="window">
          <span className="field-label">Time window</span>
          <Segmented label="Time window" value={preset} onChange={setPreset} options={PRESETS} />
        </div>
        {preset === "custom" && (
          <div className="custom-window">
            <label>
              {separate ? "Baseline from" : "From"}
              <input type="datetime-local" required value={from} onChange={(e) => setFrom(e.target.value)} />
            </label>
            <label>
              {separate ? "Baseline to" : "To"}
              <input type="datetime-local" required value={to} onChange={(e) => setTo(e.target.value)} />
            </label>
            {separate && (
              <>
                <label>
                  Candidate from
                  <input type="datetime-local" required value={cfrom} onChange={(e) => setCfrom(e.target.value)} />
                </label>
                <label>
                  Candidate to
                  <input type="datetime-local" required value={cto} onChange={(e) => setCto(e.target.value)} />
                </label>
              </>
            )}
            <label className="checkbox">
              <input type="checkbox" checked={separate} onChange={(e) => setSeparate(e.target.checked)} />
              Use a different window for the candidate
            </label>
          </div>
        )}
        <div className="form-actions">
          <button className="primary" disabled={busy || !base || !candidate}>
            {busy ? "Comparing…" : "Compare"}
          </button>
          <span className="subtle">Each window can span up to 24 hours.</span>
        </div>
      </form>

      {error && <Notice>{error}</Notice>}
      {loaded && !base && !candidate && !result && (
        <Notice tone="info">No releases found in the last hour for {node}. Enter two versions to compare.</Notice>
      )}

      {result && (
        <section className="card" aria-live="polite">
          <header className="card-head">
            <div>
              <h2>
                {result.baseline.version} → {result.candidate.version}
              </h2>
              <p className="verdict">
                {latency && result.candidate.mean_latency_ms !== null && result.baseline.mean_latency_ms !== null ? (
                  <>
                    {result.candidate.version} averaged <strong>{ms(result.candidate.mean_latency_ms)}</strong> per
                    request against <strong>{ms(result.baseline.mean_latency_ms)}</strong> for{" "}
                    {result.baseline.version}, with error rates of <strong>{percent(result.candidate.error_rate)}</strong>{" "}
                    and <strong>{percent(result.baseline.error_rate)}</strong>.
                  </>
                ) : (
                  <>Not enough measured traffic on one side to compare. Widen the window or check the version names.</>
                )}
              </p>
            </div>
          </header>
          <div className="table-scroll">
            <table className="comparison">
              <thead>
                <tr>
                  <th>Measurement</th>
                  <th className="num">{result.baseline.version}</th>
                  <th className="num">{result.candidate.version}</th>
                  <th>Change</th>
                </tr>
              </thead>
              <tbody>
                <tr>
                  <th scope="row">Mean latency</th>
                  <td className="num">{ms(result.baseline.mean_latency_ms)}</td>
                  <td className="num">{ms(result.candidate.mean_latency_ms)}</td>
                  <td>
                    <Change value={latency} />
                  </td>
                </tr>
                <tr>
                  <th scope="row">Error rate</th>
                  <td className="num">{percent(result.baseline.error_rate)}</td>
                  <td className="num">{percent(result.candidate.error_rate)}</td>
                  <td>
                    <Change value={errors} />
                  </td>
                </tr>
                <tr>
                  <th scope="row">Requests measured</th>
                  <td className="num">{number(result.baseline.requests)}</td>
                  <td className="num">{number(result.candidate.requests)}</td>
                  <td />
                </tr>
                <tr>
                  <th scope="row">Time observed</th>
                  <td className="num">{number(result.baseline.observed_seconds)} s</td>
                  <td className="num">{number(result.candidate.observed_seconds)} s</td>
                  <td />
                </tr>
              </tbody>
            </table>
          </div>
          <p className="footnote">
            Windows: {dateTime(result.baseline.from)} – {dateTime(result.baseline.to)}
            {result.candidate.from !== result.baseline.from || result.candidate.to !== result.baseline.to
              ? ` (baseline); ${dateTime(result.candidate.from)} – ${dateTime(result.candidate.to)} (candidate)`
              : ""}{" "}
            · “—” means no measurable traffic for that version in the window.
          </p>
        </section>
      )}
    </>
  );
}
