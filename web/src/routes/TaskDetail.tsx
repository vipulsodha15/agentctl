import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { ApiError, api, apiJson, jsonBody } from "../api";
import type {
  Task,
  TaskDetailResponse,
  TaskMessage,
  TaskStage,
  TaskStatus,
} from "../types";

const POLL_INTERVAL_MS = 4000;

type WSStatus = "connecting" | "live" | "reconnecting" | "offline";

export function TaskDetail() {
  const { id } = useParams<{ id: string }>();
  const [task, setTask] = useState<Task | null>(null);
  const [messages, setMessages] = useState<TaskMessage[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [composer, setComposer] = useState("");
  const [sending, setSending] = useState(false);
  const [confirmAbandon, setConfirmAbandon] = useState(false);
  const [confirmComplete, setConfirmComplete] = useState(false);
  const [wsStatus, setWsStatus] = useState<WSStatus>("connecting");
  const [showJumpToLatest, setShowJumpToLatest] = useState(false);
  const composerRef = useRef<HTMLTextAreaElement>(null);
  const threadRef = useRef<HTMLDivElement>(null);
  const wsLive = useRef(false);

  const load = useCallback(async () => {
    if (!id) return;
    try {
      const r = await apiJson<TaskDetailResponse>(`/v1/tasks/${id}`);
      setTask(r.task);
      setMessages(r.messages ?? []);
      setError(null);
    } catch (err) {
      setError(
        err instanceof ApiError
          ? `${err.code ?? err.status}: ${err.message}`
          : String(err),
      );
    }
  }, [id]);

  // Initial load — always
  useEffect(() => {
    load();
  }, [load]);

  // Polling fallback — runs only while WS is not connected, so the live path
  // does not double-fetch on every event.
  useEffect(() => {
    let cancelled = false;
    let timer: number | null = null;
    function tick() {
      if (cancelled) return;
      if (!wsLive.current) {
        load().finally(() => {
          if (!cancelled) timer = window.setTimeout(tick, POLL_INTERVAL_MS);
        });
      } else {
        // WS is live; skip this tick and try again later.
        timer = window.setTimeout(tick, POLL_INTERVAL_MS);
      }
    }
    timer = window.setTimeout(tick, POLL_INTERVAL_MS);
    return () => {
      cancelled = true;
      if (timer !== null) window.clearTimeout(timer);
    };
  }, [load]);

  // WebSocket attach with exponential-backoff reconnect.
  useEffect(() => {
    if (!id) return;
    let cancelled = false;
    let backoffMs = 500;
    let ws: WebSocket | null = null;
    let retry: number | null = null;

    function connect() {
      if (cancelled) return;
      setWsStatus(wsLive.current ? "reconnecting" : "connecting");
      const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
      ws = new WebSocket(
        `${proto}//${window.location.host}/v1/tasks/${id}/stream`,
        ["agentctl.v1"],
      );
      ws.onopen = () => {
        wsLive.current = true;
        backoffMs = 500;
        setWsStatus("live");
      };
      ws.onmessage = (ev) => {
        let frame: unknown;
        try {
          frame = JSON.parse(ev.data);
        } catch {
          return;
        }
        const f = frame as { kind?: string; data?: unknown };
        if (f.kind !== "event" || f.data === undefined) return;
        const event = (typeof f.data === "string" ? safeJson(f.data) : f.data) as
          | { kind?: string; data?: unknown }
          | null;
        if (!event || !event.kind) return;
        if (event.kind === "task.message") {
          const msg = (typeof event.data === "string"
            ? safeJson(event.data)
            : event.data) as TaskMessage | null;
          if (!msg) return;
          setMessages((prev) =>
            prev.some((m) => m.seq === msg.seq)
              ? prev
              : [...prev, msg].sort((a, b) => a.seq - b.seq),
          );
        } else if (
          event.kind === "task.status_changed" ||
          event.kind === "task.stage_advanced"
        ) {
          load();
        }
      };
      ws.onerror = () => {
        // onclose will fire next; let it handle the reconnect.
      };
      ws.onclose = () => {
        wsLive.current = false;
        ws = null;
        if (cancelled) {
          setWsStatus("offline");
          return;
        }
        setWsStatus("reconnecting");
        retry = window.setTimeout(connect, backoffMs);
        backoffMs = Math.min(backoffMs * 2, 8000);
      };
    }
    connect();
    return () => {
      cancelled = true;
      wsLive.current = false;
      if (retry !== null) window.clearTimeout(retry);
      if (ws) ws.close();
    };
  }, [id, load]);

  // Auto-scroll only if user is already near the bottom — preserves the
  // reading position if they've scrolled up to inspect earlier messages.
  // We key off (count, last-seq, last-content-length) so streaming updates
  // keep the viewport pinned without triggering on unrelated re-renders.
  const last = messages.length > 0 ? messages[messages.length - 1] : undefined;
  useEffect(() => {
    const el = threadRef.current;
    if (!el) return;
    const nearBottom =
      el.scrollHeight - el.scrollTop - el.clientHeight < 140;
    if (nearBottom) {
      el.scrollTop = el.scrollHeight;
      setShowJumpToLatest(false);
    } else {
      setShowJumpToLatest(true);
    }
  }, [messages.length, last?.seq, last?.content.length]);

  function onThreadScroll() {
    const el = threadRef.current;
    if (!el) return;
    const nearBottom =
      el.scrollHeight - el.scrollTop - el.clientHeight < 140;
    setShowJumpToLatest(!nearBottom);
  }

  function jumpToLatest() {
    const el = threadRef.current;
    if (!el) return;
    el.scrollTo({ top: el.scrollHeight, behavior: "smooth" });
  }

  if (!task) {
    return (
      <section className="task-detail">
        {error ? (
          <div className="error-text" style={{ padding: 16 }}>{error}</div>
        ) : (
          <TaskDetailSkeleton />
        )}
      </section>
    );
  }

  const stages = task.stages ?? [];
  const activeStage = stages.find((s) => s.status === "active");
  const isFinalStage =
    activeStage && activeStage.position === stages.length;
  const terminal = task.status === "done" || task.status === "abandoned";

  // Heuristic: the agent is "thinking" if the user/handoff-prompt was just
  // sent (last message author is user/system/seam, not assistant/synthesis)
  // and the stage is still active. Disables Hand off while busy.
  const lastForStage = activeStage
    ? [...messages].reverse().find((m) => m.stage_id === activeStage.stage_id)
    : undefined;
  const stageBusy = !!(
    activeStage &&
    lastForStage &&
    (lastForStage.role === "user" || lastForStage.role === "system")
  );

  async function send() {
    if (!id) return;
    const content = composer.trim();
    if (!content || sending) return;
    setSending(true);
    try {
      await api(`/v1/tasks/${id}/messages`, {
        method: "POST",
        ...jsonBody({ content }),
      });
      setComposer("");
      await load();
    } catch (err) {
      setError(
        err instanceof ApiError
          ? `${err.code ?? err.status}: ${err.message}`
          : String(err),
      );
    } finally {
      setSending(false);
    }
  }

  async function handoff() {
    if (!id) return;
    setSending(true);
    try {
      await api(`/v1/tasks/${id}/handoff`, { method: "POST" });
      await load();
    } catch (err) {
      setError(
        err instanceof ApiError
          ? `${err.code ?? err.status}: ${err.message}`
          : String(err),
      );
    } finally {
      setSending(false);
    }
  }

  async function complete() {
    if (!id) return;
    setSending(true);
    try {
      await api(`/v1/tasks/${id}/complete`, { method: "POST" });
      await load();
    } catch (err) {
      setError(
        err instanceof ApiError
          ? `${err.code ?? err.status}: ${err.message}`
          : String(err),
      );
    } finally {
      setSending(false);
    }
  }

  async function abandon() {
    if (!id) return;
    setSending(true);
    try {
      await api(`/v1/tasks/${id}/abandon`, { method: "POST" });
      await load();
      setConfirmAbandon(false);
    } catch (err) {
      setError(
        err instanceof ApiError
          ? `${err.code ?? err.status}: ${err.message}`
          : String(err),
      );
    } finally {
      setSending(false);
    }
  }

  function onKeyDown(e: React.KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      send();
    }
  }

  return (
    <section className="task-detail">
      <header className="task-detail-header">
        <div className="task-detail-title">
          <Link to="/tasks" className="back-link">
            <BackArrow /> Tasks
          </Link>
          <h2>
            <span className="task-id">#{task.task_id.slice(-6)}</span>{" "}
            {task.name}
          </h2>
          <div className="task-detail-meta">
            <span className="muted">
              workflow:{" "}
              <strong>{task.workflow_name || "(none)"}</strong>
            </span>
            <span className="muted">·</span>
            <StatusPill status={task.status} />
            {activeStage && (
              <>
                <span className="muted">·</span>
                <span className="muted">
                  current:{" "}
                  <span className={`agent-tag swatch-${activeStage.colour ?? "slate"}`}>
                    {activeStage.agent_name}
                  </span>
                </span>
              </>
            )}
            <span className="muted">·</span>
            <WSStatusBadge status={wsStatus} />
          </div>
        </div>
        <div className="task-detail-actions">
          {!terminal && (
            <button
              onClick={() => setConfirmAbandon(true)}
              className="abandon-btn"
              title="Stop this task. The chat thread is preserved."
            >
              Abandon
            </button>
          )}
        </div>
      </header>

      <StageRail
        stages={stages}
        onJumpToStage={(stageID) => {
          const el = threadRef.current;
          if (!el) return;
          const target = el.querySelector(
            `[data-stage-anchor="${stageID}"]`,
          ) as HTMLElement | null;
          if (target) {
            target.scrollIntoView({ behavior: "smooth", block: "start" });
          }
        }}
      />

      <div className="task-thread-wrap">
        <div
          className="task-thread"
          ref={threadRef}
          onScroll={onThreadScroll}
        >
          <IssueSeed task={task} />
          {messages
            // Drop the auto-generated "Task opened" system seed — the issue
            // card above already shows the body.
            .filter(
              (m) =>
                !(
                  m.role === "system" &&
                  m.content.startsWith("Task opened.")
                ),
            )
            .map((m, idx, arr) => (
              <MessageBubble
                key={`${m.seq}`}
                msg={m}
                prev={idx > 0 ? arr[idx - 1] : undefined}
                stages={stages}
                anchorStageID={
                  // Anchor the first message of a new stage (the message
                  // whose stage_id differs from the prior one).
                  idx === 0 || arr[idx - 1].stage_id !== m.stage_id
                    ? m.stage_id
                    : undefined
                }
              />
            ))}
          {!terminal && activeStage && stageBusy && (
            <ThinkingBubble agentName={activeStage.agent_name} colour={activeStage.colour ?? "slate"} />
          )}
        </div>
      </div>

      {terminal ? (
        <div className="composer-banner">
          {task.status === "done"
            ? "Task completed. The chat is read-only."
            : "Task abandoned. The chat is read-only."}
        </div>
      ) : !activeStage ? (
        <NoWorkflowComposer taskId={task.task_id} onAttached={load} />
      ) : (
        <div className="composer">
          <div className="composer-talking">
            <span>talking to:</span>
            <span className={`agent-tag swatch-${activeStage.colour ?? "slate"}`}>
              {activeStage.agent_name}
            </span>
          </div>
          <textarea
            ref={composerRef}
            className="composer-input"
            placeholder={`Message ${activeStage.agent_name}…`}
            value={composer}
            onChange={(e) => setComposer(e.target.value)}
            onKeyDown={onKeyDown}
            rows={3}
          />
          <div className="composer-actions">
            <span className="muted composer-hint">⌘↵ send</span>
            <button onClick={send} disabled={!composer.trim() || sending}>
              {sending ? "Sending…" : "Send"}
            </button>
            {isFinalStage ? (
              <button
                className="primary"
                onClick={() => setConfirmComplete(true)}
                disabled={sending || stageBusy}
                title={stageBusy ? "Waiting for the agent to finish its turn" : "Mark this task complete"}
              >
                Complete task ✓
              </button>
            ) : (
              <button
                className="primary"
                onClick={handoff}
                disabled={sending || stageBusy}
                title={stageBusy ? "Waiting for the agent to finish its turn" : `Lock the synthesis and start ${nextAgent(stages, activeStage)}`}
              >
                Hand off to {nextAgent(stages, activeStage)} ▸
              </button>
            )}
          </div>
        </div>
      )}

      {confirmAbandon && (
        <div className="modal-scrim" onClick={() => setConfirmAbandon(false)}>
          <div className="modal" onClick={(e) => e.stopPropagation()}>
            <h3>Abandon task?</h3>
            <p className="muted">
              The chat thread will be preserved, but no further stages run.
              You can start a fresh task if you change your mind.
            </p>
            <div className="form-actions">
              <button onClick={() => setConfirmAbandon(false)}>Cancel</button>
              <button onClick={abandon} className="danger">Abandon</button>
            </div>
          </div>
        </div>
      )}

      {confirmComplete && (
        <div className="modal-scrim" onClick={() => setConfirmComplete(false)}>
          <div className="modal" onClick={(e) => e.stopPropagation()}>
            <h3>Mark task complete?</h3>
            <p className="muted">
              The final stage's synthesis is already locked. Completing seals
              the task — no further messages can be sent.
            </p>
            <div className="form-actions">
              <button onClick={() => setConfirmComplete(false)}>Cancel</button>
              <button
                onClick={async () => {
                  await complete();
                  setConfirmComplete(false);
                }}
                className="primary"
              >
                Complete
              </button>
            </div>
          </div>
        </div>
      )}

      {showJumpToLatest && (
        <button
          type="button"
          className="jump-to-latest"
          onClick={jumpToLatest}
          aria-label="Jump to latest message"
        >
          ↓ New messages
        </button>
      )}

      {error && (
        <div className="task-error-banner">
          <span>{error}</span>
          <button onClick={() => setError(null)} className="ghost">dismiss</button>
        </div>
      )}
    </section>
  );
}

function nextAgent(stages: TaskStage[], active: TaskStage): string {
  const next = stages.find((s) => s.position === active.position + 1);
  return next?.agent_name ?? "next";
}

function StatusPill({ status }: { status: TaskStatus }) {
  return <span className={`status-badge status-${status}`}>{status}</span>;
}

function WSStatusBadge({ status }: { status: WSStatus }) {
  const label =
    status === "live"
      ? "live"
      : status === "connecting"
        ? "connecting"
        : status === "reconnecting"
          ? "reconnecting"
          : "offline";
  return (
    <span className={`ws-dot ws-${status}`} title={`Stream: ${label}`}>
      <span className="ws-dot-pulse" aria-hidden />
      <span className="ws-dot-label">{label}</span>
    </span>
  );
}

function StageRail({
  stages,
  onJumpToStage,
}: {
  stages: TaskStage[];
  onJumpToStage?: (stageID: string) => void;
}) {
  if (stages.length === 0) return null;
  const doneCount = stages.filter((s) => s.status === "done").length;
  const activeCount = stages.filter((s) => s.status === "active").length;
  // Progress is "done fraction + 0.5 for active partial".
  const progress =
    (doneCount + activeCount * 0.5) / Math.max(stages.length, 1);
  return (
    <div
      className="stage-rail"
      role="list"
      aria-label="Workflow stages"
      style={{
        ["--rail-n" as string]: String(stages.length),
        ["--rail-progress" as string]: `${progress * 100}%`,
      }}
    >
      <div className="stage-rail-track" aria-hidden />
      {stages.map((s, idx) => {
        const label = `Stage ${idx + 1} of ${stages.length}: ${s.agent_name} — ${s.status}`;
        return (
          <button
            key={s.stage_id}
            type="button"
            role="listitem"
            aria-label={label}
            title={label}
            onClick={() => onJumpToStage?.(s.stage_id)}
            className={`stage-rail-pill swatch-${s.colour ?? "slate"} status-${s.status}`}
          >
            <span className="stage-rail-num" aria-hidden>{idx + 1}</span>
            <span className="stage-rail-agent">{s.agent_name}</span>
            <span className="stage-rail-status-text">{s.status}</span>
          </button>
        );
      })}
    </div>
  );
}

function TaskDetailSkeleton() {
  return (
    <>
      <div className="task-detail-header skeleton-header">
        <div>
          <div className="skel skel-line w-12" />
          <div className="skel skel-line w-32" style={{ marginTop: 8 }} />
        </div>
      </div>
      <div className="stage-rail skeleton-rail">
        <div className="stage-rail-pill skel-pill" />
        <div className="stage-rail-pill skel-pill" />
        <div className="stage-rail-pill skel-pill" />
      </div>
      <div className="task-thread-wrap">
        <div className="task-thread" style={{ padding: "20px 0" }}>
          <div className="skel skel-block" style={{ height: 90 }} />
          <div className="skel skel-block" style={{ height: 60, width: "60%" }} />
        </div>
      </div>
    </>
  );
}

function IssueSeed({ task }: { task: Task }) {
  return (
    <div className="issue-seed">
      <div className="issue-seed-header">
        <span className="issue-seed-icon" aria-hidden>📄</span>
        <span className="issue-seed-label">issue.md</span>
        <span className="muted">seeded into every stage</span>
      </div>
      <div className="issue-seed-body">
        <ReactMarkdown remarkPlugins={[remarkGfm]}>{task.issue_md}</ReactMarkdown>
      </div>
    </div>
  );
}

function MessageBubble({
  msg,
  prev,
  stages,
  anchorStageID,
}: {
  msg: TaskMessage;
  prev?: TaskMessage;
  stages: TaskStage[];
  anchorStageID?: string;
}) {
  // Compute everything *before* any conditional return so React's hook order
  // stays stable across renders.
  const colour = !msg.stage_id
    ? "slate"
    : stages.find((s) => s.stage_id === msg.stage_id)?.colour ?? "slate";
  const sameAsPrev = !!prev &&
    prev.role === msg.role &&
    prev.agent_name === msg.agent_name;

  const anchorAttr = anchorStageID ? { "data-stage-anchor": anchorStageID } : {};

  if (msg.role === "seam") {
    return (
      <div className="thread-seam" {...anchorAttr}>
        <span className="thread-seam-line" />
        <span className="thread-seam-label">{msg.content}</span>
        <span className="thread-seam-line" />
      </div>
    );
  }
  if (msg.role === "system") {
    return (
      <div className="msg-system" {...anchorAttr}>
        <span className="muted">{msg.content}</span>
      </div>
    );
  }
  if (msg.role === "error") {
    return (
      <div className="msg-error" {...anchorAttr}>
        <span>⚠ {msg.content}</span>
      </div>
    );
  }

  const isUser = msg.role === "user";
  const isSynthesis = msg.role === "synthesis";

  return (
    <div
      className={`msg-row${isUser ? " user" : ""}${isSynthesis ? " synthesis" : ""}`}
      data-colour={colour}
      {...anchorAttr}
    >
      <div className="msg-stripe" />
      <div className="msg-bubble">
        {!sameAsPrev && (
          <div className="msg-header">
            {isUser ? (
              <span className="msg-author">You</span>
            ) : (
              <>
                <span className={`agent-swatch swatch-${colour}`} />
                <span className="msg-author">{msg.agent_name || "agent"}</span>
                {isSynthesis && (
                  <span className="msg-synthesis-tag">synthesis</span>
                )}
              </>
            )}
            <span className="msg-time muted" title={msg.at}>
              {formatTime(msg.at)}
            </span>
          </div>
        )}
        <div className="msg-body">
          <ReactMarkdown remarkPlugins={[remarkGfm]}>{msg.content}</ReactMarkdown>
        </div>
      </div>
    </div>
  );
}

function ThinkingBubble({ agentName, colour }: { agentName: string; colour: string }) {
  return (
    <div className="msg-row" data-colour={colour}>
      <div className="msg-stripe" />
      <div className="msg-bubble">
        <div className="msg-header">
          <span className={`agent-swatch swatch-${colour}`} />
          <span className="msg-author">{agentName}</span>
        </div>
        <div className="msg-body thinking">
          <span className="dot" />
          <span className="dot" />
          <span className="dot" />
        </div>
      </div>
    </div>
  );
}

function NoWorkflowComposer({
  taskId,
  onAttached,
}: {
  taskId: string;
  onAttached: () => void;
}) {
  const [workflows, setWorkflows] = useState<string[] | null>(null);
  const [picking, setPicking] = useState("");
  const [err, setErr] = useState<string | null>(null);
  const [attaching, setAttaching] = useState(false);
  useEffect(() => {
    apiJson<{ workflows: { name: string }[] }>("/v1/workflows")
      .then((r) => setWorkflows((r.workflows ?? []).map((w) => w.name)))
      .catch((e) => setErr(String(e)));
  }, []);
  async function attach() {
    if (!picking) return;
    setAttaching(true);
    setErr(null);
    try {
      await api(`/v1/tasks/${taskId}/attach`, {
        method: "POST",
        ...jsonBody({ workflow: picking }),
      });
      onAttached();
    } catch (e) {
      setErr(
        e instanceof ApiError
          ? `${e.code ?? e.status}: ${e.message}`
          : String(e),
      );
    } finally {
      setAttaching(false);
    }
  }
  return (
    <div className="composer attach-prompt">
      <div className="composer-talking">
        <span className="muted">Attach a workflow to begin.</span>
      </div>
      <div className="composer-actions">
        <select
          value={picking}
          onChange={(e) => setPicking(e.target.value)}
          disabled={workflows === null || attaching}
        >
          <option value="">
            {workflows === null ? "Loading workflows…" : "— pick a workflow —"}
          </option>
          {(workflows ?? []).map((w) => (
            <option key={w} value={w}>
              {w}
            </option>
          ))}
        </select>
        <button
          className="primary"
          onClick={attach}
          disabled={!picking || attaching}
        >
          {attaching ? "Attaching…" : "Attach"}
        </button>
      </div>
      {err && <div className="error-text">{err}</div>}
    </div>
  );
}

function safeJson<T = unknown>(s: string): T | null {
  try {
    return JSON.parse(s) as T;
  } catch {
    return null;
  }
}

function formatTime(iso: string): string {
  const t = Date.parse(iso);
  if (!t) return "";
  const d = new Date(t);
  const ageMs = Date.now() - t;
  // < 24h: HH:MM. < 7d: weekday + HH:MM. Otherwise: full date.
  if (ageMs < 24 * 3600 * 1000) {
    return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  }
  if (ageMs < 7 * 24 * 3600 * 1000) {
    return d.toLocaleString([], {
      weekday: "short",
      hour: "2-digit",
      minute: "2-digit",
    });
  }
  return d.toLocaleString([], {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

function BackArrow() {
  return (
    <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <polyline points="15 18 9 12 15 6" />
    </svg>
  );
}
