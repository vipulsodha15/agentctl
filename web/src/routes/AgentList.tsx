import { useCallback, useEffect, useState } from "react";
import { Link, useLocation, useNavigate } from "react-router-dom";
import { ApiError, api, apiJson } from "../api";
import { ConfirmModal } from "../components/ConfirmModal";
import type { Agent, ListAgentsResponse } from "../types";

export function AgentList() {
  const navigate = useNavigate();
  const location = useLocation();
  const [agents, setAgents] = useState<Agent[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [pendingDelete, setPendingDelete] = useState<string | null>(null);

  const load = useCallback((preferName?: string | null) => {
    apiJson<ListAgentsResponse>("/v1/agents")
      .then((r) => {
        const list = r.agents ?? [];
        setAgents(list);
        setSelected((prev) => {
          const want = preferName ?? prev;
          if (want && list.some((x) => x.name === want)) return want;
          return list.length > 0 ? list[0].name : null;
        });
      })
      .catch((err) =>
        setError(
          err instanceof ApiError
            ? `${err.code ?? err.status}: ${err.message}`
            : String(err),
        ),
      );
  }, []);

  useEffect(() => {
    const seed = (location.state as { selected?: string } | null)?.selected;
    load(seed);
    if (seed) {
      navigate(location.pathname, { replace: true, state: null });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const agent = agents.find((a) => a.name === selected);

  async function confirmDelete() {
    const name = pendingDelete;
    if (!name) return;
    setBusy(true);
    setError(null);
    try {
      const res = await api(`/v1/agents/${encodeURIComponent(name)}`, {
        method: "DELETE",
      });
      if (!res.ok && res.status !== 204) {
        let msg = res.statusText;
        try {
          const body = await res.json();
          const data = (body && (body.data ?? body)) as
            | { code?: string; message?: string }
            | undefined;
          if (data?.message) msg = data.message;
        } catch {
          // ignore
        }
        throw new Error(msg);
      }
      load(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
      setPendingDelete(null);
    }
  }

  return (
    <section className="page">
      <div className="page-header">
        <div style={{ flex: 1 }}>
          <h2>Agents</h2>
          <div className="muted" style={{ marginTop: 4 }}>
            Reusable session templates: prompt, MCPs, skills, model, colour.
          </div>
        </div>
        <Link to="/agents/new" className="button-link primary">
          + New agent
        </Link>
      </div>
      {error && <div className="error-text">{error}</div>}
      <div className="two-pane">
        <div className="two-pane-list">
          {agents.length === 0 && (
            <div className="empty" style={{ padding: 24 }}>
              No agents yet.{" "}
              <Link to="/agents/new">Create the first one.</Link>
            </div>
          )}
          {agents.map((a) => (
            <button
              key={a.name}
              className={`list-item${a.name === selected ? " active" : ""}`}
              onClick={() => setSelected(a.name)}
            >
              <span className={`agent-swatch swatch-${a.colour ?? "slate"}`} />
              <div className="list-item-body">
                <div className="list-item-title">{a.name}</div>
                <div className="list-item-sub muted">{a.description}</div>
              </div>
              {a.source === "builtin" && (
                <span className="badge-builtin">builtin</span>
              )}
            </button>
          ))}
        </div>
        <div className="two-pane-detail panel">
          {agent ? (
            <AgentDetail
              agent={agent}
              busy={busy}
              onEdit={() =>
                navigate(`/agents/${encodeURIComponent(agent.name)}/edit`)
              }
              onDuplicate={() =>
                navigate(`/agents/new?from=${encodeURIComponent(agent.name)}`)
              }
              onDelete={() => setPendingDelete(agent.name)}
            />
          ) : (
            <div className="empty">
              Select an agent to inspect, or{" "}
              <Link to="/agents/new">create a new one</Link>.
            </div>
          )}
        </div>
      </div>
      <ConfirmModal
        open={pendingDelete !== null}
        title={`Delete agent "${pendingDelete ?? ""}"?`}
        message="Assembly lines that reference it will block the delete."
        confirmLabel="Delete"
        variant="danger"
        busy={busy}
        onConfirm={confirmDelete}
        onCancel={() => setPendingDelete(null)}
      />
    </section>
  );
}

interface DetailProps {
  agent: Agent;
  busy: boolean;
  onEdit: () => void;
  onDuplicate: () => void;
  onDelete: () => void;
}

function AgentDetail({ agent, busy, onEdit, onDuplicate, onDelete }: DetailProps) {
  const isBuiltin = agent.source === "builtin";
  return (
    <div>
      <div className="agent-header assembly-line-detail-head">
        <span className={`agent-swatch large swatch-${agent.colour ?? "slate"}`} />
        <div style={{ flex: 1, minWidth: 0 }}>
          <div className="agent-name">{agent.name}</div>
          <div className="muted">{agent.description}</div>
        </div>
        {isBuiltin && <span className="badge-builtin">builtin</span>}
        <div className="assembly-line-detail-actions">
          {isBuiltin ? (
            <button type="button" onClick={onDuplicate} disabled={busy}>
              Duplicate
            </button>
          ) : (
            <>
              <button type="button" onClick={onDuplicate} disabled={busy}>
                Duplicate
              </button>
              <button type="button" onClick={onEdit} disabled={busy}>
                Edit
              </button>
              <button
                type="button"
                className="danger"
                onClick={onDelete}
                disabled={busy}
              >
                Delete
              </button>
            </>
          )}
        </div>
      </div>
      <dl className="detail-grid">
        <div>
          <dt>Colour</dt>
          <dd>{agent.colour ?? "slate"}</dd>
        </div>
        <div>
          <dt>Model</dt>
          <dd>{agent.model || <span className="muted">inherit</span>}</dd>
        </div>
        <div>
          <dt>MCPs allowed</dt>
          <dd>
            {agent.mcps_allowed && agent.mcps_allowed.length > 0 ? (
              <span className="chip-row">
                {agent.mcps_allowed.map((m) => (
                  <span key={m} className="chip">{m}</span>
                ))}
              </span>
            ) : (
              <span className="muted">all (no allowlist)</span>
            )}
          </dd>
        </div>
        <div>
          <dt>Skills allowed</dt>
          <dd>
            {agent.skills_allowed && agent.skills_allowed.length > 0 ? (
              <span className="chip-row">
                {agent.skills_allowed.map((s) => (
                  <span key={s} className="chip">{s}</span>
                ))}
              </span>
            ) : (
              <span className="muted">all (no allowlist)</span>
            )}
          </dd>
        </div>
      </dl>
      <div className="section-label" style={{ marginTop: 24 }}>Prompt</div>
      <pre className="prompt-block">{agent.prompt}</pre>
    </div>
  );
}
