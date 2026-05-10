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
  // The id of the in-flight assistant bubble (keyed by turn_id) so we can
  // replace it with the canonical assistant.message at turn end.
  inFlightAssistantByTurn: Record<string, string>;
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
  inFlightAssistantByTurn: {},
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
  const messages = normalizeConversation(data.conversation ?? []);
  return {
    ...state,
    status: data.session?.status ?? state.status,
    name: data.session?.name ?? state.name,
    inFlight: !!data.in_flight,
    queueDepth: data.queue_depth ?? 0,
    mcps: data.mcps_status ?? [],
    messages,
    warnings: [],
    seenEventIds: new Set(),
    inFlightAssistantByTurn: {},
  };
}

// The runtime.snapshot from the shim is opaque to agentd, but for rendering
// we look for common fields. The structure is best-effort; unknown items are
// skipped.
function normalizeConversation(raw: unknown[]): ConversationMessage[] {
  const out: ConversationMessage[] = [];
  let i = 0;
  for (const item of raw) {
    if (!item || typeof item !== "object") continue;
    const o = item as Record<string, unknown>;
    const role = typeof o.role === "string" ? o.role : undefined;
    const text = stringContent(o.content) ?? stringContent(o.text);
    if (role === "user" && text != null) {
      out.push({
        id: typeof o.id === "string" ? o.id : `snap-u-${i}`,
        kind: "user",
        text,
      });
    } else if (role === "assistant" && text != null) {
      out.push({
        id: typeof o.id === "string" ? o.id : `snap-a-${i}`,
        kind: "assistant",
        text,
      });
    } else if (
      typeof o.tool === "string" &&
      (o.input !== undefined || o.output !== undefined)
    ) {
      // Tool entries (best-effort).
      if (o.input !== undefined) {
        out.push({
          id: `snap-tc-${i}`,
          kind: "tool_call",
          tool: o.tool,
          text: stableStringify(o.input),
        });
      }
      if (o.output !== undefined) {
        out.push({
          id: `snap-tr-${i}`,
          kind: "tool_result",
          tool: o.tool,
          is_error: !!o.is_error,
          text: stableStringify(o.output),
        });
      }
    }
    i++;
  }
  return out;
}

function stringContent(v: unknown): string | null {
  if (typeof v === "string") return v;
  if (Array.isArray(v)) {
    // Common SDK shape: array of {type:"text", text:"..."}.
    const parts: string[] = [];
    for (const part of v) {
      if (part && typeof part === "object") {
        const p = part as Record<string, unknown>;
        if (typeof p.text === "string") parts.push(p.text);
      }
    }
    if (parts.length > 0) return parts.join("");
  }
  return null;
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
      const d = e.data as { turn_id?: string };
      if (d.turn_id) {
        const bubbleId = `a-${d.turn_id}`;
        if (!next.inFlightAssistantByTurn[d.turn_id]) {
          next = {
            ...next,
            inFlight: true,
            inFlightAssistantByTurn: {
              ...next.inFlightAssistantByTurn,
              [d.turn_id]: bubbleId,
            },
            messages: [
              ...next.messages,
              {
                id: bubbleId,
                kind: "assistant",
                text: "",
                inFlight: true,
                turn_id: d.turn_id,
              },
            ],
          };
        } else {
          next = { ...next, inFlight: true };
        }
      } else {
        next = { ...next, inFlight: true };
      }
      break;
    }
    case "assistant.delta": {
      const d = e.data as { turn_id?: string; delta?: string };
      if (!d.turn_id || !d.delta) break;
      const bubbleId = next.inFlightAssistantByTurn[d.turn_id];
      if (!bubbleId) {
        // turn.start was not seen; create a bubble lazily.
        const newId = `a-${d.turn_id}`;
        next = {
          ...next,
          inFlightAssistantByTurn: {
            ...next.inFlightAssistantByTurn,
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
      // Final canonical text — replaces accumulated deltas.
      const d = e.data as { turn_id?: string; content?: string };
      if (!d.turn_id) break;
      const bubbleId = next.inFlightAssistantByTurn[d.turn_id];
      if (bubbleId) {
        next = {
          ...next,
          messages: next.messages.map((m) =>
            m.id === bubbleId
              ? { ...m, text: d.content ?? m.text, inFlight: false }
              : m,
          ),
        };
      } else {
        next = {
          ...next,
          messages: [
            ...next.messages,
            {
              id: `a-${d.turn_id}`,
              kind: "assistant",
              text: d.content ?? "",
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
      const newMap = { ...next.inFlightAssistantByTurn };
      if (d.turn_id) {
        const bubbleId = newMap[d.turn_id];
        if (bubbleId) {
          next = {
            ...next,
            messages: next.messages.map((m) =>
              m.id === bubbleId ? { ...m, inFlight: false } : m,
            ),
          };
        }
        delete newMap[d.turn_id];
      }
      next = {
        ...next,
        inFlight: Object.keys(newMap).length > 0,
        inFlightAssistantByTurn: newMap,
      };
      break;
    }
    case "tool.call": {
      const d = e.data as {
        turn_id?: string;
        tool?: string;
        input?: unknown;
      };
      next = {
        ...next,
        messages: [
          ...next.messages,
          {
            id: `tc-${id ?? Math.random()}`,
            kind: "tool_call",
            tool: d.tool ?? "?",
            text: stableStringify(d.input ?? {}),
            turn_id: d.turn_id,
          },
        ],
      };
      break;
    }
    case "tool.result": {
      const d = e.data as {
        turn_id?: string;
        tool?: string;
        output?: unknown;
        is_error?: boolean;
      };
      next = {
        ...next,
        messages: [
          ...next.messages,
          {
            id: `tr-${id ?? Math.random()}`,
            kind: "tool_result",
            tool: d.tool ?? "",
            text: stableStringify(d.output ?? ""),
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
      <div className="header">
        <Link to="/sessions">← Sessions</Link>
        <strong>{state.name || id}</strong>
        <span className={`status-badge ${state.status}`}>{state.status}</span>
        {state.queueDepth > 0 && (
          <span style={{ color: "var(--fg-mute)", fontSize: 13 }}>
            {state.queueDepth} queued
          </span>
        )}
        {!state.connected && state.disconnectReason && (
          <span className="warning" style={{ margin: 0 }}>
            reconnecting… ({state.disconnectReason})
          </span>
        )}
        <span style={{ marginLeft: "auto", display: "flex", gap: 8 }}>
          <StopButton sessionId={id} inFlight={state.inFlight} />
          <button className="danger" onClick={onEnd} disabled={endBusy}>
            {endBusy ? "Ending…" : "End session"}
          </button>
        </span>
      </div>

      <div className="conversation">
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
          />
        ) : (
          <ChangesPanel sessionId={id} />
        )}
      </div>

      <div className="input-area">
        <MessageInput
          sessionId={id}
          inFlight={state.inFlight}
          queueDepth={state.queueDepth}
        />
      </div>

      <aside className="side">
        <McpPanel mcps={state.mcps} />
        <CostPanel sessionId={id} refreshKey={costRefreshKey} />
      </aside>
    </section>
  );
}
