// Data types, API access, connection health and formatting shared by every view.

export type Status = "reporting" | "incident" | "unreachable" | "unknown";
export type FleetNode = {
  id: string;
  version: string;
  status: Status;
  last_seen: string | null;
  latest_collected_at: string | null;
  memory_bytes: number | null;
};
export type FleetSummary = { total: number; by_status: Record<Status, number> };
export type Sample = {
  collected_at: string;
  version: string;
  app_process_id: string;
  requests: number;
  errors: number;
  total_latency_ms: number;
};
export type EvidenceInterval = {
  ended_at: string;
  requests: number;
  errors: number;
  error_rate: number;
  mean_latency_ms: number;
};
export type Incident = {
  id: number;
  kind: string;
  version: string;
  opened_at: string;
  resolved_at: string | null;
  evidence?: {
    rule?: string;
    intervals?: EvidenceInterval[];
    last_heartbeat_at?: string;
    consecutive_failures?: number;
  };
};
export type Summary = {
  version: string;
  requests: number;
  errors: number;
  error_rate: number | null;
  mean_latency_ms: number | null;
  observed_nodes: number;
  observed_seconds: number;
  from: string;
  to: string;
};
export type Comparison = { node_id: string; baseline: Summary; candidate: Summary };

// Connection health: every request reports success or failure, so the header
// can show one honest "live / reconnecting" indicator for the whole app.
type Health = { ok: boolean; at: number };
let health: Health = { ok: true, at: 0 };
const listeners = new Set<(h: Health) => void>();
export function onHealth(listener: (h: Health) => void) {
  listeners.add(listener);
  return () => void listeners.delete(listener);
}
function report(ok: boolean) {
  health = { ok, at: ok ? Date.now() : health.at };
  listeners.forEach((l) => l(health));
}

export async function get<T>(path: string, signal?: AbortSignal): Promise<T> {
  try {
    const r = await fetch("/api/v1/" + path, {
      signal: signal ?? AbortSignal.timeout(10000),
    });
    if (!r.ok) {
      const body = await r.json().catch(() => ({}));
      if (r.status >= 500) report(false);
      throw new Error(body.error ?? `Request failed (HTTP ${r.status})`);
    }
    const data = (await r.json()) as T;
    report(true);
    return data;
  } catch (e) {
    if (e instanceof TypeError) report(false); // Network failure.
    throw e;
  }
}

// Poll a loader every `ms` while mounted; restarts when `key` changes.
export function poll(
  load: (signal: AbortSignal) => Promise<void>,
  ms = 5000,
): () => void {
  let stopped = false;
  let timer: ReturnType<typeof setTimeout>;
  const controller = new AbortController();
  const run = async () => {
    try {
      await load(AbortSignal.any([controller.signal, AbortSignal.timeout(10000)]));
    } catch {
      // The caller records its own error state; polling continues.
    } finally {
      if (!stopped) timer = setTimeout(run, ms);
    }
  };
  void run();
  return () => {
    stopped = true;
    controller.abort();
    clearTimeout(timer);
  };
}

// Interval measurements from cumulative counters: only adjacent samples from the
// same application process and version, without counter resets or long gaps.
export type Interval = {
  at: number;
  version: string;
  requests: number;
  errors: number;
  latency: number;
};
export function intervals(samples: Sample[]): (Interval | null)[] {
  return samples.map((s, i) => {
    const p = samples[i - 1];
    if (!p) return null;
    const gap = Date.parse(s.collected_at) - Date.parse(p.collected_at),
      n = s.requests - p.requests,
      e = s.errors - p.errors,
      l = s.total_latency_ms - p.total_latency_ms;
    const valid =
      s.app_process_id &&
      s.app_process_id === p.app_process_id &&
      s.version === p.version &&
      gap > 0 &&
      gap <= 10000 &&
      n > 0 &&
      e >= 0 &&
      e <= n &&
      l >= 0;
    return valid
      ? { at: Date.parse(s.collected_at), version: s.version, requests: n, errors: e, latency: l }
      : null;
  });
}

// Formatting.
export const number = (v: number) => v.toLocaleString(undefined, { maximumFractionDigits: 0 });
export function ms(v: number | null): string {
  if (v === null) return "—";
  const digits = v < 10 ? 2 : v < 100 ? 1 : 0;
  return v.toLocaleString(undefined, { maximumFractionDigits: digits, minimumFractionDigits: digits }) + " ms";
}
export function percent(v: number | null): string {
  if (v === null) return "—";
  const p = v * 100;
  return p.toLocaleString(undefined, { maximumFractionDigits: p < 10 ? 1 : 0 }) + "%";
}
export const clock = (t: string | number) =>
  new Date(t).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit" });
export const dateTime = (t: string | number) =>
  new Date(t).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "medium" });
export function ago(t: string | null, now = Date.now()): string {
  if (!t) return "Never";
  const s = Math.max(0, Math.round((now - Date.parse(t)) / 1000));
  if (s < 60) return `${s} s ago`;
  if (s < 3600) return `${Math.floor(s / 60)} min ago`;
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`;
  return `${Math.floor(s / 86400)} d ago`;
}
export function duration(from: string, to: string | null, now = Date.now()): string {
  const s = Math.max(0, Math.round(((to ? Date.parse(to) : now) - Date.parse(from)) / 1000));
  if (s < 60) return `${s} s`;
  if (s < 3600) return `${Math.floor(s / 60)} min ${s % 60} s`;
  return `${Math.floor(s / 3600)} h ${Math.floor((s % 3600) / 60)} min`;
}
export const kindLabel: Record<string, string> = {
  slow_responses: "Slow responses",
  elevated_errors: "Elevated errors",
  application_unavailable: "Application unavailable",
  agent_unreachable: "Agent unreachable",
};
export const isRegression = (kind: string) =>
  kind === "slow_responses" || kind === "elevated_errors";
