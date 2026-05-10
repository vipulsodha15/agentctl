import { useEffect, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { ApiError, apiJson } from "../api";
import type { ListSessionsResponse, SessionRow } from "../types";

const POLL_INTERVAL_MS = 5000;

export function SessionList() {
  const [rows, setRows] = useState<SessionRow[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const navigate = useNavigate();

  useEffect(() => {
    let cancelled = false;
    let timer: number | null = null;

    async function load() {
      try {
        const r = await apiJson<ListSessionsResponse>("/v1/sessions");
        if (!cancelled) {
          setRows(r.sessions ?? []);
          setError(null);
        }
      } catch (err) {
        if (!cancelled) {
          const msg =
            err instanceof ApiError
              ? `${err.code ?? err.status}: ${err.message}`
              : String(err);
          setError(msg);
        }
      }
    }

    function tick() {
      load().finally(() => {
        if (!cancelled) timer = window.setTimeout(tick, POLL_INTERVAL_MS);
      });
    }

    tick();
    const onFocus = () => load();
    window.addEventListener("focus", onFocus);
    return () => {
      cancelled = true;
      window.removeEventListener("focus", onFocus);
      if (timer !== null) window.clearTimeout(timer);
    };
  }, []);

  return (
    <section>
      <div className="toolbar">
        <h2 style={{ margin: 0 }}>Sessions</h2>
        <Link to="/new">
          <button className="primary">New session</button>
        </Link>
      </div>
      {error && <div className="error-text">{error}</div>}
      {rows === null && !error && <div className="empty">Loading…</div>}
      {rows && rows.length === 0 && (
        <div className="empty">
          No sessions yet. Start one from the CLI or click "New session".
        </div>
      )}
      {rows && rows.length > 0 && (
        <table className="session-table">
          <thead>
            <tr>
              <th>ID</th>
              <th>Name</th>
              <th>Status</th>
              <th>Last activity</th>
              <th>Image ID</th>
              <th>Cost</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr
                key={row.session_id}
                className="row-link"
                onClick={() => navigate(`/sessions/${row.session_id}`)}
              >
                <td className="id-cell">{shortId(row.session_id)}</td>
                <td>{row.name || "—"}</td>
                <td>
                  <span className={`status-badge ${row.status}`}>
                    {row.status}
                  </span>
                </td>
                <td>{formatRelative(row.last_activity_at)}</td>
                <td className="id-cell">{shortImage(row.image_id)}</td>
                <td>{formatCost(row.cost_usd)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}

function shortId(id: string): string {
  if (!id) return "";
  return id.length <= 8 ? id : id.slice(-8);
}

function shortImage(image: string | undefined | null): string {
  if (!image) return "—";
  // Accept "sha256:abcd…" or already-short forms.
  const stripped = image.replace(/^sha256:/, "");
  return stripped.slice(0, 8);
}

function formatCost(cost: number | null | undefined): string {
  if (cost === null || cost === undefined) return "—";
  return `$${cost.toFixed(2)}`;
}

function formatRelative(ts: string | undefined): string {
  if (!ts) return "—";
  const t = Date.parse(ts);
  if (Number.isNaN(t)) return ts;
  const delta = Date.now() - t;
  const s = Math.round(delta / 1000);
  if (s < 60) return `${s}s ago`;
  const m = Math.round(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.round(m / 60);
  if (h < 24) return `${h}h ago`;
  const d = Math.round(h / 24);
  return `${d}d ago`;
}
