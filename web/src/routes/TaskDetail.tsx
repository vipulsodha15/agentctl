import {
  useCallback,
  useEffect,
  useReducer,
  useRef,
  useState,
} from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { ApiError, api, apiJson, jsonBody } from "../api";
import { attach } from "../ws";
import type {
  ConversationMessage,
  SnapshotData,
  Task,
  TaskDetailResponse,
  TaskMessage,
  TaskStage,
  TaskStatus,
} from "../types";
import { ConversationView } from "../components/ConversationView";
import {
  INITIAL_CONVERSATION_STATE,
  conversationReducer,
  normalizeConversation,
} from "../lib/conversation";

type WSStatus = "connecting" | "live" | "reconnecting" | "offline";

// The task-level WebSocket exists only for stage lifecycle events
// (status_changed / stage_advanced). Conversation rendering — including
// per-message echoes — flows through the active stage's session WS, which
// also handles snapshot-on-attach and reconnect.

export function TaskDetail() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const [task, setTask] = useState<Task | null>(null);
  // task_messages is the durable text-only chat log written on every send /
  // handoff / synthesis / error. Used as the text backbone whenever the
  // active stage's session WS snapshot has no text bubbles yet — either
  // because the shim hasn't flushed its SDK JSONL records (mid-turn,
  // freshly spawned stage) or because the container pre-dates the JSONL
  // mirror fix. Tool/thinking/notice rows from convState are appended on
  // top so live tool widgets render even before the JSONL flush. As soon
  // as convState carries actual text bubbles, it wins outright.
  const [taskMessages, setTaskMessages] = useState<TaskMessage[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [composer, setComposer] = useState("");
  const [sending, setSending] = useState(false);
  const [confirmAbandon, setConfirmAbandon] = useState(false);
  const [confirmComplete, setConfirmComplete] = useState(false);
  const [wsStatus, setWsStatus] = useState<WSStatus>("connecting");
  const [issueOpen, setIssueOpen] = useState(false);
  const composerRef = useRef<HTMLTextAreaElement>(null);
  const threadRef = useRef<HTMLDivElement>(null);
  const [convState, dispatchConv] = useReducer(
    conversationReducer,
    INITIAL_CONVERSATION_STATE,
  );

  // Auto-scroll the outer thread container to the bottom when new live
  // messages arrive, but only if the user is already near the bottom —
  // otherwise we'd yank them away from past-stage history they're
  // reading. The active stage's ConversationView no longer has its
  // own scroll inside the task page, so this is the one place the
  // behavior lives.
  const lastMsg = convState.messages[convState.messages.length - 1];
  useEffect(() => {
    const el = threadRef.current;
    if (!el) return;
    const nearBottom =
      el.scrollHeight - el.scrollTop - el.clientHeight < 200;
    if (nearBottom) {
      el.scrollTop = el.scrollHeight;
    }
  }, [convState.messages.length, lastMsg?.text, lastMsg?.output, convState.inFlight]);

  const load = useCallback(async () => {
    if (!id) return;
    try {
      const r = await apiJson<TaskDetailResponse>(`/v1/tasks/${id}`);
      setTask(r.task);
      setTaskMessages(r.messages ?? []);
      setError(null);
    } catch (err) {
      setError(
        err instanceof ApiError
          ? `${err.code ?? err.status}: ${err.message}`
          : String(err),
      );
    }
  }, [id]);

  useEffect(() => {
    load();
  }, [load]);

  // Task-level WS — reload on stage advance / status change. Per-message
  // events are intentionally ignored here; the session WS owns rendering.
  useEffect(() => {
    if (!id) return;
    let cancelled = false;
    let backoffMs = 500;
    let ws: WebSocket | null = null;
    let retry: number | null = null;
    let live = false;

    function connect() {
      if (cancelled) return;
      setWsStatus(live ? "reconnecting" : "connecting");
      const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
      ws = new WebSocket(
        `${proto}//${window.location.host}/v1/tasks/${id}/stream`,
        ["agentctl.v1"],
      );
      ws.onopen = () => {
        live = true;
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
        if (
          event.kind === "task.status_changed" ||
          event.kind === "task.stage_advanced"
        ) {
          load();
        } else if (event.kind === "task.message") {
          // Append newly recorded task_messages so the fallback transcript
          // stays in sync without a full refetch.
          const msg = event.data as TaskMessage | undefined;
          if (msg && typeof msg.seq === "number") {
            setTaskMessages((prev) => {
              if (prev.some((m) => m.seq === msg.seq)) return prev;
              const next = [...prev, msg];
              next.sort((a, b) => a.seq - b.seq);
              return next;
            });
          }
        }
      };
      ws.onerror = () => {
        // onclose will fire next.
      };
      ws.onclose = () => {
        live = false;
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
      if (retry !== null) window.clearTimeout(retry);
      if (ws) ws.close();
    };
  }, [id, load]);

  const stages = task?.stages ?? [];
  const activeStage = stages.find((s) => s.status === "active");
  const activeSessionID = activeStage?.session_id ?? "";

  // Active stage's session WS — full snapshot + live event stream. Resets
  // whenever the active stage changes (handoff) so we don't carry rendering
  // state across stages.
  useEffect(() => {
    if (!activeSessionID) {
      dispatchConv({ type: "reset" });
      return;
    }
    dispatchConv({ type: "reset" });
    const handle = attach(activeSessionID, {
      onOpen: () => dispatchConv({ type: "ws_open" }),
      onDisconnect: (reason) => dispatchConv({ type: "ws_close", reason }),
      onEvent: (e) => {
        if (e.kind === "session.snapshot") {
          dispatchConv({ type: "snapshot", data: e.data as SnapshotData });
          return;
        }
        if (
          e.kind === "stream_end" ||
          (e as { kind: string }).kind === "error"
        ) {
          return;
        }
        dispatchConv({ type: "event", e });
      },
    });
    return () => handle.close();
  }, [activeSessionID]);

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

  const isFinalStage =
    activeStage && activeStage.position === stages.length;
  const terminal = task.status === "done" || task.status === "abandoned";

  const doneStages = stages.filter((s) => s.status === "done");

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
      setConfirmAbandon(false);
      navigate("/tasks");
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

  const apiKeyMissing =
    error?.toLowerCase().includes("anthropic_api_key") ?? false;

  return (
    <section className="task-detail">
      <header className="task-topbar">
        <div className="task-topbar-left">
          <Link to="/tasks" className="back-link" title="Back to tasks">
            <BackArrow />
          </Link>
          <div className="task-topbar-title" title={task.name}>
            <span className="task-topbar-name">{task.name}</span>
            <span className="task-id-chip" title={task.task_id}>
              #{task.task_id.slice(-6)}
            </span>
          </div>
          <StatusPill status={task.status} />
          {activeStage && (
            <span className={`agent-tag swatch-${activeStage.colour ?? "slate"}`} title={`Current agent: ${activeStage.agent_name}`}>
              <span className="agent-tag-dot" aria-hidden />
              {activeStage.agent_name}
            </span>
          )}
        </div>
        <div className="task-topbar-right">
          <WSStatusBadge status={wsStatus} />
          <IssueSeedChip
            task={task}
            open={issueOpen}
            onToggle={() => setIssueOpen((v) => !v)}
          />
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

      <StageStrip stages={stages} taskStatus={task.status} />

      <div className="task-thread-wrap" ref={threadRef}>
        {issueOpen && (
          <IssueSeedPanel
            task={task}
            onClose={() => setIssueOpen(false)}
          />
        )}
        {apiKeyMissing && error && (
          <InlineNotice
            level="warn"
            title="ANTHROPIC_API_KEY not set"
            body="Agents can't reach the model until the key is configured. Set it in Settings, then resume."
            onDismiss={() => setError(null)}
          />
        )}

        {doneStages.map((s) => (
          <PriorStageCard
            key={s.stage_id}
            stage={s}
            nextAgent={
              stages.find((n) => n.position === s.position + 1)?.agent_name
            }
          />
        ))}

        {activeStage && activeSessionID && (
          <div className="task-active-thread">
            <ConversationView
              messages={mergeTranscript(
                convState.messages,
                taskMessages,
                activeStage.stage_id,
              )}
              warnings={convState.warnings}
              inFlight={convState.inFlight}
              mcps={convState.mcps}
              usageByTurn={convState.usageByTurn}
              filter="all"
            />
          </div>
        )}
      </div>

      {terminal ? (
        <div className="composer-banner">
          <span className="composer-banner-dot" aria-hidden />
          {task.status === "done"
            ? "Task completed. The thread is read-only."
            : "Task abandoned. The thread is read-only."}
        </div>
      ) : !activeStage ? (
        <NoWorkflowComposer taskId={task.task_id} onAttached={load} />
      ) : (
        <div className="composer">
          <textarea
            ref={composerRef}
            className="composer-input"
            placeholder={`Message ${activeStage.agent_name}…`}
            value={composer}
            onChange={(e) => setComposer(e.target.value)}
            onKeyDown={onKeyDown}
            rows={2}
          />
          <div className="composer-actions">
            <span className="composer-hint">
              <kbd>⌘</kbd>
              <kbd>↵</kbd>
              <span>to send</span>
            </span>
            <button onClick={send} disabled={!composer.trim() || sending}>
              {sending ? "Sending…" : "Send"}
            </button>
            {isFinalStage ? (
              <button
                className="primary"
                onClick={() => setConfirmComplete(true)}
                disabled={sending || convState.inFlight}
                title={convState.inFlight ? "Waiting for the agent to finish its turn" : "Mark this task complete"}
              >
                <span>Complete task</span>
                <CheckArrow />
              </button>
            ) : (
              <button
                className="primary"
                onClick={handoff}
                disabled={sending || convState.inFlight}
                title={convState.inFlight ? "Waiting for the agent to finish its turn" : `Lock the synthesis and start ${nextAgent(stages, activeStage)}`}
              >
                <span>Hand off to {nextAgent(stages, activeStage)}</span>
                <ForwardArrow />
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

      {error && !apiKeyMissing && (
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
  const label =
    status === "not-started"
      ? "not started"
      : status === "working"
        ? "working"
        : status;
  return <span className={`status-badge status-${status}`}>{label}</span>;
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

function StageStrip({
  stages,
  taskStatus,
}: {
  stages: TaskStage[];
  taskStatus: TaskStatus;
}) {
  if (stages.length === 0) return null;
  const activeIdx = stages.findIndex((s) => s.status === "active");
  return (
    <div className="stage-strip" role="list" aria-label="Workflow stages">
      {stages.map((s, idx) => {
        const isDone = s.status === "done" || taskStatus === "done";
        const isActive = s.status === "active";
        const label = `Stage ${idx + 1} of ${stages.length}: ${s.agent_name} — ${s.status}`;
        return (
          <span key={s.stage_id} className="stage-strip-item">
            <span
              role="listitem"
              aria-label={label}
              title={label}
              className={`stage-pill swatch-${s.colour ?? "slate"} ${
                isDone ? "is-done" : isActive ? "is-active" : "is-pending"
              }`}
            >
              <span className="stage-pill-num" aria-hidden>
                {isDone ? <CheckIcon /> : idx + 1}
              </span>
              <span className="stage-pill-name">{s.agent_name}</span>
            </span>
            {idx < stages.length - 1 && (
              <span
                className={`stage-strip-sep${idx < activeIdx ? " is-done" : ""}`}
                aria-hidden
              >
                <ForwardArrow />
              </span>
            )}
          </span>
        );
      })}
    </div>
  );
}

function TaskDetailSkeleton() {
  return (
    <>
      <div className="task-topbar skeleton-header">
        <div className="skel skel-line w-32" />
      </div>
      <div className="stage-strip">
        <div className="skel skel-line w-12" />
        <div className="skel skel-line w-12" />
        <div className="skel skel-line w-12" />
      </div>
      <div className="task-thread-wrap">
        <div style={{ padding: "20px 0" }}>
          <div className="skel skel-block" style={{ height: 90 }} />
          <div className="skel skel-block" style={{ height: 60, width: "60%" }} />
        </div>
      </div>
    </>
  );
}

function IssueSeedChip({
  task: _task,
  open,
  onToggle,
}: {
  task: Task;
  open: boolean;
  onToggle: () => void;
}) {
  return (
    <button
      type="button"
      className={`issue-seed-chip${open ? " open" : ""}`}
      onClick={onToggle}
      aria-expanded={open}
      title="View the issue brief — seeded into every stage"
    >
      <DocIcon />
      <span>issue.md</span>
      <span className="issue-seed-chip-chev" aria-hidden>
        <ChevronIcon direction={open ? "down" : "right"} />
      </span>
    </button>
  );
}

function IssueSeedPanel({
  task,
  onClose,
}: {
  task: Task;
  onClose: () => void;
}) {
  return (
    <div className="issue-seed-panel" role="region" aria-label="issue.md">
      <div className="issue-seed-panel-head">
        <span className="issue-seed-panel-title">
          <DocIcon />
          <span>issue.md</span>
          <span className="muted">— seeded into every stage</span>
        </span>
        <button
          type="button"
          className="ghost"
          onClick={onClose}
          aria-label="Close issue.md"
        >
          Close
        </button>
      </div>
      <div className="issue-seed-panel-body">
        <ReactMarkdown remarkPlugins={[remarkGfm]}>{task.issue_md}</ReactMarkdown>
      </div>
    </div>
  );
}

// PriorStageCard renders a handed-off stage's full session transcript.
// The session has been terminated (no live WS), but the SDK JSONL records
// survive in the messages table — we fetch them via GET
// /v1/sessions/{id}/snapshot and normalize through the same code path the
// live reducer uses. The stage's synthesis is shown as a footer callout
// so the takeaway is visible without scrolling through the whole turn
// history.
function PriorStageCard({
  stage,
  nextAgent,
}: {
  stage: TaskStage;
  nextAgent?: string;
}) {
  const [messages, setMessages] = useState<ConversationMessage[] | null>(null);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    if (!stage.session_id) {
      setLoading(false);
      return;
    }
    let cancelled = false;
    setLoading(true);
    apiJson<{ conversation: unknown[] }>(
      `/v1/sessions/${encodeURIComponent(stage.session_id)}/snapshot`,
    )
      .then((r) => {
        if (cancelled) return;
        const { messages: msgs } = normalizeConversation(r.conversation ?? []);
        setMessages(msgs);
      })
      .catch((e) => {
        if (cancelled) return;
        setErr(
          e instanceof ApiError
            ? `${e.code ?? e.status}: ${e.message}`
            : String(e),
        );
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [stage.session_id]);

  return (
    <>
      <div className={`task-prior-stage swatch-${stage.colour ?? "slate"}`}>
        <div className="task-stage-header">
          <span className="task-stage-position">Stage {stage.position}</span>
          <span className="muted">handed off ·</span>
          <span className={`agent-tag swatch-${stage.colour ?? "slate"}`}>
            <span className="agent-tag-dot" aria-hidden />
            {stage.agent_name}
          </span>
          <span className="task-stage-done">
            <CheckIcon /> done
          </span>
        </div>
        {loading && (
          <div className="task-stage-loading muted">Loading transcript…</div>
        )}
        {err && (
          <div className="task-stage-loading error-text">
            Couldn't load transcript: {err}
          </div>
        )}
        {messages !== null && messages.length > 0 && (
          <div className="task-prior-transcript">
            <ConversationView
              messages={messages}
              warnings={[]}
              inFlight={false}
              mcps={[]}
              usageByTurn={{}}
              filter="all"
            />
          </div>
        )}
        {stage.synthesis && (
          <div className="task-stage-synthesis">
            <div className="task-stage-synthesis-label">Synthesis</div>
            <div className="task-stage-synthesis-body">
              <ReactMarkdown remarkPlugins={[remarkGfm]}>
                {stage.synthesis}
              </ReactMarkdown>
            </div>
          </div>
        )}
      </div>
      {nextAgent && (
        <div className="task-stage-seam">
          <span className="task-stage-seam-line" />
          <span className="task-stage-seam-label">
            <ForwardArrow /> handed off to <strong>{nextAgent}</strong>
          </span>
          <span className="task-stage-seam-line" />
        </div>
      )}
    </>
  );
}

function InlineNotice({
  level,
  title,
  body,
  onDismiss,
}: {
  level: "warn" | "info" | "error";
  title: string;
  body?: string;
  onDismiss?: () => void;
}) {
  return (
    <div className={`inline-notice inline-notice-${level}`} role="alert">
      <span className="inline-notice-icon" aria-hidden>
        {level === "warn" ? <AlertIcon /> : <InfoIcon />}
      </span>
      <div className="inline-notice-body">
        <div className="inline-notice-title">{title}</div>
        {body && <div className="inline-notice-text">{body}</div>}
      </div>
      {onDismiss && (
        <button type="button" className="ghost inline-notice-dismiss" onClick={onDismiss}>
          dismiss
        </button>
      )}
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
  const [agents, setAgents] = useState<string[] | null>(null);
  const [mode, setMode] = useState<"workflow" | "agent">("workflow");
  const [picking, setPicking] = useState("");
  const [err, setErr] = useState<string | null>(null);
  const [attaching, setAttaching] = useState(false);
  useEffect(() => {
    apiJson<{ workflows: { name: string }[] }>("/v1/workflows")
      .then((r) => setWorkflows((r.workflows ?? []).map((w) => w.name)))
      .catch((e) => setErr(String(e)));
    apiJson<{ agents: { name: string }[] }>("/v1/agents")
      .then((r) => setAgents((r.agents ?? []).map((a) => a.name)))
      .catch((e) => setErr(String(e)));
  }, []);
  // Reset the selection when switching mode so a stale name doesn't carry over.
  useEffect(() => {
    setPicking("");
  }, [mode]);
  async function doAttach() {
    if (!picking) return;
    setAttaching(true);
    setErr(null);
    try {
      await api(`/v1/tasks/${taskId}/attach`, {
        method: "POST",
        ...jsonBody(
          mode === "workflow" ? { workflow: picking } : { agent: picking },
        ),
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
  const options = mode === "workflow" ? workflows : agents;
  const loading = options === null;
  return (
    <div className="composer attach-prompt">
      <div className="composer-bar">
        <span className="muted">Attach a workflow or a single agent to begin.</span>
        <span
          className="segmented"
          role="tablist"
          aria-label="Attach mode"
          style={{ marginLeft: "auto" }}
        >
          <button
            type="button"
            role="tab"
            aria-selected={mode === "workflow"}
            className={`segmented-btn${mode === "workflow" ? " active" : ""}`}
            onClick={() => setMode("workflow")}
            disabled={attaching}
          >
            Workflow
          </button>
          <button
            type="button"
            role="tab"
            aria-selected={mode === "agent"}
            className={`segmented-btn${mode === "agent" ? " active" : ""}`}
            onClick={() => setMode("agent")}
            disabled={attaching}
          >
            Single agent
          </button>
        </span>
      </div>
      <div className="composer-actions">
        <select
          value={picking}
          onChange={(e) => setPicking(e.target.value)}
          disabled={loading || attaching}
        >
          <option value="">
            {loading
              ? mode === "workflow"
                ? "Loading workflows…"
                : "Loading agents…"
              : mode === "workflow"
                ? "— pick a workflow —"
                : "— pick an agent —"}
          </option>
          {(options ?? []).map((n) => (
            <option key={n} value={n}>
              {n}
            </option>
          ))}
        </select>
        <button
          className="primary"
          onClick={doAttach}
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

// mergeTranscript chooses what to feed ConversationView for the active stage.
//
// convState is the rich stream from the session WS — text bubbles, tool
// widgets, thinking blocks, MCP/skill notices, cost chips. It's authoritative
// once the SDK's JSONL has been flushed (end-of-turn) and the snapshot lands.
//
// task_messages is the durable text-only chat log written eagerly on every
// send / handoff / synthesis / error. It covers two holes the JSONL doesn't:
//   1. Stages whose container pre-dates the JSONL mirror — no JSONL exists.
//   2. Mid-turn refresh on a fresh stage — JSONL hasn't been flushed yet.
// It has no tool/thinking rows because task_messages doesn't persist those.
//
// Strategy: if convState carries any text bubbles, trust it as the canonical
// transcript (the snapshot landed). Otherwise render the task_messages
// fallback for text history AND append any rich rows convState has accrued
// from live events (tool calls, thinking, MCP notices) so the user still
// sees tools, skills, and thinking as collapsed widgets even while the
// JSONL snapshot is empty.
function mergeTranscript(
  convMessages: ConversationMessage[],
  taskMessages: TaskMessage[],
  activeStageID: string,
): ConversationMessage[] {
  const convHasText = convMessages.some(
    (m) => m.kind === "user" || m.kind === "assistant",
  );
  if (convHasText) return convMessages;
  const fallback = taskMessagesAsConversation(taskMessages, activeStageID);
  const richExtras = convMessages.filter(
    (m) => m.kind === "tool" || m.kind === "thinking" || m.kind === "notice",
  );
  return fallback.length === 0 && richExtras.length === 0
    ? convMessages
    : [...fallback, ...richExtras];
}

// taskMessagesAsConversation maps the flat task_messages log into the same
// ConversationMessage shape the session WS produces.
function taskMessagesAsConversation(
  msgs: TaskMessage[],
  activeStageID: string,
): ConversationMessage[] {
  const out: ConversationMessage[] = [];
  for (const m of msgs) {
    // Only carry the active stage's history into the live thread; prior
    // stages render via PriorStageCard. Cross-stage system rows (seam,
    // task-opened seed) have no stage_id and are fine to drop here.
    if (m.stage_id && m.stage_id !== activeStageID) continue;
    const id = `tm-${m.seq}`;
    switch (m.role) {
      case "user":
        out.push({ id, kind: "user", text: m.content });
        break;
      case "assistant":
      case "synthesis":
        out.push({ id, kind: "assistant", text: m.content });
        break;
      case "error":
        out.push({ id, kind: "notice", text: m.content, notice_level: "error" });
        break;
      case "system":
      case "seam":
        out.push({ id, kind: "notice", text: m.content, notice_level: "info" });
        break;
    }
  }
  return out;
}

function BackArrow() {
  return (
    <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <polyline points="15 18 9 12 15 6" />
    </svg>
  );
}

function ForwardArrow() {
  return (
    <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M5 12h13" />
      <polyline points="13 6 19 12 13 18" />
    </svg>
  );
}

function CheckArrow() {
  return (
    <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <polyline points="20 6 9 17 4 12" />
    </svg>
  );
}

function CheckIcon() {
  return (
    <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" strokeWidth="2.4" strokeLinecap="round" strokeLinejoin="round">
      <polyline points="20 6 9 17 4 12" />
    </svg>
  );
}

function DocIcon() {
  return (
    <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
      <path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z" />
      <polyline points="14 3 14 8 19 8" />
      <line x1="9" y1="13" x2="15" y2="13" />
      <line x1="9" y1="17" x2="13" y2="17" />
    </svg>
  );
}

function AlertIcon() {
  return (
    <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round">
      <path d="M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z" />
      <line x1="12" y1="9" x2="12" y2="13" />
      <line x1="12" y1="17" x2="12.01" y2="17" />
    </svg>
  );
}

function InfoIcon() {
  return (
    <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round">
      <circle cx="12" cy="12" r="9" />
      <line x1="12" y1="11" x2="12" y2="17" />
      <line x1="12" y1="7.5" x2="12.01" y2="7.5" />
    </svg>
  );
}

function ChevronIcon({ direction }: { direction: "down" | "right" }) {
  return (
    <svg viewBox="0 0 24 24" width="12" height="12" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      {direction === "down" ? (
        <polyline points="6 9 12 15 18 9" />
      ) : (
        <polyline points="9 6 15 12 9 18" />
      )}
    </svg>
  );
}
