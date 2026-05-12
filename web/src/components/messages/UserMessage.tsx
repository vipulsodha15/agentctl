import type { ConversationMessage } from "../../types";
import { Markdown } from "./Markdown";

export function UserMessage({ message }: { message: ConversationMessage }) {
  return (
    <div className="msg user" id={`msg-${message.id}`}>
      <div className="avatar">YOU</div>
      <div className="body">
        <div className="role">You</div>
        <div className="content">
          <Markdown text={message.text} />
        </div>
      </div>
    </div>
  );
}
