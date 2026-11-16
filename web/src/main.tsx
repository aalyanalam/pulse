import React, { useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import { CompareView } from "./CompareView";
import { FleetView } from "./FleetView";
import { NodeView } from "./NodeView";
import { clock, onHealth } from "./api";
import { href, useRoute } from "./route";
import "./style.css";

// One indicator for the whole app: live while requests succeed, otherwise
// "reconnecting" with the time of the last successful update.
function Live() {
  const [health, setHealth] = useState({ ok: true, at: 0 });
  useEffect(() => onHealth(setHealth), []);
  const label = !health.at ? "Connecting…" : health.ok ? "Live" : "Reconnecting…";
  return (
    <span className={`live ${health.ok ? "live-ok" : "live-down"}`} role="status">
      <span className="live-dot" aria-hidden="true" />
      {label}
      {health.at > 0 && <span className="live-time"> · updated {clock(health.at)}</span>}
    </span>
  );
}

function App() {
  const route = useRoute();
  const [lastNode, setLastNode] = useState(route.node);
  useEffect(() => {
    if (route.node) setLastNode(route.node);
  }, [route.node]);
  useEffect(() => {
    document.title =
      route.view === "fleet" ? "Fleet · Pulse" : `${route.node || "Compare"} · ${route.view === "node" ? "Investigate" : "Compare"} · Pulse`;
    window.scrollTo(0, 0);
  }, [route.view, route.node]);

  const nav = [
    { label: "Fleet", to: href("fleet"), active: route.view === "fleet" },
    { label: "Investigate", to: href("node", lastNode), active: route.view === "node", disabled: !lastNode },
    { label: "Compare releases", to: href("compare", lastNode), active: route.view === "compare" },
  ];

  return (
    <>
      <a className="skip" href="#content">
        Skip to content
      </a>
      <header className="topbar">
        <div className="topbar-inner">
          <a className="brand" href={href("fleet")} aria-label="Pulse fleet overview">
            <svg width="22" height="22" viewBox="0 0 24 24" aria-hidden="true">
              <path d="M2 12h4l3-7 4 14 3-7h6" />
            </svg>
            pulse
          </a>
          <span className="env">Local simulation</span>
          <nav aria-label="Main">
            {nav.map((n) =>
              n.disabled ? (
                <span key={n.label} className="nav-link disabled" title="Open a node from the fleet first">
                  {n.label}
                </span>
              ) : (
                <a key={n.label} className="nav-link" href={n.to} aria-current={n.active ? "page" : undefined}>
                  {n.label}
                </a>
              ),
            )}
          </nav>
          <Live />
        </div>
      </header>
      <main id="content">
        {route.view === "fleet" ? (
          <FleetView />
        ) : route.view === "node" && route.node ? (
          <NodeView key={route.node} node={route.node} initialMinutes={Number(route.params.get("minutes"))} />
        ) : route.view === "compare" ? (
          <CompareView key={route.node + "?" + route.params.toString()} route={route} />
        ) : (
          <FleetView />
        )}
      </main>
      <footer className="page-foot">
        Pulse · telemetry and release diagnostics for a simulated fleet.{" "}
        <span>Status from heartbeats · comparisons show correlation, not causation.</span>
      </footer>
    </>
  );
}

createRoot(document.getElementById("root")!).render(<App />);
