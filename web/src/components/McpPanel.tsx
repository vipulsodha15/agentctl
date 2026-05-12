import type { McpStatus } from "../types";

interface Props {
  mcps: McpStatus[];
}

export function McpPanel({ mcps }: Props) {
  return (
    <div className="panel">
      <h3>MCP Servers</h3>
      {mcps.length === 0 ? (
        <div className="empty">No MCP servers attached.</div>
      ) : (
        mcps.map((m) => (
          <div className="mcp-row" key={m.name}>
            <span className="mcp-name">
              <StatusDot status={m.status} />
              {m.name}
            </span>
            <span className={`status-badge ${badgeClass(m.status)}`}>
              {m.status}
            </span>
          </div>
        ))
      )}
    </div>
  );
}

function badgeClass(status: McpStatus["status"]): string {
  if (status === "ok") return "ok";
  if (status === "unreachable") return "unreachable";
  return "skipped";
}

function StatusDot({ status }: { status: McpStatus["status"] }) {
  const color =
    status === "ok"
      ? "var(--c-success)"
      : status === "unreachable"
        ? "var(--c-err)"
        : "var(--c-fg-subtle)";
  return (
    <span
      aria-hidden
      style={{
        display: "inline-block",
        width: 8,
        height: 8,
        borderRadius: "50%",
        background: color,
        flex: "0 0 auto",
      }}
    />
  );
}
