import { useEffect, useState } from "react";
import type { ConversationMessage, McpStatus, ToolStatus } from "../../types";
import { ToolBlock } from "./ToolBlock";

interface Props {
  items: ConversationMessage[];
  mcps: McpStatus[];
}

function statusOf(m: ConversationMessage): ToolStatus {
  return m.status ?? (m.is_error ? "error" : "done");
}

export function ToolGroup({ items, mcps }: Props) {
  const total = items.length;
  const pending = items.filter((m) => statusOf(m) === "pending").length;
  const errors = items.filter((m) => statusOf(m) === "error").length;
  const done = total - pending;

  // Auto-open while any tool is still running so the user can watch
  // progress; once everything completes we leave the state alone so a
  // user-driven toggle isn't overridden by streaming activity.
  const [open, setOpen] = useState(pending > 0);
  useEffect(() => {
    if (pending > 0) setOpen(true);
  }, [pending]);

  const status: ToolStatus =
    pending > 0 ? "pending" : errors > 0 ? "error" : "done";

  return (
    <div
      className={`msg tool-group tool-group-${status}`}
      id={`msg-group-${items[0].id}`}
    >
      <div className="avatar" aria-hidden>
        ⚙
      </div>
      <div className="body">
        <button
          type="button"
          className="tool-header tool-group-header"
          onClick={() => setOpen((v) => !v)}
          aria-expanded={open}
        >
          <span className={`tool-chevron ${open ? "open" : ""}`} aria-hidden>
            ▶
          </span>
          <span className="tool-verb">
            {total} tool {total === 1 ? "call" : "calls"}
          </span>
          <span className="tool-group-progress" aria-hidden>
            <span className="tool-group-pip tool-group-pip-done" style={{ flex: done }} />
            {errors > 0 && (
              <span className="tool-group-pip tool-group-pip-error" style={{ flex: errors }} />
            )}
            {pending > 0 && (
              <span className="tool-group-pip tool-group-pip-pending" style={{ flex: pending }} />
            )}
          </span>
          <GroupChip status={status} done={done} total={total} errors={errors} />
        </button>

        {open && (
          <div className="tool-group-list">
            {items.map((m) => (
              <ToolBlock key={m.id} message={m} mcps={mcps} />
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

function GroupChip({
  status,
  done,
  total,
  errors,
}: {
  status: ToolStatus;
  done: number;
  total: number;
  errors: number;
}) {
  if (status === "pending") {
    return (
      <span className="status-chip pending" title="Running">
        <span className="streaming-dot" aria-hidden /> {done}/{total}
      </span>
    );
  }
  if (status === "error") {
    return (
      <span className="status-chip error" title={`${errors} failed`}>
        ✗ {errors} failed
      </span>
    );
  }
  return (
    <span className="status-chip done" title="All completed">
      ✓ {total} done
    </span>
  );
}
