import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import type { ConversationMessage, McpStatus, UsageTotals } from "../types";
import { UserMessage } from "./messages/UserMessage";
import { AssistantMessage } from "./messages/AssistantMessage";
import { ThinkingBlock } from "./messages/ThinkingBlock";
import { ToolBlock } from "./messages/ToolBlock";
import { ToolGroup } from "./messages/ToolGroup";
import { SystemNotice } from "./messages/SystemNotice";

interface Props {
  messages: ConversationMessage[];
  warnings: string[];
  inFlight: boolean;
  mcps: McpStatus[];
  // Per-turn aggregated usage (tokens + cost). Rendered as a chip at the
  // turn boundary.
  usageByTurn: Record<string, UsageTotals>;
  filter: TranscriptFilter;
  // Session id is plumbed through so interactive tool cards (today:
  // AskUserQuestion) can POST a follow-up user message back to this session.
  sessionId?: string;
}

export type TranscriptFilter = "all" | "errors" | "tools" | "text";

export function ConversationView({
  messages,
  warnings,
  inFlight,
  mcps,
  usageByTurn,
  filter,
  sessionId,
}: Props) {
  const scrollRef = useRef<HTMLDivElement>(null);
  const contentRef = useRef<HTMLDivElement>(null);
  const userScrolledUpRef = useRef(false);
  // Previous scrollTop, used to distinguish a real user scroll-up (scrollTop
  // decreased) from content-growth side effects. A programmatic
  // scroll-to-bottom can only increase scrollTop, so a decrease is an
  // unambiguous signal that the user grabbed the scrollbar / wheeled up.
  const lastScrollTopRef = useRef(0);
  const [atBottom, setAtBottom] = useState(true);
  const [newSinceScroll, setNewSinceScroll] = useState(0);
  const last = messages[messages.length - 1];

  // The scroll container sets `scroll-behavior: smooth` in CSS, which would
  // make even a direct `scrollTop = X` assignment animate and lag behind
  // streaming deltas. Always pass `behavior: "instant"` so we land in one go.
  const pinToBottom = (el: HTMLDivElement) => {
    el.scrollTo({ top: el.scrollHeight, behavior: "instant" as ScrollBehavior });
    lastScrollTopRef.current = el.scrollTop;
  };

  // Sending a new user message always re-pins to the bottom.
  useLayoutEffect(() => {
    if (last?.kind !== "user") return;
    userScrolledUpRef.current = false;
    const el = scrollRef.current;
    if (el) pinToBottom(el);
    setNewSinceScroll(0);
    setAtBottom(true);
  }, [last?.id, last?.kind]);

  // Auto-scroll on any content growth — covers streaming text deltas, tool
  // output arriving on earlier messages, thinking blocks expanding, etc.
  // Driving off ResizeObserver instead of a message-shape dep list means we
  // pin the bottom for every kind of update without enumerating each case.
  useEffect(() => {
    const scroller = scrollRef.current;
    const content = contentRef.current;
    if (!scroller || !content) return;
    const observer = new ResizeObserver(() => {
      if (!userScrolledUpRef.current) {
        pinToBottom(scroller);
        setNewSinceScroll(0);
      } else {
        setNewSinceScroll((n) => n + 1);
      }
    });
    observer.observe(content);
    return () => observer.disconnect();
  }, []);

  const onScroll = useCallback(() => {
    const el = scrollRef.current;
    if (!el) return;
    const prev = lastScrollTopRef.current;
    const curr = el.scrollTop;
    lastScrollTopRef.current = curr;
    const nearBottom = el.scrollHeight - curr - el.clientHeight < 24;
    if (curr < prev - 1) {
      userScrolledUpRef.current = true;
    }
    if (nearBottom) {
      userScrolledUpRef.current = false;
      setNewSinceScroll(0);
    }
    setAtBottom(nearBottom);
  }, []);

  const visible = filterMessages(messages, filter);
  const lastIsStreamingText =
    last !== undefined && last.kind === "assistant" && !!last.inFlight;
  const showTyping = inFlight && !lastIsStreamingText;
  const groups = groupByTurn(visible);

  const jumpToBottom = () => {
    const el = scrollRef.current;
    if (el) pinToBottom(el);
    userScrolledUpRef.current = false;
    setNewSinceScroll(0);
  };

  return (
    <div className="conversation-wrap">
      <div className="conversation" ref={scrollRef} onScroll={onScroll}>
        <div ref={contentRef}>
          {warnings.map((w, i) => (
            <div key={`w-${i}`} className="warning">
              {w}
            </div>
          ))}
          {messages.length === 0 && !inFlight && (
            <div className="empty">No messages yet. Send one below to start.</div>
          )}

          {groups.map((g, gi) => (
            <TurnGroup
              key={g.key}
              group={g}
              mcps={mcps}
              usage={g.turn_id ? usageByTurn[g.turn_id] : undefined}
              isLast={gi === groups.length - 1}
              sessionId={sessionId}
            />
          ))}

          {showTyping && <TypingIndicator />}
        </div>
      </div>
      {!atBottom && newSinceScroll > 0 && (
        <button
          type="button"
          className="jump-pill"
          onClick={jumpToBottom}
          aria-label="Jump to latest"
        >
          ↓ {newSinceScroll} new
        </button>
      )}
    </div>
  );
}

function filterMessages(
  messages: ConversationMessage[],
  filter: TranscriptFilter,
): ConversationMessage[] {
  if (filter === "all") return messages;
  return messages.filter((m) => {
    switch (filter) {
      case "errors":
        return (
          m.is_error === true ||
          m.status === "error" ||
          m.notice_level === "error"
        );
      case "tools":
        return m.kind === "tool";
      case "text":
        return m.kind === "user" || m.kind === "assistant";
      default:
        return true;
    }
  });
}

interface Group {
  key: string;
  turn_id?: string;
  items: ConversationMessage[];
}

function groupByTurn(messages: ConversationMessage[]): Group[] {
  // We split the transcript into "turns" so we can render a divider + a
  // per-turn cost chip. A user message starts a new group; messages without
  // a turn_id (e.g. early system notices) collapse into the previous group.
  const out: Group[] = [];
  let current: Group | null = null;
  for (const m of messages) {
    if (m.kind === "user" || current === null) {
      current = { key: m.id, turn_id: m.turn_id, items: [m] };
      out.push(current);
      continue;
    }
    if (m.turn_id && current.turn_id && m.turn_id !== current.turn_id) {
      current = { key: m.id, turn_id: m.turn_id, items: [m] };
      out.push(current);
      continue;
    }
    if (m.turn_id && !current.turn_id) {
      current.turn_id = m.turn_id;
    }
    current.items.push(m);
  }
  return out;
}

type RenderRow =
  | { kind: "message"; message: ConversationMessage }
  | { kind: "tool-group"; key: string; items: ConversationMessage[] };

function collapseToolRuns(items: ConversationMessage[]): RenderRow[] {
  // Merge consecutive tool messages into a single render row so the
  // transcript shows one collapsible group per run instead of one bubble
  // per call. Runs of length 1 fall back to the unwrapped ToolBlock to
  // avoid adding a header where there's nothing to group.
  const out: RenderRow[] = [];
  let buf: ConversationMessage[] = [];
  const flush = () => {
    if (buf.length === 0) return;
    if (buf.length === 1) {
      out.push({ kind: "message", message: buf[0] });
    } else {
      out.push({ kind: "tool-group", key: `tg-${buf[0].id}`, items: buf });
    }
    buf = [];
  };
  for (const m of items) {
    if (m.kind === "tool") {
      buf.push(m);
    } else {
      flush();
      out.push({ kind: "message", message: m });
    }
  }
  flush();
  return out;
}

function TurnGroup({
  group,
  mcps,
  usage,
  isLast,
  sessionId,
}: {
  group: Group;
  mcps: McpStatus[];
  usage: UsageTotals | undefined;
  isLast: boolean;
  sessionId?: string;
}) {
  const rows = collapseToolRuns(group.items);
  return (
    <div className="turn-group">
      {rows.map((row) =>
        row.kind === "message" ? (
          <MessageRow
            key={row.message.id}
            message={row.message}
            mcps={mcps}
            sessionId={sessionId}
          />
        ) : (
          <ToolGroup
            key={row.key}
            items={row.items}
            mcps={mcps}
            sessionId={sessionId}
          />
        ),
      )}
      {(usage || (!isLast && group.items.length > 0)) && (
        <div className="turn-divider">
          {usage && (
            <span className="turn-cost" title="Tokens and cost for this turn">
              <span className="turn-cost-tokens">
                {(usage.input_tokens + usage.output_tokens).toLocaleString()}
                <span className="turn-cost-unit"> tok</span>
              </span>
              {typeof usage.cost_usd === "number" && (
                <span className="turn-cost-money">
                  ${usage.cost_usd.toFixed(4)}
                </span>
              )}
            </span>
          )}
        </div>
      )}
    </div>
  );
}

function MessageRow({
  message,
  mcps,
  sessionId,
}: {
  message: ConversationMessage;
  mcps: McpStatus[];
  sessionId?: string;
}) {
  switch (message.kind) {
    case "user":
      return <UserMessage message={message} />;
    case "assistant":
      return <AssistantMessage message={message} />;
    case "thinking":
      return <ThinkingBlock message={message} />;
    case "tool":
      return <ToolBlock message={message} mcps={mcps} sessionId={sessionId} />;
    case "notice":
      return <SystemNotice message={message} />;
    default:
      return (
        <div className="msg" id={`msg-${message.id}`}>
          <div className="avatar">S</div>
          <div className="body">
            <div className="role">System</div>
            <div className="content">{message.text}</div>
          </div>
        </div>
      );
  }
}

function TypingIndicator() {
  return (
    <div
      className="typing-indicator"
      aria-live="polite"
      aria-label="Assistant is working"
    >
      <div className="avatar" aria-hidden>a</div>
      <div className="body">
        <div className="role">Assistant</div>
        <div className="typing-pill">
          <span className="bdot" />
          <span className="bdot" />
          <span className="bdot" />
          <span className="label">Working…</span>
        </div>
      </div>
    </div>
  );
}
