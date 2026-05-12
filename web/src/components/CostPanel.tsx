import { useEffect, useState } from "react";
import { ApiError, apiJson } from "../api";
import type { SessionCostTotals } from "../types";

interface Props {
  sessionId: string;
  // Bumping this triggers a refetch — SessionDetail nudges it on turn.end
  // and session.stopped to keep the panel reflecting the latest usage rows.
  refreshKey?: number;
}

export function CostPanel({ sessionId, refreshKey }: Props) {
  const [data, setData] = useState<SessionCostTotals | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!sessionId) return;
    let cancelled = false;
    apiJson<SessionCostTotals | { per_session: SessionCostTotals }>(
      `/v1/usage?session_id=${encodeURIComponent(sessionId)}`,
    )
      .then((r) => {
        if (cancelled) return;
        const body = unwrap(r);
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
      });
    return () => {
      cancelled = true;
    };
  }, [sessionId, refreshKey]);

  return (
    <div className="panel">
      <h3>Cost</h3>
      {error && <div className="error-text">{error}</div>}
      {!error && !data && <div className="empty">Loading…</div>}
      {data && (
        <div>
          <div className="cost-amount">
            {formatUSD(data.cost_usd)}
            {data.has_unknown_model && (
              <span
                title="Some turns used a model not in the price table."
                style={{ marginLeft: 6, color: "var(--c-warn)", fontSize: 18 }}
              >
                *
              </span>
            )}
          </div>
          <div className="cost-meta">
            {data.turns} {data.turns === 1 ? "turn" : "turns"} ·{" "}
            {compact(data.input_tokens)} in / {compact(data.output_tokens)} out
          </div>

          {data.by_model && data.by_model.length > 0 && (
            <table className="cost-mini" style={{ marginTop: 10 }}>
              <thead>
                <tr>
                  <th>Model</th>
                  <th>Turns</th>
                  <th>Cost</th>
                </tr>
              </thead>
              <tbody>
                {data.by_model.map((m) => (
                  <tr key={m.model}>
                    <td className="id-cell">{m.model}</td>
                    <td>{m.turns}</td>
                    <td>{formatUSD(m.cost_usd)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}

          {data.timeline && data.timeline.length > 0 && (
            <details style={{ marginTop: 10 }}>
              <summary style={{ cursor: "pointer", fontSize: 12 }}>
                Last {Math.min(20, data.timeline.length)} turns
              </summary>
              <table className="cost-mini" style={{ marginTop: 6 }}>
                <thead>
                  <tr>
                    <th>At</th>
                    <th>In</th>
                    <th>Out</th>
                    <th>Cost</th>
                  </tr>
                </thead>
                <tbody>
                  {data.timeline.slice(-20).map((t) => (
                    <tr key={t.turn_id}>
                      <td>{shortTime(t.at)}</td>
                      <td>{compact(t.input_tokens)}</td>
                      <td>{compact(t.output_tokens)}</td>
                      <td>
                        {t.cost_usd === null || t.cost_usd === undefined
                          ? "—"
                          : formatUSD(t.cost_usd)}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </details>
          )}
        </div>
      )}
    </div>
  );
}

function unwrap(
  r: SessionCostTotals | { per_session: SessionCostTotals },
): SessionCostTotals {
  if ((r as { per_session?: SessionCostTotals }).per_session) {
    return (r as { per_session: SessionCostTotals }).per_session;
  }
  return r as SessionCostTotals;
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

function shortTime(ts: string | undefined): string {
  if (!ts) return "—";
  const t = Date.parse(ts);
  if (Number.isNaN(t)) return ts;
  const d = new Date(t);
  return d.toISOString().slice(11, 19);
}
