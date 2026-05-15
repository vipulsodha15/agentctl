import { useCallback, useEffect, useState } from "react";
import { Link, useLocation, useNavigate } from "react-router-dom";
import { ApiError, api, apiJson } from "../api";
import { ConfirmModal } from "../components/ConfirmModal";
import type {
  Agent,
  ListAgentsResponse,
  ListAssemblyLinesResponse,
  AssemblyLine,
} from "../types";

export function AssemblyLineList() {
  const navigate = useNavigate();
  const location = useLocation();
  const [assemblyLines, setAssemblyLines] = useState<AssemblyLine[]>([]);
  const [agents, setAgents] = useState<Record<string, Agent>>({});
  const [selected, setSelected] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [pendingDelete, setPendingDelete] = useState<string | null>(null);

  const load = useCallback(
    (preferName?: string | null) => {
      Promise.all([
        apiJson<ListAssemblyLinesResponse>("/v1/assembly-lines"),
        apiJson<ListAgentsResponse>("/v1/agents"),
      ])
        .then(([w, a]) => {
          const list = w.assembly_lines ?? [];
          setAssemblyLines(list);
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

  const wf = assemblyLines.find((w) => w.name === selected);

  async function confirmDelete() {
    const name = pendingDelete;
    if (!name) return;
    setBusy(true);
    setError(null);
    try {
      const res = await api(`/v1/assembly-lines/${encodeURIComponent(name)}`, {
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
          <h2>Assembly lines</h2>
          <div className="muted" style={{ marginTop: 4 }}>
            Ordered chains of role-distinct agents. Each task runs one assembly line.
          </div>
        </div>
        <Link to="/assembly-lines/new" className="button-link primary">
          + New assembly line
        </Link>
      </div>
      {error && <div className="error-text">{error}</div>}
      <div className="two-pane">
        <div className="two-pane-list">
          {assemblyLines.length === 0 && (
            <div className="empty" style={{ padding: 24 }}>
              No assembly lines yet.{" "}
              <Link to="/assembly-lines/new">Create the first one.</Link>
            </div>
          )}
          {assemblyLines.map((w) => (
            <button
              key={w.name}
              className={`list-item${w.name === selected ? " active" : ""}`}
              onClick={() => setSelected(w.name)}
            >
              <div className="list-item-body">
                <div className="list-item-title">{w.name}</div>
                <div className="list-item-sub muted">{w.description}</div>
              </div>
              <div className="assembly-line-stagecount">
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
            <AssemblyLineDetail
              assemblyLine={wf}
              agents={agents}
              busy={busy}
              onEdit={() =>
                navigate(`/assembly-lines/${encodeURIComponent(wf.name)}/edit`)
              }
              onDuplicate={() =>
                navigate(`/assembly-lines/new?from=${encodeURIComponent(wf.name)}`)
              }
              onDelete={() => setPendingDelete(wf.name)}
            />
          ) : (
            <div className="empty">
              Select an assembly line to inspect, or{" "}
              <Link to="/assembly-lines/new">create a new one</Link>.
            </div>
          )}
        </div>
      </div>
      <ConfirmModal
        open={pendingDelete !== null}
        title={`Delete assembly line "${pendingDelete ?? ""}"?`}
        message="Tasks already in flight keep their stages, but new tasks cannot use it."
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
  assemblyLine: AssemblyLine;
  agents: Record<string, Agent>;
  busy: boolean;
  onEdit: () => void;
  onDuplicate: () => void;
  onDelete: () => void;
}

function AssemblyLineDetail({
  assemblyLine,
  agents,
  busy,
  onEdit,
  onDuplicate,
  onDelete,
}: DetailProps) {
  const isBuiltin = assemblyLine.source === "builtin";
  return (
    <div>
      <div className="agent-header assembly-line-detail-head">
        <div style={{ flex: 1, minWidth: 0 }}>
          <div className="agent-name">{assemblyLine.name}</div>
          <div className="muted">{assemblyLine.description}</div>
        </div>
        {isBuiltin && <span className="badge-builtin">builtin</span>}
        <div className="assembly-line-detail-actions">
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
      <div className="assembly-line-stages">
        {assemblyLine.stages.map((s, idx) => {
          const a = agents[s.agent];
          return (
            <div key={idx} className="assembly-line-stage-card">
              <div className="assembly-line-stage-head">
                <span className="assembly-line-stage-num">{idx + 1}</span>
                <span className={`agent-swatch swatch-${a?.colour ?? "slate"}`} />
                <div>
                  <div className="assembly-line-stage-agent">{s.agent}</div>
                  {a ? (
                    <div className="muted assembly-line-stage-desc">
                      {a.description}
                    </div>
                  ) : (
                    <div className="muted assembly-line-stage-desc">
                      Agent no longer defined.
                    </div>
                  )}
                </div>
              </div>
              {idx < assemblyLine.stages.length - 1 && (
                <div className="assembly-line-stage-connector" aria-hidden />
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}
