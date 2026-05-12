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
    <section className="page">
      <div className="page-header">
        <div style={{ flex: 1 }}>
          <h2>Sessions</h2>
          <div className="muted" style={{ marginTop: 4 }}>
            {rows === null
              ? "Loading…"
              : rows.length === 0
                ? "No active sessions"
                : `${rows.length} ${rows.length === 1 ? "session" : "sessions"}`}
          </div>
        </div>
        <Link to="/new">
          <button className="primary">
            <PlusIcon /> New session
          </button>
        </Link>
      </div>
      {error && <div className="error-text">{error}</div>}
      {rows === null && !error && (
        <div className="panel" style={{ textAlign: "center", padding: "48px 24px" }}>
          <div className="empty" style={{ padding: 0 }}>Loading sessions…</div>
        </div>
      )}
      {rows && rows.length === 0 && (
        <div
          className="panel"
          style={{ textAlign: "center", padding: "72px 24px" }}
        >
          <div
            aria-hidden
            style={{
              width: 44,
              height: 44,
              margin: "0 auto 18px",
              borderRadius: 12,
              display: "grid",
              placeItems: "center",
              background: "var(--c-surface-2)",
              color: "var(--c-fg-mute)",
              border: "1px solid var(--c-border)",
            }}
          >
            <svg
              width="20"
              height="20"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              strokeWidth="1.5"
              strokeLinecap="round"
              strokeLinejoin="round"
            >
              <path d="M4 6.5h13M4 12h13M4 17.5h9" />
              <circle cx="19.5" cy="6.5" r="1" fill="currentColor" stroke="none" />
              <circle cx="19.5" cy="12" r="1" fill="currentColor" stroke="none" />
            </svg>
          </div>
          <div style={{ fontWeight: 600, marginBottom: 6, fontSize: 15, letterSpacing: "-0.015em" }}>
            No sessions yet
          </div>
          <div className="empty" style={{ padding: 0, maxWidth: 380, margin: "0 auto", lineHeight: 1.55 }}>
            Start one from the CLI with{" "}
            <code style={{ fontFamily: "var(--font-mono)", background: "var(--c-surface-2)", padding: "1.5px 6px", borderRadius: 4, border: "1px solid var(--c-border)", fontSize: 12, color: "var(--c-fg)" }}>
              agentctl new
            </code>{" "}
            or click below.
          </div>
          <div style={{ marginTop: 20 }}>
            <Link to="/new">
              <button className="primary">
                <PlusIcon /> Create your first session
              </button>
            </Link>
          </div>
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
                <td style={{ fontWeight: 500 }}>{row.name || "—"}</td>
                <td>
                  <span className={`status-badge ${row.status}`}>
                    {row.status}
                  </span>
                </td>
                <td style={{ color: "var(--c-fg-mute)" }}>{formatRelative(row.last_activity_at)}</td>
                <td className="id-cell">{shortImage(row.image_id)}</td>
                <td style={{ fontFamily: "var(--font-mono)", fontSize: 12.5, fontFeatureSettings: "'tnum'" }}>{formatCost(row.cost_usd)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}

function PlusIcon() {
  return (
    <svg
      width="13"
      height="13"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2.2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
      style={{ marginRight: 5, marginLeft: -2 }}
    >
      <path d="M12 5v14M5 12h14" />
    </svg>
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
