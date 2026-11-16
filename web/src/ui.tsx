import React, { useEffect, useId, useState } from "react";
import { FleetNode, Status, get } from "./api";
import { href } from "./route";

// Small inline icons (24×24 grid, 2px strokes) so status never relies on color.
const paths: Record<string, React.ReactNode> = {
  check: <path d="M5 12.5l4.5 4.5L19 7.5" />,
  alert: (
    <>
      <path d="M12 3.5L2.5 20h19L12 3.5z" />
      <path d="M12 10v4.5M12 17.5v.01" />
    </>
  ),
  off: (
    <>
      <circle cx="12" cy="12" r="8.5" />
      <path d="M6 6l12 12" />
    </>
  ),
  unknown: (
    <>
      <circle cx="12" cy="12" r="8.5" />
      <path d="M9.5 9.5a2.5 2.5 0 114 2c-1 .6-1.5 1.2-1.5 2.2M12 17v.01" />
    </>
  ),
  search: (
    <>
      <circle cx="10.5" cy="10.5" r="6" />
      <path d="M15 15l5 5" />
    </>
  ),
  arrow: <path d="M5 12h14M13 6l6 6-6 6" />,
  back: <path d="M19 12H5M11 6l-6 6 6 6" />,
};
export function Icon({ name, size = 16 }: { name: keyof typeof paths; size?: number }) {
  return (
    <svg className="icon" width={size} height={size} viewBox="0 0 24 24" aria-hidden="true">
      {paths[name]}
    </svg>
  );
}

const statusMeta: Record<Status, { label: string; icon: keyof typeof paths; hint: string }> = {
  reporting: { label: "Reporting", icon: "check", hint: "Recent heartbeat and a healthy application probe" },
  incident: { label: "Incident", icon: "alert", hint: "At least one open incident" },
  unreachable: { label: "Unreachable", icon: "off", hint: "No heartbeat for more than 15 seconds" },
  unknown: { label: "No heartbeat", icon: "unknown", hint: "Telemetry only, or no healthy probe yet" },
};
export function StatusBadge({ status }: { status: Status }) {
  const m = statusMeta[status] ?? statusMeta.unknown;
  return (
    <span className={`status status-${status}`} title={m.hint}>
      <Icon name={m.icon} size={14} />
      {m.label}
    </span>
  );
}

export function Tile({
  label,
  value,
  detail,
  tone,
  onClick,
  pressed,
}: {
  label: string;
  value: React.ReactNode;
  detail?: React.ReactNode;
  tone?: "critical" | "good";
  onClick?: () => void;
  pressed?: boolean;
}) {
  const body = (
    <>
      <span className="tile-label">{label}</span>
      <span className="tile-value">{value}</span>
      {detail && <span className="tile-detail">{detail}</span>}
    </>
  );
  const className = `tile${tone ? " tile-" + tone : ""}`;
  return onClick ? (
    <button type="button" className={className + " tile-button"} onClick={onClick} aria-pressed={pressed}>
      {body}
    </button>
  ) : (
    <div className={className}>{body}</div>
  );
}

export function Segmented<T extends string | number>({
  label,
  options,
  value,
  onChange,
}: {
  label: string;
  options: { value: T; label: string }[];
  value: T;
  onChange: (v: T) => void;
}) {
  return (
    <div className="segmented" role="group" aria-label={label}>
      {options.map((o) => (
        <button
          type="button"
          key={String(o.value)}
          aria-pressed={o.value === value}
          onClick={() => onChange(o.value)}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

// Type-ahead node search backed by the fleet API's `q` filter, so any node in a
// large fleet is two keystrokes away. Enter or picking a suggestion navigates.
export function NodeSearch({
  current,
  view = "node",
  placeholder = "Find a node…",
}: {
  current?: string;
  view?: "node" | "compare";
  placeholder?: string;
}) {
  const [text, setText] = useState("");
  const [matches, setMatches] = useState<FleetNode[]>([]);
  const listId = useId();
  useEffect(() => {
    if (!text.trim()) return setMatches([]);
    const controller = new AbortController();
    const timer = setTimeout(() => {
      get<FleetNode[]>("fleet?q=" + encodeURIComponent(text.trim()), controller.signal)
        .then((nodes) => setMatches(nodes.slice(0, 20)))
        .catch(() => setMatches([]));
    }, 200);
    return () => {
      clearTimeout(timer);
      controller.abort();
    };
  }, [text]);
  const go = (id: string) => {
    if (!id) return;
    location.hash = href(view, id);
    setText("");
  };
  return (
    <form
      className="search"
      role="search"
      onSubmit={(e) => {
        e.preventDefault();
        go(matches.find((m) => m.id === text)?.id ?? matches[0]?.id ?? "");
      }}
    >
      <Icon name="search" />
      <input
        type="search"
        aria-label={current ? `Switch from ${current} to another node` : "Find a node"}
        placeholder={placeholder}
        list={listId}
        value={text}
        onChange={(e) => {
          setText(e.target.value);
          // Picking a suggestion arrives as a replacement (or untyped) input; typing does not.
          const type = (e.nativeEvent as InputEvent).inputType;
          const picked = !type || type === "insertReplacementText";
          if (picked && matches.some((m) => m.id === e.target.value)) go(e.target.value);
        }}
      />
      <datalist id={listId}>
        {matches.map((m) => (
          <option key={m.id} value={m.id} />
        ))}
      </datalist>
    </form>
  );
}

export function Notice({ tone = "warning", children }: { tone?: "warning" | "critical" | "info"; children: React.ReactNode }) {
  return (
    <div className={`notice notice-${tone}`} role={tone === "info" ? "status" : "alert"}>
      <Icon name={tone === "info" ? "unknown" : "alert"} />
      <div>{children}</div>
    </div>
  );
}
