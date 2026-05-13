import { useCallback, useEffect, useState } from "react";
import { Link, useLocation, useNavigate } from "react-router-dom";
import { ApiError, api, apiJson } from "../api";
import type {
  Agent,
  ListAgentsResponse,
  ListWorkflowsResponse,
  Workflow,
} from "../types";

export function WorkflowList() {
  const navigate = useNavigate();
  const location = useLocation();
  const [workflows, setWorkflows] = useState<Workflow[]>([]);
  const [agents, setAgents] = useState<Record<string, Agent>>({});
  const [selected, setSelected] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(
    (preferName?: string | null) => {
      Promise.all([
        apiJson<ListWorkflowsResponse>("/v1/workflows"),
        apiJson<ListAgentsResponse>("/v1/agents"),
      ])
        .then(([w, a]) => {
          const list = w.workflows ?? [];
          setWorkflows(list);
          const map: Record<string, Agent> = {};
          for (const ag of a.agents ?? []) map[ag.name] = ag;
          setAgents(map);
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
    },
    [],
  );

  useEffect(() => {
    const seed = (location.state as { selected?: string } | null)?.selected;
    load(seed);
    // Drop the navigation state once we've consumed it so a manual refresh
    // doesn't keep selecting the same row.
    if (seed) {
      navigate(location.pathname, { replace: true, state: null });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const wf = workflows.find((w) => w.name === selected);

  async function onDelete(name: string) {
    const ok = window.confirm(
      `Delete workflow "${name}"? Tasks already in flight keep their stages, but new tasks cannot use it.`,
    );
    if (!ok) return;
    setBusy(true);
    setError(null);
    try {
      const res = await api(`/v1/workflows/${encodeURIComponent(name)}`, {
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
    }
  }

  return (
    <section className="page">
      <div className="page-header">
        <div style={{ flex: 1 }}>
          <h2>Workflows</h2>
          <div className="muted" style={{ marginTop: 4 }}>
            Ordered chains of role-distinct agents. Each task runs one workflow.
          </div>
        </div>
        <Link to="/workflows/new" className="button-link primary">
          + New workflow
        </Link>
      </div>
      {error && <div className="error-text">{error}</div>}
      <div className="two-pane">
        <div className="two-pane-list">
          {workflows.length === 0 && (
            <div className="empty" style={{ padding: 24 }}>
              No workflows yet.{" "}
              <Link to="/workflows/new">Create the first one.</Link>
            </div>
          )}
          {workflows.map((w) => (
            <button
              key={w.name}
              className={`list-item${w.name === selected ? " active" : ""}`}
              onClick={() => setSelected(w.name)}
            >
              <div className="list-item-body">
                <div className="list-item-title">{w.name}</div>
                <div className="list-item-sub muted">{w.description}</div>
              </div>
              <div className="workflow-stagecount">
                {w.stages.length} stage{w.stages.length === 1 ? "" : "s"}
              </div>
              {w.source === "builtin" && (
                <span className="badge-builtin">builtin</span>
              )}
            </button>
          ))}
        </div>
        <div className="two-pane-detail panel">
          {wf ? (
            <WorkflowDetail
              workflow={wf}
              agents={agents}
              busy={busy}
              onEdit={() =>
                navigate(`/workflows/${encodeURIComponent(wf.name)}/edit`)
              }
              onDuplicate={() =>
                navigate(`/workflows/new?from=${encodeURIComponent(wf.name)}`)
              }
              onDelete={() => onDelete(wf.name)}
            />
          ) : (
            <div className="empty">
              Select a workflow to inspect, or{" "}
              <Link to="/workflows/new">create a new one</Link>.
            </div>
          )}
        </div>
      </div>
    </section>
  );
}

interface DetailProps {
  workflow: Workflow;
  agents: Record<string, Agent>;
  busy: boolean;
  onEdit: () => void;
  onDuplicate: () => void;
  onDelete: () => void;
}

function WorkflowDetail({
  workflow,
  agents,
  busy,
  onEdit,
  onDuplicate,
  onDelete,
}: DetailProps) {
  const isBuiltin = workflow.source === "builtin";
  return (
    <div>
      <div className="agent-header workflow-detail-head">
        <div style={{ flex: 1, minWidth: 0 }}>
          <div className="agent-name">{workflow.name}</div>
          <div className="muted">{workflow.description}</div>
        </div>
        {isBuiltin && <span className="badge-builtin">builtin</span>}
        <div className="workflow-detail-actions">
          {isBuiltin ? (
            <button type="button" onClick={onDuplicate} disabled={busy}>
              Duplicate
            </button>
          ) : (
            <>
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
      <div className="section-label" style={{ marginTop: 24 }}>
        Stages
      </div>
      <div className="workflow-stages">
        {workflow.stages.map((s, idx) => {
          const a = agents[s.agent];
          return (
            <div key={idx} className="workflow-stage-card">
              <div className="workflow-stage-head">
                <span className="workflow-stage-num">{idx + 1}</span>
                <span className={`agent-swatch swatch-${a?.colour ?? "slate"}`} />
                <div>
                  <div className="workflow-stage-agent">{s.agent}</div>
                  {a ? (
                    <div className="muted workflow-stage-desc">
                      {a.description}
                    </div>
                  ) : (
                    <div className="muted workflow-stage-desc">
                      Agent no longer defined.
                    </div>
                  )}
                </div>
              </div>
              {idx < workflow.stages.length - 1 && (
                <div className="workflow-stage-connector" aria-hidden />
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}
