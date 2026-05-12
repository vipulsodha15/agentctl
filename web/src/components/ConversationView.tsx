import { useEffect, useRef } from "react";
import type { ConversationMessage } from "../types";

interface Props {
  messages: ConversationMessage[];
  warnings: string[];
  inFlight: boolean;
}

export function ConversationView({ messages, warnings, inFlight }: Props) {
  const scrollRef = useRef<HTMLDivElement>(null);
  const last = messages[messages.length - 1];

  useEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    el.scrollTop = el.scrollHeight;
  }, [messages.length, last?.text, inFlight]);

  // Skip the bottom indicator when the visible bubble is already showing the
  // in-line "streaming…" dot, to avoid two simultaneous "in progress" signals
  // for the same chunk.
  const lastIsStreamingText =
    last !== undefined && last.kind === "assistant" && !!last.inFlight;
  const showTyping = inFlight && !lastIsStreamingText;

  return (
    <div className="conversation" ref={scrollRef}>
      {warnings.map((w, i) => (
        <div key={`w-${i}`} className="warning">
          {w}
        </div>
      ))}
      {messages.length === 0 && !inFlight && (
        <div className="empty">No messages yet. Send one below to start.</div>
      )}
      {messages.map((m) => (
        <MessageRow key={m.id} message={m} />
      ))}
      {showTyping && <TypingIndicator />}
    </div>
  );
}

function TypingIndicator() {
  return (
    <div className="typing-indicator" aria-live="polite" aria-label="Assistant is working">
      <div className="avatar">AI</div>
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

function MessageRow({ message }: { message: ConversationMessage }) {
  if (message.kind === "user") {
    return (
      <div className="msg user">
        <div className="avatar">YOU</div>
        <div className="body">
          <div className="role">You</div>
          <div className="content">{message.text}</div>
        </div>
      </div>
    );
  }
  if (message.kind === "assistant") {
    return (
      <div className="msg assistant">
        <div className="avatar">AI</div>
        <div className="body">
          <div className="role">
            Assistant
            {message.inFlight && (
              <>
                <span className="streaming-dot" aria-hidden />
                <span style={{ fontWeight: 500, color: "var(--c-fg-subtle)" }}>
                  streaming…
                </span>
              </>
            )}
          </div>
          <div className="content">{message.text}</div>
        </div>
      </div>
    );
  }
  if (message.kind === "tool_call") {
    return (
      <div className="msg tool">
        <div className="avatar">T</div>
        <div className="body">
          <div className="role">
            <span className="tool-kind">tool call</span>
            <span className="tool-name">{message.tool ?? "?"}</span>
          </div>
          <details className="tool-card">
            <summary>Input</summary>
            <pre>{message.text}</pre>
          </details>
        </div>
      </div>
    );
  }
  if (message.kind === "tool_result") {
    return (
      <div className="msg tool">
        <div className="avatar">T</div>
        <div className="body">
          <div className="role">
            <span
              className={`tool-kind ${message.is_error ? "tool-error" : ""}`}
            >
              {message.is_error ? "tool error" : "tool result"}
            </span>
            {message.tool && (
              <span className="tool-name">{message.tool}</span>
            )}
          </div>
          <details className="tool-card">
            <summary>Output</summary>
            <pre>{message.text}</pre>
          </details>
        </div>
      </div>
    );
  }
  return (
    <div className="msg">
      <div className="avatar">S</div>
      <div className="body">
        <div className="role">System</div>
        <div className="content">{message.text}</div>
      </div>
    </div>
  );
}
