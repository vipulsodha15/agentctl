import { useCallback, useEffect, useMemo, useReducer, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { ApiError, apiJson } from "../api";
import { attach } from "../ws";
import type {
  ConversationMessage,
  McpStatus,
  SessionStatus,
  SnapshotData,
  UsageTotals,
  WireEvent,
} from "../types";
import { ConversationView, type TranscriptFilter } from "../components/ConversationView";
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
  // tool_use_id → message index, so tool.result can update the same row
  // instead of appending a separate one.
  toolIndexById: Record<string, number>;
  // Per-turn aggregated usage, surfaced as a chip on the turn divider.
  usageByTurn: Record<string, UsageTotals>;
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
  toolIndexById: {},
  usageByTurn: {},
  connected: false,
  disconnectReason: null,
};

function reducer(state: State, action: Action): State {
  switch (action.type) {
    case "reset":
      return { ...INITIAL, seenEventIds: new Set() };
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
  const { messages, toolNames, toolIndexById } = normalizeConversation(convArray);
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
    toolIndexById,
    usageByTurn: {},
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
//
// We pair tool_use parts with their later tool_result parts (keyed by
// tool_use_id) so the rendered transcript carries a single "tool" entry per
// call instead of two separate rows.
function normalizeConversation(raw: unknown[]): {
  messages: ConversationMessage[];
  toolNames: Record<string, string>;
  toolIndexById: Record<string, number>;
} {
  const out: ConversationMessage[] = [];
  const toolNames: Record<string, string> = {};
  const toolIndexById: Record<string, number> = {};
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
      const flushText = () => {
        if (textParts) {
          out.push({
            id: `${baseId}-t${partIdx++}`,
            kind: "assistant",
            text: textParts,
          });
          textParts = "";
        }
      };
      for (const part of content) {
        if (!part || typeof part !== "object") continue;
        const p = part as Record<string, unknown>;
        const t = typeof p.type === "string" ? (p.type as string) : "";
        if (t === "text" && typeof p.text === "string") {
          textParts += textParts ? "\n" + p.text : p.text;
        } else if (t === "thinking" || t === "redacted_thinking") {
          flushText();
          const tx =
            (typeof p.thinking === "string" && p.thinking) ||
            (typeof p.text === "string" && p.text) ||
            "";
          if (tx) {
            out.push({
              id: `${baseId}-th${partIdx++}`,
              kind: "thinking",
              text: tx,
            });
          }
        } else if (t === "tool_use") {
          flushText();
          const toolUseId = typeof p.id === "string" ? (p.id as string) : "";
          const name = typeof p.name === "string" ? (p.name as string) : "?";
          if (toolUseId) toolNames[toolUseId] = name;
          const id = toolUseId || `${baseId}-tc${partIdx++}`;
          if (toolUseId) toolIndexById[toolUseId] = out.length;
          out.push({
            id,
            kind: "tool",
            tool: name,
            tool_use_id: toolUseId || undefined,
            input: p.input ?? {},
            text: stableStringify(p.input ?? {}),
            status: "pending",
          });
        }
      }
      flushText();
    } else if (role === "user") {
      let userText = "";
      let partIdx = 0;
      const flushText = () => {
        if (userText) {
          out.push({
            id: `${baseId}-u${partIdx++}`,
            kind: "user",
            text: userText,
          });
          userText = "";
        }
      };
      for (const part of content) {
        if (!part || typeof part !== "object") continue;
        const p = part as Record<string, unknown>;
        const t = typeof p.type === "string" ? (p.type as string) : "";
        if (t === "text" && typeof p.text === "string") {
          userText += userText ? "\n" + p.text : p.text;
        } else if (t === "tool_result") {
          flushText();
          const useId =
            typeof p.tool_use_id === "string" ? (p.tool_use_id as string) : "";
          const isErr = !!p.is_error;
          const outputText =
            typeof p.content === "string"
              ? p.content
              : stableStringify(p.content ?? "");
          // Prefer to fold this result into the existing tool_use row.
          if (useId && toolIndexById[useId] !== undefined) {
            const idx = toolIndexById[useId];
            const prev = out[idx];
            out[idx] = {
              ...prev,
              output: outputText,
              is_error: isErr,
              status: isErr ? "error" : "done",
            };
          } else {
            // Result without a matching call — render as a standalone tool row
            // so the user can still see what happened.
            const tool = useId && toolNames[useId] ? toolNames[useId] : "";
            const id = useId || `${baseId}-tr${partIdx++}`;
            out.push({
              id,
              kind: "tool",
              tool,
              tool_use_id: useId || undefined,
              input: undefined,
              text: "",
              output: outputText,
              is_error: isErr,
              status: isErr ? "error" : "done",
            });
            if (useId) toolIndexById[useId] = out.length - 1;
          }
        }
      }
      flushText();
    }
  }
  return { messages: out, toolNames, toolIndexById };
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
      // Mark any tool rows still pending as cancelled-as-done so they don't
      // spin forever after a cancellation.
      if (e.kind === "turn.cancelled") {
        next = {
          ...next,
          messages: next.messages.map((m) =>
            m.kind === "tool" && m.status === "pending"
              ? { ...m, status: "done" }
              : m,
          ),
        };
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

      const rowId = useId ? `tc-${useId}` : `tc-${id ?? Math.random()}`;
      const idx = next.messages.length;
      next = {
        ...next,
        openBubbleByTurn: openMap,
        toolNames: nextToolNames,
        toolIndexById: useId
          ? { ...next.toolIndexById, [useId]: idx }
          : next.toolIndexById,
        messages: [
          ...next.messages,
          {
            id: rowId,
            kind: "tool",
            tool: toolName,
            tool_use_id: useId || undefined,
            input: d.input ?? {},
            text: stableStringify(d.input ?? {}),
            status: "pending",
            started_at: Date.now(),
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
      const body = d.content !== undefined ? d.content : d.output;
      const outputText =
        typeof body === "string" ? body : stableStringify(body ?? "");
      const idx = useId ? next.toolIndexById[useId] : undefined;
      const isErr = !!d.is_error;
      if (idx !== undefined && next.messages[idx]?.kind === "tool") {
        // Fold the result into the existing pending row.
        next = {
          ...next,
          messages: next.messages.map((m, i) =>
            i === idx
              ? {
                  ...m,
                  output: outputText,
                  is_error: isErr,
                  status: isErr ? "error" : "done",
                  ended_at: Date.now(),
                }
              : m,
          ),
        };
      } else {
        const tool =
          d.tool ?? (useId ? next.toolNames[useId] : undefined) ?? "";
        const rowId = useId ? `tr-${useId}` : `tr-${id ?? Math.random()}`;
        next = {
          ...next,
          messages: [
            ...next.messages,
            {
              id: rowId,
              kind: "tool",
              tool,
              tool_use_id: useId || undefined,
              input: undefined,
              text: "",
              output: outputText,
              is_error: isErr,
              status: isErr ? "error" : "done",
              ended_at: Date.now(),
              turn_id: d.turn_id,
            },
          ],
        };
        if (useId) {
          next = {
            ...next,
            toolIndexById: {
              ...next.toolIndexById,
              [useId]: next.messages.length - 1,
            },
          };
        }
      }
      break;
    }
    case "usage": {
      const d = e.data as {
        turn_id?: string;
        input_tokens?: number;
        output_tokens?: number;
        cache_read_tokens?: number;
        cache_write_tokens?: number;
        cost_usd?: number;
      };
      if (!d.turn_id) break;
      const prev: UsageTotals = next.usageByTurn[d.turn_id] ?? {
        input_tokens: 0,
        output_tokens: 0,
        cache_read_tokens: 0,
        cache_write_tokens: 0,
      };
      next = {
        ...next,
        usageByTurn: {
          ...next.usageByTurn,
          [d.turn_id]: {
            input_tokens: prev.input_tokens + (d.input_tokens ?? 0),
            output_tokens: prev.output_tokens + (d.output_tokens ?? 0),
            cache_read_tokens:
              prev.cache_read_tokens + (d.cache_read_tokens ?? 0),
            cache_write_tokens:
              prev.cache_write_tokens + (d.cache_write_tokens ?? 0),
            cost_usd:
              (prev.cost_usd ?? 0) + (typeof d.cost_usd === "number" ? d.cost_usd : 0),
          },
        },
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
      const text = `MCP ${d.name ?? "?"} unreachable${d.error ? `: ${d.error}` : ""}`;
      next = {
        ...next,
        warnings: [...next.warnings, text],
        messages: [
          ...next.messages,
          {
            id: `n-mcp-${id ?? Math.random()}`,
            kind: "notice",
            text,
            notice_level: "error",
          },
        ],
        mcps: next.mcps.map((m) =>
          m.name === d.name ? { ...m, status: "unreachable", error: d.error } : m,
        ),
      };
      break;
    }
    case "mcp.skipped": {
      const d = e.data as { name?: string; reason?: string };
      const text = `MCP ${d.name ?? "?"} skipped${d.reason ? `: ${d.reason}` : ""}`;
      next = {
        ...next,
        warnings: [...next.warnings, text],
        messages: [
          ...next.messages,
          {
            id: `n-mcps-${id ?? Math.random()}`,
            kind: "notice",
            text,
            notice_level: "warn",
          },
        ],
      };
      break;
    }
    case "skills.changed": {
      // Inline notice — the right-rail Skills panel (if any) carries the
      // canonical list, but transcripts benefit from a marker so audit
      // trails can see when the skill set shifted mid-conversation.
      const d = e.data as { added?: string[]; removed?: string[] };
      const parts: string[] = [];
      if (Array.isArray(d.added) && d.added.length > 0) {
        parts.push(`+${d.added.join(", ")}`);
      }
      if (Array.isArray(d.removed) && d.removed.length > 0) {
        parts.push(`-${d.removed.join(", ")}`);
      }
      next = {
        ...next,
        messages: [
          ...next.messages,
          {
            id: `n-sk-${id ?? Math.random()}`,
            kind: "notice",
            text: `Skills updated${parts.length ? ` (${parts.join(" ")})` : ""}`,
            notice_level: "info",
          },
        ],
      };
      break;
    }
    case "skill.collision": {
      const d = e.data as { name?: string; overrides?: string };
      next = {
        ...next,
        messages: [
          ...next.messages,
          {
            id: `n-skc-${id ?? Math.random()}`,
            kind: "notice",
            text: `Skill "${d.name ?? "?"}" overrides ${d.overrides ?? "another"}`,
            notice_level: "warn",
          },
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
      const text = `session error: ${d.code ?? ""} ${d.message ?? ""}`.trim();
      next = {
        ...next,
        status: "error",
        warnings: [...next.warnings, text],
        messages: [
          ...next.messages,
          {
            id: `n-err-${id ?? Math.random()}`,
            kind: "notice",
            text,
            notice_level: "error",
          },
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
  const [filter, setFilter] = useState<TranscriptFilter>("all");

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
          {tab === "conversation" && (
            <div className="filter-bar">
              <FilterButton current={filter} value="all" onSet={setFilter}>
                All
              </FilterButton>
              <FilterButton current={filter} value="text" onSet={setFilter}>
                Text
              </FilterButton>
              <FilterButton current={filter} value="tools" onSet={setFilter}>
                Tools
              </FilterButton>
              <FilterButton current={filter} value="errors" onSet={setFilter}>
                Errors
              </FilterButton>
            </div>
          )}
        </div>
        {tab === "conversation" ? (
          <ConversationView
            messages={visibleMessages}
            warnings={state.warnings}
            inFlight={state.inFlight}
            mcps={state.mcps}
            usageByTurn={state.usageByTurn}
            filter={filter}
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

function FilterButton({
  current,
  value,
  onSet,
  children,
}: {
  current: TranscriptFilter;
  value: TranscriptFilter;
  onSet: (v: TranscriptFilter) => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      className={`filter-btn ${current === value ? "active" : ""}`}
      onClick={() => onSet(value)}
    >
      {children}
    </button>
  );
}
