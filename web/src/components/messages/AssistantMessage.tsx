import type { ConversationMessage } from "../../types";
import { Markdown } from "./Markdown";

export function AssistantMessage({ message }: { message: ConversationMessage }) {
  return (
    <div className="msg assistant" id={`msg-${message.id}`}>
      <div className="avatar" aria-hidden>a</div>
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
