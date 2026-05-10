import { useEffect, useRef } from "react";
import type { ConversationMessage } from "../types";

interface Props {
  messages: ConversationMessage[];
  warnings: string[];
}

export function ConversationView({ messages, warnings }: Props) {
  const scrollRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    // Pin to bottom when new content arrives. Cheap approximation: always
    // scroll. For a more polished UX we'd track "user scrolled up" state;
    // M3 polish is out of scope.
    el.scrollTop = el.scrollHeight;
  }, [messages.length, messages[messages.length - 1]?.text]);

  return (
    <div className="conversation" ref={scrollRef}>
      {warnings.map((w, i) => (
        <div key={`w-${i}`} className="warning">
          {w}
        </div>
      ))}
      {messages.length === 0 && (
        <div className="empty">No messages yet. Send one below.</div>
      )}
      {messages.map((m) => (
        <Bubble key={m.id} message={m} />
      ))}
    </div>
  );
}

function Bubble({ message }: { message: ConversationMessage }) {
  if (message.kind === "user") {
    return (
      <div className="bubble user">
        <div className="role">you</div>
        {message.text}
      </div>
    );
  }
  if (message.kind === "assistant") {
    return (
      <div className="bubble assistant">
        <div className="role">
          assistant{message.inFlight ? " · streaming…" : ""}
        </div>
        {message.text}
      </div>
    );
  }
  if (message.kind === "tool_call") {
    return (
      <div className="bubble tool">
        <div className="tool-line">tool: {message.tool ?? "?"}</div>
        <details>
          <summary>input</summary>
          <pre>{message.text}</pre>
        </details>
      </div>
    );
  }
  if (message.kind === "tool_result") {
    return (
      <div className="bubble tool">
        <div className="tool-line">
          result{message.is_error ? " (error)" : ""}: {message.tool ?? ""}
        </div>
        <details>
          <summary>output</summary>
          <pre>{message.text}</pre>
        </details>
      </div>
    );
  }
  return (
    <div className="bubble">
      <div className="role">system</div>
      {message.text}
    </div>
  );
}
