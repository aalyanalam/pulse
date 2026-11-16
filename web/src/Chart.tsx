import React, { useLayoutEffect, useMemo, useRef, useState } from "react";
import { Incident, Interval, Sample, clock, intervals, kindLabel, ms, number, percent } from "./api";

// Measure the rendered width so text and marks stay at real pixel sizes at
// every screen width (a scaled viewBox shrinks axis labels on phones).
function useWidth<T extends HTMLElement>(): [React.RefObject<T | null>, number] {
  const ref = useRef<T>(null);
  const [width, setWidth] = useState(0);
  // Measure synchronously on mount (ResizeObserver callbacks can be delayed,
  // for example in background tabs), then follow later resizes.
  useLayoutEffect(() => {
    if (!ref.current) return;
    setWidth(ref.current.getBoundingClientRect().width);
    const observer = new ResizeObserver(([entry]) => setWidth(entry.contentRect.width));
    observer.observe(ref.current);
    return () => observer.disconnect();
  }, []);
  return [ref, width];
}

// Clean axis ticks: 0 and three or four steps of 1, 2 or 5 × 10^k.
function ticks(max: number): number[] {
  const raw = max / 4;
  const power = 10 ** Math.floor(Math.log10(raw || 1));
  const step = [1, 2, 5, 10].map((m) => m * power).find((s) => s >= raw) ?? power * 10;
  const result = [];
  for (let v = 0; v <= max + step * 0.001; v += step) result.push(v);
  if (result[result.length - 1] < max) result.push(result[result.length - 1] + step);
  return result;
}

type Point = { x: number; y: number; value: number; interval: Interval };
const HEIGHT = 236;
const M = { top: 40, right: 16, bottom: 30, left: 52 };

export function Chart({
  title,
  subtitle,
  field,
  samples,
  incidents,
  windowStart,
  windowEnd,
}: {
  title: string;
  subtitle: string;
  field: "latency" | "errors";
  samples: Sample[];
  incidents: Incident[];
  windowStart: number;
  windowEnd: number;
}) {
  const [ref, width] = useWidth<HTMLDivElement>();
  const [active, setActive] = useState<number | null>(null);
  const measured = useMemo(() => intervals(samples), [samples]);
  const value = (i: Interval) => (field === "latency" ? i.latency / i.requests : i.errors / i.requests);
  const format = (v: number) => (field === "latency" ? ms(v) : percent(v));
  const values = measured.filter((m): m is Interval => m !== null).map(value);
  const yTicks = ticks(Math.max(field === "latency" ? 1 : 0.01, ...values));
  const yMax = yTicks[yTicks.length - 1];
  const yStep = yTicks[1] - yTicks[0];
  const tickLabel = (t: number) =>
    field === "latency"
      ? t.toLocaleString(undefined, { maximumFractionDigits: yStep < 1 ? 1 : 0 })
      : (t * 100).toLocaleString(undefined, { maximumFractionDigits: yStep < 0.01 ? 1 : 0 }) + "%";
  const plotW = Math.max(0, width - M.left - M.right);
  const plotH = HEIGHT - M.top - M.bottom;
  const x = (t: number) => M.left + ((t - windowStart) / (windowEnd - windowStart)) * plotW;
  const y = (v: number) => M.top + plotH - (v / yMax) * plotH;

  const points: (Point | null)[] = measured.map((m) =>
    m && m.at >= windowStart ? { x: x(m.at), y: y(value(m)), value: value(m), interval: m } : null,
  );
  const valid = points.filter((p): p is Point => p !== null);
  // Lines break at missing evidence (restarts, gaps, version changes).
  let path = "";
  points.forEach((p, i) => {
    if (!p) return;
    path += `${points[i - 1] ? "L" : "M"}${p.x.toFixed(1)},${p.y.toFixed(1)}`;
  });
  // Release markers where the reported version changes.
  const releases = samples
    .map((s, i) => ({ at: Date.parse(s.collected_at), version: s.version, changed: i > 0 && s.version !== samples[i - 1].version }))
    .filter((r) => r.changed && r.at >= windowStart);
  // Labels sit on two staggered rows above the plot so a deploy and its quick
  // rollback both stay named; working newest-first, a marker with no free row
  // goes unlabelled (its version is still in the tooltip and table).
  const rowEnds = [Infinity, Infinity];
  const markers = releases
    .map((r) => ({ ...r, x: x(r.at), row: -1 }))
    .reverse()
    .map((m) => {
      const row = rowEnds.findIndex((end) => end - m.x > 70);
      if (row >= 0) rowEnds[row] = m.x;
      return { ...m, row };
    });
  const bands = incidents
    .filter((i) => Date.parse(i.opened_at) < windowEnd && (!i.resolved_at || Date.parse(i.resolved_at) > windowStart))
    .map((i) => ({
      id: i.id,
      label: kindLabel[i.kind] ?? i.kind,
      x1: x(Math.max(windowStart, Date.parse(i.opened_at))),
      x2: x(Math.min(windowEnd, i.resolved_at ? Date.parse(i.resolved_at) : windowEnd)),
    }));
  // As many time labels as fit: roughly one per 90 px, between 2 and 5.
  const xCount = Math.max(2, Math.min(5, Math.floor(plotW / 90) + 1));
  const xTicks = Array.from({ length: xCount }, (_, k) => windowStart + ((windowEnd - windowStart) * k) / (xCount - 1));

  const nearest = (px: number) => {
    let best: number | null = null;
    valid.forEach((p, i) => {
      if (best === null || Math.abs(p.x - px) < Math.abs(valid[best].x - px)) best = i;
    });
    return best;
  };
  const point = active === null ? null : valid[active];
  const tooltipLeft = point ? Math.min(Math.max(point.x - 90, 0), Math.max(0, width - 180)) : 0;

  return (
    <section className="card chart">
      <header className="card-head">
        <div>
          <h3>{title}</h3>
          <p className="subtle">{subtitle}</p>
        </div>
      </header>
      <div className="chart-plot" ref={ref}>
        {width > 0 && (
          <svg
            width={width}
            height={HEIGHT}
            role="img"
            tabIndex={0}
            aria-label={`${title}. ${valid.length} measured intervals. Use the arrow keys to read values.`}
            onPointerMove={(e) => {
              const box = e.currentTarget.getBoundingClientRect();
              setActive(nearest(e.clientX - box.left));
            }}
            onPointerLeave={() => setActive(null)}
            onBlur={() => setActive(null)}
            onKeyDown={(e) => {
              if (!valid.length) return;
              if (e.key === "ArrowLeft" || e.key === "ArrowRight") {
                e.preventDefault();
                const step = e.key === "ArrowLeft" ? -1 : 1;
                setActive((a) => Math.min(valid.length - 1, Math.max(0, (a ?? valid.length) + step)));
              }
            }}
          >
            {bands.map((b) => (
              <rect key={b.id} className="band" x={b.x1} y={M.top} width={Math.max(2, b.x2 - b.x1)} height={plotH}>
                <title>{b.label}</title>
              </rect>
            ))}
            {yTicks.map((t) => (
              <g key={t}>
                <line className={t === 0 ? "axis" : "grid"} x1={M.left} x2={M.left + plotW} y1={y(t)} y2={y(t)} />
                <text className="tick" x={M.left - 8} y={y(t)} dy="0.32em" textAnchor="end">
                  {tickLabel(t)}
                </text>
              </g>
            ))}
            {xTicks.map((t, k) => (
              <text
                key={t}
                className="tick"
                x={x(t)}
                y={HEIGHT - 8}
                textAnchor={k === 0 ? "start" : k === xCount - 1 ? "end" : "middle"}
              >
                {new Date(t).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })}
              </text>
            ))}
            {/* All marker lines first, then all labels, so no line paints over a label. */}
            <g className="release">
              {markers.map((m) => (
                <line key={m.at} x1={m.x} x2={m.x} y1={m.row === 0 ? M.top - 18 : M.top - 4} y2={M.top + plotH} />
              ))}
              {markers
                .filter((m) => m.row >= 0)
                .map((m) => {
                  // A surface-colored backing keeps a crossing marker line out of the label.
                  const flip = m.x > M.left + plotW - 70;
                  const width = m.version.length * 7 + 6;
                  const ty = m.row === 0 ? M.top - 24 : M.top - 8;
                  return (
                    <g key={m.at}>
                      <rect className="label-bg" x={flip ? m.x - 1 - width : m.x + 1} y={ty - 11} width={width} height={15} />
                      <text x={flip ? m.x - 4 : m.x + 4} y={ty} textAnchor={flip ? "end" : "start"}>
                        {m.version}
                      </text>
                    </g>
                  );
                })}
            </g>
            <path className="series" d={path} />
            {point && (
              <g className="crosshair">
                <line x1={point.x} x2={point.x} y1={M.top} y2={M.top + plotH} />
                <circle cx={point.x} cy={point.y} r={5} />
              </g>
            )}
          </svg>
        )}
        {point && (
          <div className="tooltip" style={{ left: tooltipLeft }} aria-hidden="true">
            <strong>{format(point.value)}</strong>
            <span>
              {clock(point.interval.at)} · {point.interval.version}
            </span>
            <span>{number(point.interval.requests)} requests</span>
          </div>
        )}
        {width > 0 && valid.length === 0 && <p className="chart-empty">No measured traffic in this window.</p>}
      </div>
      <p className="chart-key subtle">
        <span className="key-release" aria-hidden="true" /> Release change
        <span className="key-band" aria-hidden="true" /> Incident
        <span className="key-note">Gaps: restarts, idle or missing intervals</span>
      </p>
      <details className="data-table">
        <summary>View as table</summary>
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th>Interval end</th>
                <th>Version</th>
                <th className="num">Requests</th>
                <th className="num">{field === "latency" ? "Mean latency" : "Error rate"}</th>
              </tr>
            </thead>
            <tbody>
              {valid
                .slice()
                .reverse()
                .map((p) => (
                  <tr key={p.interval.at}>
                    <td>{clock(p.interval.at)}</td>
                    <td>{p.interval.version}</td>
                    <td className="num">{number(p.interval.requests)}</td>
                    <td className="num">{format(p.value)}</td>
                  </tr>
                ))}
            </tbody>
          </table>
        </div>
      </details>
    </section>
  );
}
