import type { McpStatus } from "../types";

interface Props {
  mcps: McpStatus[];
}

export function McpPanel({ mcps }: Props) {
  return (
    <div className="panel">
      <h3>MCPs</h3>
      {mcps.length === 0 ? (
        <div className="empty">None</div>
      ) : (
        mcps.map((m) => (
          <div className="mcp-row" key={m.name}>
            <span>{m.name}</span>
            <StatusBadge status={m.status} />
          </div>
        ))
      )}
    </div>
  );
}

function StatusBadge({ status }: { status: McpStatus["status"] }) {
  const cls =
    status === "ok"
      ? "running"
      : status === "unreachable"
        ? "error"
        : "stopped";
  return <span className={`status-badge ${cls}`}>{status}</span>;
}
