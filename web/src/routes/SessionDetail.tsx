import { useCallback, useEffect, useMemo, useReducer, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { ApiError, apiJson } from "../api";
import { attach } from "../ws";
import type {
  ConversationMessage,
  McpStatus,
  SessionStatus,
  SnapshotData,
  WireEvent,
} from "../types";
import { ConversationView } from "../components/ConversationView";
import { MessageInput } from "../components/MessageInput";
import { McpPanel } from "../components/McpPanel";
import { StopButton } from "../components/StopButton";
import { CostPanel } from "../components/CostPanel";
import { ChangesPanel } from "../components/ChangesPanel";

interface State {
  status: SessionStatus | "unknown";
  name: string;
  inFlight: boolean;
  queueDepth: number;
  mcps: McpStatus[];
  messages: ConversationMessage[];
  warnings: string[];
  // Track event ids we have already applied (at-least-once delivery, ADR 0015).
  seenEventIds: Set<string>;
  // The currently-open assistant bubble id per turn. Set on the first
  // delta/non-empty assistant.message for a turn; CLEARED on every
  // assistant.message so the next delta in the same turn opens a fresh
  // bubble. An SDK turn can emit multiple AssistantMessage records around
  // a tool round-trip; each one becomes its own bubble.
  openBubbleByTurn: Record<string, string>;
  // Number of in-flight turns. turn.start increments; turn.end/cancelled
  // decrements (clamped at 0). We use a counter (not a turn_id set)
  // because the daemon's turn.start.turn_id historically did NOT match
  // the shim's turn.end.turn_id — clients can't reliably pair them.
  // The daemon runs turns serially, so a counter is enough.
  inFlightCount: number;
  // tool_use_id → tool name, populated as tool.call events arrive so we
  // can resolve the tool name on the matching tool.result.
  toolNames: Record<string, string>;
  connected: boolean;
  disconnectReason: string | null;
}

type Action =
  | { type: "reset" }
  | { type: "snapshot"; data: SnapshotData }
  | { type: "event"; e: WireEvent }
  | { type: "ws_open" }
  | { type: "ws_close"; reason: string };

const INITIAL: State = {
  status: "unknown",
  name: "",
  inFlight: false,
  queueDepth: 0,
  mcps: [],
  messages: [],
  warnings: [],
  seenEventIds: new Set(),
  openBubbleByTurn: {},
  inFlightCount: 0,
  toolNames: {},
  connected: false,
  disconnectReason: null,
};

function reducer(state: State, action: Action): State {
  switch (action.type) {
    case "reset":
      return { ...INITIAL };
    case "ws_open":
      return { ...state, connected: true, disconnectReason: null };
    case "ws_close":
      return { ...state, connected: false, disconnectReason: action.reason };
    case "snapshot":
      return applySnapshot(state, action.data);
    case "event":
      return applyEvent(state, action.e);
    default:
      return state;
  }
}

function applySnapshot(state: State, data: SnapshotData): State {
  // The snapshot wins: discard local mid-turn rendering state and rebuild.
  const rawConv = (data as { conversation?: unknown }).conversation;
  const convArray: unknown[] = Array.isArray(rawConv)
    ? rawConv
    : Array.isArray((rawConv as { messages?: unknown[] } | null)?.messages)
      ? ((rawConv as { messages: unknown[] }).messages)
      : [];
  const { messages, toolNames } = normalizeConversation(convArray);
  // SessionSnapshotData.in_flight (proto.go) is a turn_id string when in-flight,
  // empty otherwise — the local type declares it as boolean for historical
  // reasons. Coerce defensively.
  const rawInFlight = (data as unknown as { in_flight?: unknown }).in_flight;
  const isInFlight =
    (typeof rawInFlight === "string" && rawInFlight !== "") ||
    rawInFlight === true;
  return {
    ...state,
    status: data.session?.status ?? state.status,
    name: data.session?.name ?? state.name,
    inFlight: isInFlight,
    queueDepth: data.queue_depth ?? 0,
    mcps: normalizeMcps(data.mcps_status),
    messages,
    warnings: [],
    seenEventIds: new Set(),
    openBubbleByTurn: {},
    inFlightCount: isInFlight ? 1 : 0,
    toolNames,
  };
}

// mcps_status is sent as map[name]status on the wire (proto.SessionSnapshotData),
// but McpPanel renders an array of McpStatus objects. Tolerate either shape.
function normalizeMcps(raw: unknown): McpStatus[] {
  if (Array.isArray(raw)) return raw as McpStatus[];
  if (raw && typeof raw === "object") {
    return Object.entries(raw as Record<string, unknown>).map(([name, v]) => ({
      name,
      status: (typeof v === "string" ? v : "skipped") as McpStatus["status"],
    }));
  }
  return [];
}

// The runtime.snapshot from the shim is the SDK's JSONL records verbatim:
//   {type:"user",      message:{role,content:string|parts}, uuid, ...}
//   {type:"assistant", message:{role,content:[{type:"text"|"thinking"|"tool_use"|...}]}, uuid, ...}
//   {type:"attachment"|"queue-operation"|"last-prompt", ...}   ← skip
// Tool results come back as a user message whose content array has
// {type:"tool_result", tool_use_id, content, is_error}.
function normalizeConversation(raw: unknown[]): {
  messages: ConversationMessage[];
  toolNames: Record<string, string>;
} {
  const out: ConversationMessage[] = [];
  const toolNames: Record<string, string> = {};
  let i = 0;
  for (const item of raw) {
    i++;
    if (!item || typeof item !== "object") continue;
    const o = item as Record<string, unknown>;
    const recType = typeof o.type === "string" ? (o.type as string) : "";
    if (recType !== "user" && recType !== "assistant") continue;
    const msg =
      o.message && typeof o.message === "object"
        ? (o.message as Record<string, unknown>)
        : null;
    if (!msg) continue;
    const role = typeof msg.role === "string" ? (msg.role as string) : recType;
    const content = msg.content;
    const baseId = typeof o.uuid === "string" ? (o.uuid as string) : `snap-${i}`;

    if (typeof content === "string") {
      if (role === "user") {
        out.push({ id: baseId, kind: "user", text: content });
      } else if (role === "assistant") {
        out.push({ id: baseId, kind: "assistant", text: content });
      }
      continue;
    }
    if (!Array.isArray(content)) continue;

    if (role === "assistant") {
      let textParts = "";
      let partIdx = 0;
      for (const part of content) {
        if (!part || typeof part !== "object") continue;
        const p = part as Record<string, unknown>;
        const t = typeof p.type === "string" ? (p.type as string) : "";
        if (t === "text" && typeof p.text === "string") {
          textParts += textParts ? "\n" + p.text : p.text;
        } else if (t === "tool_use") {
          if (textParts) {
            out.push({ id: `${baseId}-t${partIdx++}`, kind: "assistant", text: textParts });
            textParts = "";
          }
          const toolUseId = typeof p.id === "string" ? (p.id as string) : "";
          const name = typeof p.name === "string" ? (p.name as string) : "?";
          if (toolUseId) toolNames[toolUseId] = name;
          out.push({
            id: toolUseId || `${baseId}-tc${partIdx++}`,
            kind: "tool_call",
            tool: name,
            text: stableStringify(p.input ?? {}),
          });
        }
        // thinking / server-side parts are intentionally ignored.
      }
      if (textParts) {
        out.push({ id: `${baseId}-t${partIdx}`, kind: "assistant", text: textParts });
      }
    } else if (role === "user") {
      let userText = "";
      let partIdx = 0;
      for (const part of content) {
        if (!part || typeof part !== "object") continue;
        const p = part as Record<string, unknown>;
        const t = typeof p.type === "string" ? (p.type as string) : "";
        if (t === "text" && typeof p.text === "string") {
          userText += userText ? "\n" + p.text : p.text;
        } else if (t === "tool_result") {
          if (userText) {
            out.push({ id: `${baseId}-u${partIdx++}`, kind: "user", text: userText });
            userText = "";
          }
          const useId = typeof p.tool_use_id === "string" ? (p.tool_use_id as string) : "";
          const tool = useId && toolNames[useId] ? toolNames[useId] : "";
          out.push({
            id: useId || `${baseId}-tr${partIdx++}`,
            kind: "tool_result",
            tool,
            is_error: !!p.is_error,
            text: typeof p.content === "string" ? p.content : stableStringify(p.content ?? ""),
          });
        }
      }
      if (userText) {
        out.push({ id: `${baseId}-u${partIdx}`, kind: "user", text: userText });
      }
    }
  }
  return { messages: out, toolNames };
}

function stableStringify(v: unknown): string {
  try {
    return JSON.stringify(v, null, 2);
  } catch {
    return String(v);
  }
}

function eventId(e: WireEvent): string | null {
  return (e.event_id ?? e.id ?? null) as string | null;
}

function applyEvent(state: State, e: WireEvent): State {
  const id = eventId(e);
  if (id) {
    if (state.seenEventIds.has(id)) return state;
  }
  let next: State = state;
  switch (e.kind) {
    case "user.message": {
      const d = e.data as { message_id?: string; content?: string };
      next = {
        ...next,
        messages: [
          ...next.messages,
          {
            id: d.message_id ?? `u-${id ?? Math.random()}`,
            kind: "user",
            text: d.content ?? "",
          },
        ],
      };
      break;
    }
    case "turn.start": {
      // Just bump the in-flight counter. Don't pre-create an assistant
      // bubble — the SDK may emit zero or many AssistantMessage records
      // for a turn (tool-use-only messages produce no text). Bubbles are
      // created lazily on the first delta / non-empty assistant.message.
      const count = next.inFlightCount + 1;
      next = { ...next, inFlightCount: count, inFlight: count > 0 };
      break;
    }
    case "assistant.delta": {
      const d = e.data as { turn_id?: string; delta?: string };
      if (!d.turn_id || !d.delta) break;
      const bubbleId = next.openBubbleByTurn[d.turn_id];
      if (!bubbleId) {
        // Open a new bubble for this slice of the turn.
        const newId = `a-${d.turn_id}-${id ?? next.messages.length}`;
        next = {
          ...next,
          openBubbleByTurn: {
            ...next.openBubbleByTurn,
            [d.turn_id]: newId,
          },
          messages: [
            ...next.messages,
            {
              id: newId,
              kind: "assistant",
              text: d.delta,
              inFlight: true,
              turn_id: d.turn_id,
            },
          ],
        };
      } else {
        next = {
          ...next,
          messages: next.messages.map((m) =>
            m.id === bubbleId ? { ...m, text: m.text + (d.delta ?? "") } : m,
          ),
        };
      }
      break;
    }
    case "assistant.message": {
      // Canonical text for one AssistantMessage record. An SDK turn can
      // emit several of these around a tool round-trip; each finalizes
      // the currently-open bubble (if any) and clears the open-bubble
      // pointer so a subsequent delta opens a fresh bubble.
      const d = e.data as { turn_id?: string; content?: string };
      if (!d.turn_id) break;
      const content = d.content ?? "";
      const bubbleId = next.openBubbleByTurn[d.turn_id];
      if (bubbleId) {
        next = {
          ...next,
          messages: next.messages.map((m) =>
            m.id === bubbleId
              ? {
                  ...m,
                  // Empty content (tool-use-only AssistantMessage) keeps
                  // whatever deltas we already accumulated; non-empty
                  // content replaces with the canonical text.
                  text: content !== "" ? content : m.text,
                  inFlight: false,
                }
              : m,
          ),
        };
        // Clear the open-bubble pointer so the next delta in this turn
        // opens a NEW bubble (separate from any tool calls in between).
        const map = { ...next.openBubbleByTurn };
        delete map[d.turn_id];
        next = { ...next, openBubbleByTurn: map };
      } else if (content !== "") {
        // No bubble was open AND the message has text — create a sealed
        // bubble directly. Tool-use-only messages (empty content with no
        // open bubble) are intentionally skipped.
        next = {
          ...next,
          messages: [
            ...next.messages,
            {
              id: `a-${d.turn_id}-${id ?? next.messages.length}`,
              kind: "assistant",
              text: content,
              turn_id: d.turn_id,
            },
          ],
        };
      }
      break;
    }
    case "turn.end":
    case "turn.cancelled": {
      const d = e.data as { turn_id?: string };
      const count = Math.max(0, next.inFlightCount - 1);
      let openMap = next.openBubbleByTurn;
      // Prefer an exact turn_id match; otherwise, when the counter hits 0,
      // seal ALL leftover open bubbles. This handles older shim/daemon
      // builds where turn.start.turn_id != turn.end.turn_id, so an
      // exact-match cleanup would never fire.
      if (d.turn_id && openMap[d.turn_id]) {
        const bubbleId = openMap[d.turn_id];
        next = {
          ...next,
          messages: next.messages.map((m) =>
            m.id === bubbleId ? { ...m, inFlight: false } : m,
          ),
        };
        const m = { ...openMap };
        delete m[d.turn_id];
        openMap = m;
      } else if (count === 0 && Object.keys(openMap).length > 0) {
        const open = new Set(Object.values(openMap));
        next = {
          ...next,
          messages: next.messages.map((m) =>
            open.has(m.id) ? { ...m, inFlight: false } : m,
          ),
        };
        openMap = {};
      }
      next = {
        ...next,
        openBubbleByTurn: openMap,
        inFlightCount: count,
        inFlight: count > 0,
      };
      break;
    }
    case "tool.call": {
      // Shim emits: { turn_id, tool_use_id, name, input }
      // Older payloads may use `tool` instead of `name`; accept both.
      const d = e.data as {
        turn_id?: string;
        tool?: string;
        name?: string;
        tool_use_id?: string;
        input?: unknown;
      };
      const toolName = d.tool ?? d.name ?? "?";
      const useId = d.tool_use_id ?? "";

      // Close any bubble currently open for this turn so the tool call
      // renders AFTER the assistant text that triggered it.
      let openMap = next.openBubbleByTurn;
      if (d.turn_id && openMap[d.turn_id]) {
        const sealed = openMap[d.turn_id];
        next = {
          ...next,
          messages: next.messages.map((m) =>
            m.id === sealed ? { ...m, inFlight: false } : m,
          ),
        };
        const m = { ...openMap };
        delete m[d.turn_id];
        openMap = m;
      }

      const nextToolNames = useId
        ? { ...next.toolNames, [useId]: toolName }
        : next.toolNames;

      next = {
        ...next,
        openBubbleByTurn: openMap,
        toolNames: nextToolNames,
        messages: [
          ...next.messages,
          {
            id: useId ? `tc-${useId}` : `tc-${id ?? Math.random()}`,
            kind: "tool_call",
            tool: toolName,
            text: stableStringify(d.input ?? {}),
            turn_id: d.turn_id,
          },
        ],
      };
      break;
    }
    case "tool.result": {
      // Shim emits: { turn_id, tool_use_id, content, is_error }
      // Older payloads may use `output` and/or `tool`; accept both.
      const d = e.data as {
        turn_id?: string;
        tool?: string;
        tool_use_id?: string;
        output?: unknown;
        content?: unknown;
        is_error?: boolean;
      };
      const useId = d.tool_use_id ?? "";
      const toolName =
        d.tool ?? (useId ? next.toolNames[useId] : undefined) ?? "";
      const body = d.content !== undefined ? d.content : d.output;
      const text =
        typeof body === "string" ? body : stableStringify(body ?? "");
      next = {
        ...next,
        messages: [
          ...next.messages,
          {
            id: useId ? `tr-${useId}` : `tr-${id ?? Math.random()}`,
            kind: "tool_result",
            tool: toolName,
            text,
            is_error: !!d.is_error,
            turn_id: d.turn_id,
          },
        ],
      };
      break;
    }
    case "queue.depth": {
      const d = e.data as { depth?: number };
      next = { ...next, queueDepth: d.depth ?? 0 };
      break;
    }
    case "mcp.unreachable": {
      const d = e.data as { name?: string; error?: string };
      next = {
        ...next,
        warnings: [
          ...next.warnings,
          `MCP ${d.name ?? "?"} unreachable${d.error ? `: ${d.error}` : ""}`,
        ],
        mcps: next.mcps.map((m) =>
          m.name === d.name ? { ...m, status: "unreachable", error: d.error } : m,
        ),
      };
      break;
    }
    case "mcp.skipped": {
      const d = e.data as { name?: string; reason?: string };
      next = {
        ...next,
        warnings: [
          ...next.warnings,
          `MCP ${d.name ?? "?"} skipped${d.reason ? `: ${d.reason}` : ""}`,
        ],
      };
      break;
    }
    case "session.starting":
      next = { ...next, status: "starting" };
      break;
    case "session.running":
    case "session.resumed":
      next = { ...next, status: "running" };
      break;
    case "session.stopping":
      next = { ...next, status: "stopping" };
      break;
    case "session.stopped":
      next = { ...next, status: "stopped", inFlight: false };
      break;
    case "session.terminated":
      next = { ...next, status: "terminated", inFlight: false };
      break;
    case "session.error": {
      const d = e.data as { message?: string; code?: string };
      next = {
        ...next,
        status: "error",
        warnings: [
          ...next.warnings,
          `session error: ${d.code ?? ""} ${d.message ?? ""}`.trim(),
        ],
      };
      break;
    }
    case "runtime.throttled": {
      const d = e.data as { active?: boolean };
      if (d.active) {
        next = {
          ...next,
          warnings: [...next.warnings, "runtime throttled (delta drops)"],
        };
      }
      break;
    }
    default:
      // unknown event kinds are ignored per api.md §5
      break;
  }

  if (id) {
    const seen = new Set(next.seenEventIds);
    seen.add(id);
    next = { ...next, seenEventIds: seen };
  }
  return next;
}

export function SessionDetail() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const [state, dispatch] = useReducer(reducer, INITIAL);
  const [tab, setTab] = useState<"conversation" | "changes">("conversation");
  const [endBusy, setEndBusy] = useState(false);
  const [costRefreshKey, setCostRefreshKey] = useState(0);

  // Initial fetch for session metadata (name, status before snapshot lands).
  useEffect(() => {
    if (!id) return;
    let cancelled = false;
    apiJson<{ session: { name?: string; status?: SessionStatus } }>(
      `/v1/sessions/${encodeURIComponent(id)}`,
    )
      .then((r) => {
        if (cancelled) return;
        if (r?.session) {
          dispatch({
            type: "event",
            e: {
              kind: "session." + (r.session.status ?? "running"),
              data: {},
            } as WireEvent,
          });
        }
      })
      .catch(() => {
        // non-fatal; the WS snapshot will populate state.
      });
    return () => {
      cancelled = true;
    };
  }, [id]);

  useEffect(() => {
    if (!id) return;
    dispatch({ type: "reset" });
    const handle = attach(id, {
      onOpen: () => dispatch({ type: "ws_open" }),
      onDisconnect: (reason) => dispatch({ type: "ws_close", reason }),
      onEvent: (e) => {
        if (e.kind === "session.snapshot") {
          dispatch({ type: "snapshot", data: e.data as SnapshotData });
          return;
        }
        // Frame envelopes from the unix surface (kind=stream_end / error)
        // can also arrive here on the WS in some implementations; ignore.
        if (
          e.kind === "stream_end" ||
          (e as { kind: string }).kind === "error"
        ) {
          return;
        }
        if (
          e.kind === "usage" ||
          e.kind === "turn.end" ||
          e.kind === "turn.cancelled"
        ) {
          setCostRefreshKey((k) => k + 1);
        }
        dispatch({ type: "event", e });
      },
    });
    return () => handle.close();
  }, [id]);

  const onEnd = useCallback(async () => {
    if (!id) return;
    if (!window.confirm("End session? Container, volume, and history will be removed.")) {
      return;
    }
    setEndBusy(true);
    try {
      await apiJson(`/v1/sessions/${encodeURIComponent(id)}`, {
        method: "DELETE",
      });
      navigate("/sessions");
    } catch (err) {
      const msg =
        err instanceof ApiError
          ? `${err.code ?? err.status}: ${err.message}`
          : String(err);
      alert(`Failed to end session: ${msg}`);
      setEndBusy(false);
    }
  }, [id, navigate]);

  const visibleMessages = useMemo(() => state.messages, [state.messages]);

  if (!id) return null;

  return (
    <section className="session-detail">
      <div className="session-header">
        <span className="breadcrumb">
          <Link to="/sessions">Sessions</Link>
          <span aria-hidden>/</span>
        </span>
        <span className="session-title" title={state.name || id}>
          {state.name || id}
        </span>
        <span className={`status-badge ${state.status}`}>{state.status}</span>
        {state.inFlight && (
          <span className="responding-pill" aria-live="polite">
            <span className="bdot" />
            <span className="bdot" />
            <span className="bdot" />
            <span className="label">Responding</span>
          </span>
        )}
        {state.queueDepth > 0 && (
          <span className="queue-pill">{state.queueDepth} queued</span>
        )}
        {!state.connected && state.disconnectReason && (
          <span className="reconnect-pill">
            <span className="streaming-dot" aria-hidden />
            reconnecting…
          </span>
        )}
        <span className="actions">
          <StopButton sessionId={id} inFlight={state.inFlight} />
          <button className="danger" onClick={onEnd} disabled={endBusy}>
            {endBusy ? "Ending…" : "End session"}
          </button>
        </span>
      </div>

      <div
        className={`conv-card${
          state.inFlight && tab === "conversation" ? " in-flight" : ""
        }`}
      >
        <div className="tabs">
          <button
            className={tab === "conversation" ? "active" : ""}
            onClick={() => setTab("conversation")}
          >
            Conversation
          </button>
          <button
            className={tab === "changes" ? "active" : ""}
            onClick={() => setTab("changes")}
          >
            Changes
          </button>
        </div>
        {tab === "conversation" ? (
          <ConversationView
            messages={visibleMessages}
            warnings={state.warnings}
            inFlight={state.inFlight}
          />
        ) : (
          <div className="conversation">
            <ChangesPanel sessionId={id} />
          </div>
        )}
        {tab === "conversation" && (
          <div className="input-area-wrap">
            <MessageInput
              sessionId={id}
              inFlight={state.inFlight}
              queueDepth={state.queueDepth}
            />
          </div>
        )}
      </div>

      <aside className="side">
        <McpPanel mcps={state.mcps} />
        <CostPanel sessionId={id} refreshKey={costRefreshKey} />
      </aside>
    </section>
  );
}
