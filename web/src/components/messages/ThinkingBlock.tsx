import type { ConversationMessage } from "../../types";

// Approximate token count from word count. The real number is reported via
// usage events but isn't attached to thinking text directly, so this gives a
// reader the relative magnitude without misrepresenting it as exact.
function approxTokens(text: string): number {
  if (!text) return 0;
  const words = text.trim().split(/\s+/).length;
  return Math.round(words * 1.3);
}

export function ThinkingBlock({ message }: { message: ConversationMessage }) {
  const tokens = approxTokens(message.text);
  return (
    <div className="msg thinking" id={`msg-${message.id}`}>
      <div className="avatar" title="Internal reasoning">💭</div>
      <div className="body">
        <details className="thinking-card">
          <summary>
            <span className="thinking-label">Thought</span>
            <span className="thinking-meta">
              ~{tokens.toLocaleString()} tokens
            </span>
          </summary>
          <div className="thinking-body">{message.text}</div>
        </details>
      </div>
    </div>
  );
}
