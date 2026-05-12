import type { ConversationMessage } from "../../types";
import { Markdown } from "./Markdown";

export function AssistantMessage({ message }: { message: ConversationMessage }) {
  return (
    <div className="msg assistant" id={`msg-${message.id}`}>
      <div className="avatar" aria-hidden>
        <svg
          width="15"
          height="15"
          viewBox="0 0 24 24"
          fill="currentColor"
        >
          <path d="M12 2l1.8 5.4L19 9l-5.2 1.6L12 16l-1.8-5.4L5 9l5.2-1.6L12 2z" />
          <path d="M19 14l.9 2.7L22 18l-2.1.6L19 22l-.9-2.7L16 18l2.1-.6L19 14z" opacity="0.7" />
        </svg>
      </div>
      <div className="body">
        <div className="role">
          Assistant
          {message.inFlight && (
            <>
              <span className="streaming-dot" aria-hidden />
              <span className="streaming-label">streaming…</span>
            </>
          )}
        </div>
        <div className="content">
          <Markdown text={message.text} />
        </div>
      </div>
    </div>
  );
}
