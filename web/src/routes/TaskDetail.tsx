import {
  useCallback,
  useEffect,
  useLayoutEffect,
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
import { TaskTodoRail } from "../components/TaskTodoRail";
import { ChangesPanel } from "../components/ChangesPanel";
import {
  INITIAL_CONVERSATION_STATE,
  conversationReducer,
  normalizeConversation,
} from "../lib/conversation";

const DIFF_DRAWER_OPEN_KEY = "agentctl.task.diffDrawer";

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

  // Diff drawer state. The drawer lives on the right side of the task page
  // and shows the active stage's session diff via ChangesPanel. Persisted to
  // localStorage so it stays open across navigations within a session.
  const [diffOpen, setDiffOpen] = useState<boolean>(() => {
    try {
      return localStorage.getItem(DIFF_DRAWER_OPEN_KEY) === "1";
    } catch {
      return false;
    }
  });
  useEffect(() => {
    try {
      localStorage.setItem(DIFF_DRAWER_OPEN_KEY, diffOpen ? "1" : "0");
    } catch {
      // ignore — storage may be disabled
    }
  }, [diffOpen]);
  // Bump on every turn end so the drawer's diff tree refreshes after files
  // are likely written. Edge → low signal-to-noise to refresh on every WS
  // event; once per turn is the right granularity.
  const [diffRefreshKey, setDiffRefreshKey] = useState(0);
  const inFlightRef = useRef<boolean>(false);
  useEffect(() => {
    if (inFlightRef.current && !convState.inFlight) {
      setDiffRefreshKey((k) => k + 1);
    }
    inFlightRef.current = convState.inFlight;
  }, [convState.inFlight]);

  // Auto-scroll the outer thread container to the bottom when new live
  // messages arrive, but only if the user is already near the bottom —
  // otherwise we'd yank them away from past-stage history they're
  // reading. The active stage's ConversationView no longer has its
  // own scroll inside the task page, so this is the one place the
  // behavior lives.
  //
  // We measure `nearBottom` against the previous scroll/content state
  // (captured in a ref before the DOM updates) so a streaming delta that
  // just appended content doesn't push us past the threshold and stall
  // the auto-scroll. The scroll itself uses `behavior: "instant"` to
  // override the container's CSS `scroll-behavior: smooth` — rapid
  // streaming updates would otherwise keep interrupting an in-flight
  // smooth animation and the bottom would never be reached.
  const lastMsg = convState.messages[convState.messages.length - 1];
  const stickToBottomRef = useRef(true);
  useEffect(() => {
    const el = threadRef.current;
    if (!el) return;
    const onScroll = () => {
      stickToBottomRef.current =
        el.scrollHeight - el.scrollTop - el.clientHeight < 200;
    };
    el.addEventListener("scroll", onScroll, { passive: true });
    return () => el.removeEventListener("scroll", onScroll);
  }, []);
  useLayoutEffect(() => {
    const el = threadRef.current;
    if (!el) return;
    if (!stickToBottomRef.current) return;
    el.scrollTo({ top: el.scrollHeight, behavior: "instant" as ScrollBehavior });
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

  // Escape cancels the active stage's turn — mirrors the SessionDetail
  // binding (commit 1f2785d) so keyboard users on the task page get the
  // same behavior. Skip while a confirm modal is open so its own
  // Escape-to-close keeps working.
  useEffect(() => {
    if (!activeSessionID) return;
    if (!convState.inFlight) return;
    if (confirmAbandon || confirmComplete) return;
    let stopping = false;
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      if (stopping) return;
      stopping = true;
      apiJson(
        `/v1/sessions/${encodeURIComponent(activeSessionID)}/interrupt`,
        { method: "POST", ...jsonBody({ clear_queue: false }) },
      )
        .catch(() => {
          // Errors surface via the existing event stream / status panel.
        })
        .finally(() => {
          stopping = false;
        });
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [activeSessionID, convState.inFlight, confirmAbandon, confirmComplete]);

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

  const drawerVisible = diffOpen && !!activeSessionID;

  return (
    <section className={`task-detail${drawerVisible ? " diff-open" : ""}`}>
      <div className="task-main">
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
          {activeSessionID && (
            <button
              type="button"
              className={`diff-toggle-chip${diffOpen ? " active" : ""}`}
              onClick={() => setDiffOpen((v) => !v)}
              aria-pressed={diffOpen}
              title={diffOpen ? "Hide changed files" : "Show changed files"}
            >
              <FilesIcon />
              <span>Files</span>
              <DrawerChevron open={diffOpen} />
            </button>
          )}
          <IssueSeedChip
            task={task}
            open={issueOpen}
            onToggle={() => setIssueOpen((v) => !v)}
          />
          {!terminal && activeStage && (
            isFinalStage ? (
              <button
                className="topbar-primary"
                onClick={() => setConfirmComplete(true)}
                disabled={sending || convState.inFlight}
                title={convState.inFlight ? "Waiting for the agent to finish its turn" : "Mark this task complete"}
              >
                <span>Complete task</span>
                <CheckArrow />
              </button>
            ) : (
              <button
                className="topbar-primary"
                onClick={handoff}
                disabled={sending || convState.inFlight}
                title={convState.inFlight ? "Waiting for the agent to finish its turn" : `Lock the synthesis and start ${nextAgent(stages, activeStage)}`}
              >
                <span>Hand off to {nextAgent(stages, activeStage)}</span>
                <ForwardArrow />
              </button>
            )
          )}
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
            taskMessages={taskMessages}
            nextAgent={
              stages.find((n) => n.position === s.position + 1)?.agent_name
            }
          />
        ))}

        {activeStage && activeSessionID && (() => {
          // Compute the merged transcript once so the rail and the
          // ConversationView see the same set of tool entries. Without
          // this the rail reads convState directly and loses TodoWrite
          // tool calls on refresh — exactly the bug the task_messages
          // mirror was added to fix.
          const merged = mergeTranscript(
            convState.messages,
            taskMessages,
            activeStage.stage_id,
          );
          return (
            <div className="task-active-thread">
              <ConversationView
                messages={merged}
                warnings={convState.warnings}
                inFlight={convState.inFlight}
                mcps={convState.mcps}
                usageByTurn={convState.usageByTurn}
                filter="all"
                sessionId={activeSessionID}
              />
              <TaskTodoRail messages={merged} />
            </div>
          );
        })()}
      </div>

      {terminal ? (
        <div className="composer-banner">
          <span className="composer-banner-dot" aria-hidden />
          {task.status === "done"
            ? "Task completed. The thread is read-only."
            : "Task abandoned. The thread is read-only."}
        </div>
      ) : !activeStage ? (
        <NoAssemblyLineComposer taskId={task.task_id} onAttached={load} />
      ) : (
        <div className="composer">
          <div className="composer-input-wrap">
            <textarea
              ref={composerRef}
              className="composer-input"
              placeholder={`Message ${activeStage.agent_name}…`}
              value={composer}
              onChange={(e) => setComposer(e.target.value)}
              onKeyDown={onKeyDown}
              rows={2}
            />
            <button
              className="composer-send-inline"
              onClick={send}
              disabled={!composer.trim() || sending}
            >
              {sending ? "Sending…" : "Send"}
            </button>
          </div>
          <span className="composer-hint">
            <kbd>⌘</kbd>
            <kbd>↵</kbd>
            <span>to send</span>
          </span>
        </div>
      )}
      </div>

      {drawerVisible && (
        <aside className="task-diff-drawer" aria-label="Changed files">
          <div className="task-diff-drawer-header">
            <strong>Changes</strong>
            <button
              type="button"
              className="task-diff-drawer-close"
              onClick={() => setDiffOpen(false)}
              aria-label="Close changes drawer"
              title="Close"
            >
              <CloseIcon />
            </button>
          </div>
          <div className="task-diff-drawer-body">
            <ChangesPanel
              sessionId={activeSessionID}
              refreshKey={diffRefreshKey}
            />
          </div>
        </aside>
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
  // Per ADR 0020 §UX principles (provider invisibility), the per-stage
  // runtime chip only appears when the line actually exercises more than
  // one provider across its spawned stages. A single-provider line keeps
  // the pre-ADR 0020 chrome unchanged.
  const showRuntime = stagesMixProviders(stages);
  return (
    <div className="stage-strip" role="list" aria-label="Assembly line stages">
      {stages.map((s, idx) => {
        const isDone = s.status === "done" || taskStatus === "done";
        const isActive = s.status === "active";
        const runtime = stageRuntimeLabel(s);
        const baseLabel = `Stage ${idx + 1} of ${stages.length}: ${s.agent_name} — ${s.status}`;
        const label = runtime ? `${baseLabel} on ${runtime}` : baseLabel;
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
              {showRuntime && runtime && (
                <span
                  className="stage-pill-runtime"
                  title={`Runtime: ${runtime}`}
                  data-testid="stage-pill-runtime"
                >
                  {runtime}
                </span>
              )}
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

// stagesMixProviders mirrors the Go-side gate in internal/cli/task.go: the
// runtime chip earns its place by showing a difference, so we only enable
// it once two distinct non-empty providers have been spawned. Pending
// stages (no session yet) carry an empty provider and don't trigger
// visibility on their own.
function stagesMixProviders(stages: TaskStage[]): boolean {
  let seen = "";
  for (const s of stages) {
    if (!s.provider) continue;
    if (!seen) {
      seen = s.provider;
      continue;
    }
    if (s.provider !== seen) return true;
  }
  return false;
}

// stageRuntimeLabel renders a stage's provider/model into the short pill
// text. Provider alone -> "anthropic"; provider+model -> "anthropic/opus-4".
// Returns empty when neither field is known yet.
function stageRuntimeLabel(s: TaskStage): string {
  if (s.provider && s.model) return `${s.provider}/${s.model}`;
  if (s.provider) return s.provider;
  if (s.model) return s.model;
  return "";
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
// The session has been terminated (no live WS). We prefer the SDK JSONL
// records from GET /v1/sessions/{id}/snapshot when present, but that
// mirror is async — the shim may not have flushed before StopStage tore
// the actor down, especially on fast Complete flows. Fall back to the
// synchronous task_messages backbone keyed by stage_id (the same source
// mergeTranscript uses for the active stage) so the chat never goes
// silently blank just because the JSONL mirror is empty. The stage's
// synthesis is shown as a footer callout so the takeaway is visible
// without scrolling through the whole turn history.
function PriorStageCard({
  stage,
  taskMessages,
  nextAgent,
}: {
  stage: TaskStage;
  taskMessages: TaskMessage[];
  nextAgent?: string;
}) {
  const [snapshot, setSnapshot] = useState<ConversationMessage[] | null>(null);
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
        setSnapshot(msgs);
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

  const merged = mergeTranscript(
    snapshot ?? [],
    taskMessages,
    stage.stage_id,
  );

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
        {loading && merged.length === 0 && (
          <div className="task-stage-loading muted">Loading transcript…</div>
        )}
        {err && merged.length === 0 && (
          <div className="task-stage-loading error-text">
            Couldn't load transcript: {err}
          </div>
        )}
        {merged.length > 0 && (
          <div className="task-prior-transcript">
            <ConversationView
              messages={merged}
              warnings={[]}
              inFlight={false}
              mcps={[]}
              usageByTurn={{}}
              filter="all"
            />
          </div>
        )}
        {!loading && !err && merged.length === 0 && !stage.synthesis && (
          <div className="task-stage-loading muted">
            Transcript unavailable for this stage.
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

function NoAssemblyLineComposer({
  taskId,
  onAttached,
}: {
  taskId: string;
  onAttached: () => void;
}) {
  const [assemblyLines, setAssemblyLines] = useState<string[] | null>(null);
  const [agents, setAgents] = useState<string[] | null>(null);
  const [mode, setMode] = useState<"assembly-line" | "agent">("assembly-line");
  const [picking, setPicking] = useState("");
  const [err, setErr] = useState<string | null>(null);
  const [attaching, setAttaching] = useState(false);
  useEffect(() => {
    apiJson<{ assembly_lines: { name: string }[] }>("/v1/assembly-lines")
      .then((r) => setAssemblyLines((r.assembly_lines ?? []).map((w) => w.name)))
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
          mode === "assembly-line"
            ? { assembly_line: picking }
            : { agent: picking },
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
  const options = mode === "assembly-line" ? assemblyLines : agents;
  const loading = options === null;
  return (
    <div className="composer attach-prompt">
      <div className="composer-bar">
        <span className="muted">Attach an assembly line or a single agent to begin.</span>
        <span
          className="segmented"
          role="tablist"
          aria-label="Attach mode"
          style={{ marginLeft: "auto" }}
        >
          <button
            type="button"
            role="tab"
            aria-selected={mode === "assembly-line"}
            className={`segmented-btn${mode === "assembly-line" ? " active" : ""}`}
            onClick={() => setMode("assembly-line")}
            disabled={attaching}
          >
            Assembly line
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
              ? mode === "assembly-line"
                ? "Loading assembly lines…"
                : "Loading agents…"
              : mode === "assembly-line"
                ? "— pick an assembly line —"
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
// task_messages is the durable chat log written eagerly on every send /
// handoff / synthesis / error, AND on every tool call / tool result (role
// "tool"). It covers holes the JSONL doesn't:
//   1. Stages whose container pre-dates the JSONL mirror — no JSONL exists.
//   2. Mid-turn refresh on a fresh stage — JSONL hasn't been flushed yet.
//   3. Tool entries on providers whose JSONL the snapshot path can't
//      normalize (e.g. Codex's CLI shape).
//
// Strategy: pick the source with MORE text bubbles. The previous "any text
// bubble in convState wins" rule broke whenever the JSONL snapshot was
// empty/lagging: the first live user.message event would push convState past
// the "has text" threshold and the merge would flip to convState alone, even
// though taskMessages held the full prior history — making the visible chat
// blink to just the new exchange on every send. Comparing counts keeps
// taskMessages as the backbone whenever it is richer, and only hands the
// reins to convState once the JSONL snapshot has caught up. In the fallback
// path we still overlay convState's rich rows (tool / thinking / notice) and
// any newly streamed text bubbles that have not yet been mirrored.
function mergeTranscript(
  convMessages: ConversationMessage[],
  taskMessages: TaskMessage[],
  activeStageID: string,
): ConversationMessage[] {
  const fallback = taskMessagesAsConversation(taskMessages, activeStageID);
  const convTextCount = countTextBubbles(convMessages);
  const fallbackTextCount = countTextBubbles(fallback);

  // Steady state: convState carries at least as many text bubbles as the
  // durable mirror, so the JSONL snapshot has caught up and the rich live
  // transcript is canonical.
  if (convTextCount >= fallbackTextCount) {
    if (fallback.length === 0 && convMessages.length === 0) return convMessages;
    return convMessages;
  }

  // Fallback path: render taskMessages as the text backbone, then append
  // convState's rich rows and any trailing text bubbles convState carries
  // past what the mirror has flushed (e.g. the in-flight assistant reply).
  const richExtras = convMessages.filter(
    (m) => m.kind === "tool" || m.kind === "thinking" || m.kind === "notice",
  );
  const convTextOverflow: ConversationMessage[] = [];
  let seen = 0;
  for (const m of convMessages) {
    if (m.kind === "user" || m.kind === "assistant") {
      if (seen >= fallbackTextCount) convTextOverflow.push(m);
      seen++;
    }
  }
  return [...fallback, ...richExtras, ...convTextOverflow];
}

function countTextBubbles(msgs: ConversationMessage[]): number {
  let n = 0;
  for (const m of msgs) {
    if (m.kind === "user" || m.kind === "assistant") n++;
  }
  return n;
}

// Wire shape of role=tool task_messages — written by tm.Manager's
// handleToolUse / handleToolResult. Two rows per tool exchange (call +
// result), paired by tool_use_id so re-rendering after a refresh produces
// the same single tool widget the live stream did.
interface ToolCallPayload {
  phase: "call";
  tool: string;
  tool_use_id?: string;
  input?: unknown;
}
interface ToolResultPayload {
  phase: "result";
  tool?: string;
  tool_use_id?: string;
  output?: unknown;
  is_error?: boolean;
}

// taskMessagesAsConversation maps the flat task_messages log into the same
// ConversationMessage shape the session WS produces. Tool entries are paired
// by tool_use_id so a call+result becomes a single tool widget.
function taskMessagesAsConversation(
  msgs: TaskMessage[],
  activeStageID: string,
): ConversationMessage[] {
  const out: ConversationMessage[] = [];
  const toolIndexById: Record<string, number> = {};
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
      case "tool": {
        const payload = safeJson<ToolCallPayload | ToolResultPayload>(m.content);
        if (!payload) break;
        if (payload.phase === "call") {
          const useId = payload.tool_use_id ?? "";
          const idx = out.length;
          if (useId) toolIndexById[useId] = idx;
          out.push({
            id: useId ? `tm-tc-${useId}` : id,
            kind: "tool",
            tool: payload.tool || "?",
            tool_use_id: useId || undefined,
            input: payload.input ?? {},
            text: stableStringify(payload.input ?? {}),
            status: "pending",
          });
        } else {
          const useId = payload.tool_use_id ?? "";
          const outputText =
            typeof payload.output === "string"
              ? payload.output
              : stableStringify(payload.output ?? "");
          const isErr = !!payload.is_error;
          const idx = useId ? toolIndexById[useId] : undefined;
          if (idx !== undefined && out[idx]?.kind === "tool") {
            const prev = out[idx];
            out[idx] = {
              ...prev,
              output: outputText,
              is_error: isErr,
              status: isErr ? "error" : "done",
            };
          } else {
            out.push({
              id: useId ? `tm-tr-${useId}` : id,
              kind: "tool",
              tool: payload.tool ?? "",
              tool_use_id: useId || undefined,
              input: undefined,
              text: "",
              output: outputText,
              is_error: isErr,
              status: isErr ? "error" : "done",
            });
          }
        }
        break;
      }
    }
  }
  return out;
}

function stableStringify(v: unknown): string {
  try {
    return JSON.stringify(v, null, 2);
  } catch {
    return String(v);
  }
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

function FilesIcon() {
  return (
    <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
      <path d="M3 6h6l2 2h10v11a1 1 0 0 1-1 1H3z" />
      <line x1="3" y1="11" x2="21" y2="11" />
    </svg>
  );
}

function DrawerChevron({ open }: { open: boolean }) {
  return (
    <svg
      viewBox="0 0 24 24"
      width="11"
      height="11"
      fill="none"
      stroke="currentColor"
      strokeWidth="2.2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
      style={{
        transition: "transform 180ms var(--ease-out)",
        transform: open ? "rotate(180deg)" : "rotate(0deg)",
      }}
    >
      <polyline points="15 6 9 12 15 18" />
    </svg>
  );
}

function CloseIcon() {
  return (
    <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
      <line x1="6" y1="6" x2="18" y2="18" />
      <line x1="18" y1="6" x2="6" y2="18" />
    </svg>
  );
}
