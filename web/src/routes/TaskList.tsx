import { useEffect, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { ApiError, apiJson } from "../api";
import type { ListTasksResponse, Task, TaskStatus } from "../types";

const POLL_INTERVAL_MS = 4000;

export function TaskList() {
  const [rows, setRows] = useState<Task[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const navigate = useNavigate();

  useEffect(() => {
    let cancelled = false;
    let timer: number | null = null;

    async function load() {
      try {
        const r = await apiJson<ListTasksResponse>("/v1/tasks");
        if (!cancelled) {
          setRows(r.tasks ?? []);
          setError(null);
        }
      } catch (err) {
        if (!cancelled) {
          setError(
            err instanceof ApiError
              ? `${err.code ?? err.status}: ${err.message}`
              : String(err),
          );
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
          <h2>Tasks</h2>
          <div className="muted" style={{ marginTop: 4 }}>
            {rows === null
              ? "Loading…"
              : rows.length === 0
                ? "No tasks yet"
                : `${rows.length} task${rows.length === 1 ? "" : "s"}`}
          </div>
        </div>
        <Link to="/tasks/new">
          <button className="primary">+ New task</button>
        </Link>
      </div>
      {error && <div className="error-text">{error}</div>}
      {rows && rows.length === 0 && !error && (
        <div className="panel task-empty">
          <div className="task-empty-art" aria-hidden>
            <span className="task-stage-dot blue" />
            <span className="task-stage-line" />
            <span className="task-stage-dot purple" />
            <span className="task-stage-line" />
            <span className="task-stage-dot green" />
          </div>
          <h3>Start a task to spin up a workflow.</h3>
          <p className="muted">
            A task glues a workflow (an ordered chain of role-distinct agents)
            to an issue and a repo. Begin with the built-in <code>bug</code>{" "}
            workflow or assemble your own.
          </p>
          <div className="task-empty-actions">
            <Link to="/tasks/new">
              <button className="primary">+ New task</button>
            </Link>
            <Link to="/workflows">
              <button>Browse workflows</button>
            </Link>
          </div>
        </div>
      )}
      {rows && rows.length > 0 && (
        <div className="task-list">
          {rows.map((t) => (
            <TaskRow key={t.task_id} task={t} onOpen={() => navigate(`/tasks/${t.task_id}`)} />
          ))}
        </div>
      )}
    </section>
  );
}

function TaskRow({ task, onOpen }: { task: Task; onOpen: () => void }) {
  const stages = task.stages ?? [];
  const activeIdx = stages.findIndex((s) => s.status === "active");
  const currentAgent =
    activeIdx >= 0
      ? stages[activeIdx].agent_name
      : stages.length > 0
        ? stages[stages.length - 1].agent_name
        : undefined;
  return (
    <div className="task-row" onClick={onOpen} role="button" tabIndex={0}
      onKeyDown={(e) => { if (e.key === "Enter") onOpen(); }}>
      <div className="task-row-head">
        <div className="task-row-title">
          <span className="task-id">#{task.task_id.slice(-6)}</span>
          <span className="task-name">{task.name}</span>
        </div>
        <StatusBadge status={task.status} />
      </div>
      <div className="task-row-rail">
        {stages.length === 0 && (
          <span className="muted">No workflow attached</span>
        )}
        {stages.map((s, idx) => (
          <span key={s.stage_id} className="task-stage-pill" data-status={s.status} data-colour={s.colour ?? "slate"}>
            <span className="task-stage-pill-dot" />
            <span className="task-stage-pill-name">{s.agent_name}</span>
            {idx < stages.length - 1 && <span className="task-stage-pill-arrow" />}
          </span>
        ))}
      </div>
      <div className="task-row-meta">
        <span className="muted">
          {task.workflow_name ? `workflow: ${task.workflow_name}` : "no workflow"}
        </span>
        {currentAgent && task.status === "working" && (
          <span className="muted">
            talking to: <strong>{currentAgent}</strong>
          </span>
        )}
        <span className="muted">{formatRelative(task.created_at)}</span>
      </div>
    </div>
  );
}

function StatusBadge({ status }: { status: TaskStatus }) {
  return <span className={`status-badge status-${status}`}>{statusLabel(status)}</span>;
}

function statusLabel(s: TaskStatus): string {
  switch (s) {
    case "not-started":
      return "not started";
    case "working":
      return "working";
    case "done":
      return "done";
    case "abandoned":
      return "abandoned";
  }
}

function formatRelative(iso: string): string {
  const t = Date.parse(iso);
  if (!t) return "";
  const diffSec = (Date.now() - t) / 1000;
  if (diffSec < 60) return "just now";
  if (diffSec < 3600) return `${Math.floor(diffSec / 60)}m ago`;
  if (diffSec < 86400) return `${Math.floor(diffSec / 3600)}h ago`;
  return new Date(t).toLocaleDateString();
}
