import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { ApiError, apiJson } from "../api";
import type { RangeCostTotals } from "../types";

const PRESETS: { label: string; value: string }[] = [
  { label: "Today", value: "today" },
  { label: "Last 7 days", value: "7d" },
  { label: "Last 30 days", value: "30d" },
  { label: "Custom", value: "custom" },
];

export function Usage() {
  const [preset, setPreset] = useState<string>("7d");
  const [start, setStart] = useState<string>("");
  const [end, setEnd] = useState<string>("");
  const [data, setData] = useState<RangeCostTotals | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  const range = useMemo(() => {
    if (preset !== "custom") return preset;
    if (start && end) return `${start}..${end}`;
    return "";
  }, [preset, start, end]);

  useEffect(() => {
    if (!range) return;
    let cancelled = false;
    setLoading(true);
    apiJson<RangeCostTotals | { range: RangeCostTotals }>(
      `/v1/usage?since=${encodeURIComponent(range)}`,
    )
      .then((r) => {
        if (cancelled) return;
        const body = (r as { range?: RangeCostTotals }).range
          ? (r as { range: RangeCostTotals }).range
          : (r as RangeCostTotals);
        setData(body);
        setError(null);
      })
      .catch((err) => {
        if (cancelled) return;
        const msg =
          err instanceof ApiError
            ? `${err.code ?? err.status}: ${err.message}`
            : String(err);
        setError(msg);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [range]);

  return (
    <section className="page">
      <div className="page-header">
        <div style={{ flex: 1 }}>
          <h2>Usage</h2>
          <div className="muted" style={{ marginTop: 4 }}>
            Token consumption and cost over time
          </div>
        </div>
        <div className="filter-bar" style={{ marginLeft: 0 }}>
          {PRESETS.map((p) => (
            <button
              key={p.value}
              className={`filter-btn${preset === p.value ? " active" : ""}`}
              onClick={() => setPreset(p.value)}
            >
              {p.label}
            </button>
          ))}
        </div>
      </div>

      {preset === "custom" && (
        <div style={{ display: "flex", gap: 10, marginBottom: 16, alignItems: "center" }}>
          <input
            type="date"
            value={start}
            onChange={(e) => setStart(e.target.value)}
          />
          <span className="muted">to</span>
          <input
            type="date"
            value={end}
            onChange={(e) => setEnd(e.target.value)}
          />
        </div>
      )}

      {error && <div className="error-text">{error}</div>}
      {loading && !data && (
        <div className="panel" style={{ textAlign: "center", padding: "48px 24px" }}>
          <div className="empty" style={{ padding: 0 }}>Loading usage…</div>
        </div>
      )}

      {data && (
        <div>
          <div className="panel" style={{ marginBottom: 18, padding: "20px 22px" }}>
            <h3>Total spend</h3>
            <div className="cost-amount" style={{ fontSize: 36, marginTop: 8 }}>
              {formatUSD(data.cost_usd)}
              {data.has_unknown_model && (
                <span
                  title="Some turns used a model not in the price table."
                  style={{ marginLeft: 8, color: "var(--c-warn-fg)", fontSize: 20 }}
                >
                  *
                </span>
              )}
            </div>
            <div className="cost-meta">
              {data.turns} {data.turns === 1 ? "turn" : "turns"} ·{" "}
              {compact(data.input_tokens)} in / {compact(data.output_tokens)} out ·{" "}
              {humanRange(data.start, data.end)}
            </div>
          </div>

          {data.by_session && data.by_session.length === 0 && (
            <div className="empty">No usage rows in this range.</div>
          )}
          {data.by_session && data.by_session.length > 0 && (
            <table className="session-table">
              <thead>
                <tr>
                  <th>Session</th>
                  <th>Name</th>
                  <th>Status</th>
                  <th>Turns</th>
                  <th>In tokens</th>
                  <th>Out tokens</th>
                  <th>Cost</th>
                </tr>
              </thead>
              <tbody>
                {data.by_session.map((s) => (
                  <tr key={s.session_id}>
                    <td className="id-cell">
                      <Link to={`/sessions/${s.session_id}`}>
                        {short(s.session_id)}
                      </Link>
                    </td>
                    <td>{s.name || "—"}</td>
                    <td>
                      {s.status ? (
                        <span className={`status-badge ${s.status}`}>
                          {s.status}
                        </span>
                      ) : (
                        "—"
                      )}
                    </td>
                    <td>{s.turns}</td>
                    <td>{compact(s.input_tokens)}</td>
                    <td>{compact(s.output_tokens)}</td>
                    <td>
                      {formatUSD(s.cost_usd)}
                      {s.has_unknown_model && "*"}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      )}
    </section>
  );
}

function formatUSD(v: number | null | undefined): string {
  if (v === null || v === undefined) return "—";
  return `$${v.toFixed(2)}`;
}

function compact(n: number | null | undefined): string {
  if (n === null || n === undefined) return "0";
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + "M";
  if (n >= 1_000) return (n / 1_000).toFixed(1) + "k";
  return String(n);
}

function short(id: string): string {
  if (!id) return "";
  return id.length <= 12 ? id : id.slice(-12);
}

function humanRange(start: string, end: string): string {
  if (!start || !end) return "";
  return `${start.slice(0, 10)} → ${end.slice(0, 10)}`;
}
