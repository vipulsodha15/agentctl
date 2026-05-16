import { useCallback, useEffect, useRef, useState } from "react";
import type { ConversationMessage, McpStatus, UsageTotals } from "../types";
import { UserMessage } from "./messages/UserMessage";
import { AssistantMessage } from "./messages/AssistantMessage";
import { ThinkingBlock } from "./messages/ThinkingBlock";
import { ToolBlock } from "./messages/ToolBlock";
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
}

export type TranscriptFilter = "all" | "errors" | "tools" | "text";

export function ConversationView({
  messages,
  warnings,
  inFlight,
  mcps,
  usageByTurn,
  filter,
}: Props) {
  const scrollRef = useRef<HTMLDivElement>(null);
  const userScrolledUpRef = useRef(false);
  const [atBottom, setAtBottom] = useState(true);
  const [newSinceScroll, setNewSinceScroll] = useState(0);
  const last = messages[messages.length - 1];

  // Auto-scroll on every new message / streaming update unless the user has
  // actively scrolled up to read. Sending a new user message always re-pins
  // to the bottom.
  useEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    if (last?.kind === "user") {
      userScrolledUpRef.current = false;
    }
    if (!userScrolledUpRef.current) {
      el.scrollTop = el.scrollHeight;
      setNewSinceScroll(0);
    } else {
      setNewSinceScroll((n) => n + 1);
    }
    // We intentionally re-run on every message-text change, not just length.
  }, [messages.length, last?.kind, last?.text, last?.output, inFlight]);

  const onScroll = useCallback(() => {
    const el = scrollRef.current;
    if (!el) return;
    const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
    userScrolledUpRef.current = !nearBottom;
    setAtBottom(nearBottom);
    if (nearBottom) setNewSinceScroll(0);
  }, []);

  const visible = filterMessages(messages, filter);
  const lastIsStreamingText =
    last !== undefined && last.kind === "assistant" && !!last.inFlight;
  const showTyping = inFlight && !lastIsStreamingText;
  const groups = groupByTurn(visible);

  const jumpToBottom = () => {
    const el = scrollRef.current;
    if (el) el.scrollTop = el.scrollHeight;
    userScrolledUpRef.current = false;
  };

  return (
    <div className="conversation-wrap">
      <div className="conversation" ref={scrollRef} onScroll={onScroll}>
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
          />
        ))}

        {showTyping && <TypingIndicator />}
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

function TurnGroup({
  group,
  mcps,
  usage,
  isLast,
}: {
  group: Group;
  mcps: McpStatus[];
  usage: UsageTotals | undefined;
  isLast: boolean;
}) {
  return (
    <div className="turn-group">
      {group.items.map((m) => (
        <MessageRow key={m.id} message={m} mcps={mcps} />
      ))}
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
}: {
  message: ConversationMessage;
  mcps: McpStatus[];
}) {
  switch (message.kind) {
    case "user":
      return <UserMessage message={message} />;
    case "assistant":
      return <AssistantMessage message={message} />;
    case "thinking":
      return <ThinkingBlock message={message} />;
    case "tool":
      return <ToolBlock message={message} mcps={mcps} />;
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
