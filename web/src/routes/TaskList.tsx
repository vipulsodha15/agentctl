import { useEffect, useMemo, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { ApiError, apiJson } from "../api";
import type { ListTasksResponse, Task, TaskStage, TaskStatus } from "../types";

const POLL_INTERVAL_MS = 4000;

const COLUMNS: { status: TaskStatus; label: string; hint: string }[] = [
  { status: "not-started", label: "Not started", hint: "No tasks waiting" },
  { status: "working", label: "Working", hint: "No tasks in flight" },
  { status: "done", label: "Done", hint: "Nothing finished yet" },
  { status: "abandoned", label: "Abandoned", hint: "Nothing abandoned" },
];

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

  const grouped = useMemo(() => {
    const map: Record<TaskStatus, Task[]> = {
      "not-started": [],
      working: [],
      done: [],
      abandoned: [],
    };
    if (rows) {
      const sorted = [...rows].sort((a, b) => {
        const ta = Date.parse(a.created_at) || 0;
        const tb = Date.parse(b.created_at) || 0;
        return tb - ta;
      });
      for (const t of sorted) map[t.status]?.push(t);
    }
    return map;
  }, [rows]);

  return (
    <section className="page task-board-page">
      <div className="page-header">
        <div style={{ flex: 1 }}>
          <h2>Tasks</h2>
          <div className="muted" style={{ marginTop: 4 }}>
            {rows === null
              ? "Loading…"
              : rows.length === 0
                ? "No tasks yet"
                : `${rows.length} task${rows.length === 1 ? "" : "s"} across ${COLUMNS.length} lanes`}
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
        <div className="task-board" role="list" aria-label="Task board">
          {COLUMNS.map((col) => (
            <BoardColumn
              key={col.status}
              status={col.status}
              label={col.label}
              hint={col.hint}
              tasks={grouped[col.status]}
              onOpen={(t) => navigate(`/tasks/${t.task_id}`)}
            />
          ))}
        </div>
      )}
    </section>
  );
}

function BoardColumn({
  status,
  label,
  hint,
  tasks,
  onOpen,
}: {
  status: TaskStatus;
  label: string;
  hint: string;
  tasks: Task[];
  onOpen: (t: Task) => void;
}) {
  return (
    <div
      className={`task-board-column status-${status}`}
      role="listitem"
      aria-label={`${label} — ${tasks.length} task${tasks.length === 1 ? "" : "s"}`}
    >
      <div className="task-board-column-head">
        <span className="task-board-column-dot" aria-hidden />
        <span className="task-board-column-label">{label}</span>
        <span className="task-board-column-count">{tasks.length}</span>
      </div>
      <div className="task-board-column-body">
        {tasks.length === 0 ? (
          <div className="task-board-empty muted">{hint}</div>
        ) : (
          tasks.map((t) => (
            <TaskCard key={t.task_id} task={t} onOpen={() => onOpen(t)} />
          ))
        )}
      </div>
    </div>
  );
}

function TaskCard({ task, onOpen }: { task: Task; onOpen: () => void }) {
  const stages = task.stages ?? [];
  const activeIdx = stages.findIndex((s) => s.status === "active");
  const currentAgent =
    activeIdx >= 0
      ? stages[activeIdx]
      : stages.length > 0
        ? stages[stages.length - 1]
        : undefined;
  const doneCount = stages.filter((s) => s.status === "done").length;
  return (
    <div
      className="task-card"
      onClick={onOpen}
      role="button"
      tabIndex={0}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          onOpen();
        }
      }}
    >
      <div className="task-card-head">
        <span className="task-card-id">#{task.task_id.slice(-6)}</span>
        <span className="task-card-time">{formatRelative(task.created_at)}</span>
      </div>
      <div className="task-card-name">{task.name}</div>
      {stages.length > 0 ? (
        <MiniStageRail stages={stages} />
      ) : (
        <div className="task-card-no-workflow muted">No workflow attached</div>
      )}
      {(task.workflow_name || currentAgent) && (
        <div className="task-card-foot">
          {task.workflow_name && (
            <span className="task-card-workflow">
              <WorkflowGlyph />
              {task.workflow_name}
            </span>
          )}
          {currentAgent && (
            <span
              className={`task-card-agent swatch-${currentAgent.colour ?? "slate"}`}
              title={
                task.status === "working"
                  ? `Talking to ${currentAgent.agent_name}`
                  : `Last agent: ${currentAgent.agent_name}`
              }
            >
              <span className="task-card-agent-dot" aria-hidden />
              <span className="task-card-agent-name">
                {currentAgent.agent_name}
              </span>
              {task.status === "working" && stages.length > 1 && (
                <span className="task-card-progress muted">
                  {Math.min(doneCount + 1, stages.length)}/{stages.length}
                </span>
              )}
            </span>
          )}
        </div>
      )}
    </div>
  );
}

function MiniStageRail({ stages }: { stages: TaskStage[] }) {
  return (
    <div className="task-card-rail" aria-hidden>
      {stages.map((s, idx) => (
        <span
          key={s.stage_id}
          className={`task-card-rail-seg status-${s.status} swatch-${s.colour ?? "slate"}`}
          title={`${s.agent_name} — ${s.status}`}
          style={{ flex: 1 }}
        >
          {idx === 0 && <span className="task-card-rail-cap" aria-hidden />}
        </span>
      ))}
    </div>
  );
}

function WorkflowGlyph() {
  return (
    <svg viewBox="0 0 24 24" width="11" height="11" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
      <circle cx="5" cy="12" r="2" />
      <circle cx="12" cy="12" r="2" />
      <circle cx="19" cy="12" r="2" />
      <path d="M7 12h3" />
      <path d="M14 12h3" />
    </svg>
  );
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
