import type { ConversationMessage } from "../../types";

export function SystemNotice({ message }: { message: ConversationMessage }) {
  const level = message.notice_level ?? "info";
  return (
    <div
      className={`msg notice notice-${level}`}
      id={`msg-${message.id}`}
      role="status"
    >
      <div className="avatar" aria-hidden>
        {level === "error" ? "!" : level === "warn" ? "!" : "i"}
      </div>
      <div className="body">
        <div className="notice-text">{message.text}</div>
      </div>
    </div>
  );
}
