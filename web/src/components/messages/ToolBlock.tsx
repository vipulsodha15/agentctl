import { useMemo, useState } from "react";
import type { ConversationMessage, McpStatus } from "../../types";
import {
  formatToolHeader,
  isBashTool,
  isFileEditTool,
} from "../../lib/toolFormat";
import { DiffPreview } from "./DiffPreview";
import { TerminalOutput } from "./TerminalOutput";
import { CopyButton } from "./CopyButton";

interface Props {
  message: ConversationMessage;
  mcps: McpStatus[];
}

function tryParseJson(s: string): unknown {
  if (!s) return s;
  if (typeof s !== "string") return s;
  const t = s.trim();
  if (t === "") return s;
  if (t[0] !== "{" && t[0] !== "[" && t[0] !== '"') return s;
  try {
    return JSON.parse(s);
  } catch {
    return s;
  }
}

function stringify(v: unknown): string {
  if (typeof v === "string") return v;
  try {
    return JSON.stringify(v, null, 2);
  } catch {
    return String(v);
  }
}

function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
  const m = Math.floor(ms / 60_000);
  const s = Math.floor((ms % 60_000) / 1000);
  return `${m}m ${s}s`;
}

export function ToolBlock({ message, mcps }: Props) {
  const header = useMemo(
    () => formatToolHeader(message.tool, message.input),
    [message.tool, message.input],
  );
  const status = message.status ?? (message.is_error ? "error" : "done");

  const [open, setOpen] = useState(false);

  const mcpHealth = header.isMcp && header.mcpServer
    ? mcps.find((m) => m.name === header.mcpServer)?.status
    : undefined;

  const duration =
    message.started_at && message.ended_at
      ? formatDuration(message.ended_at - message.started_at)
      : undefined;

  const showDiff = isFileEditTool(message.tool);
  const showTerminal = isBashTool(message.tool);

  const inputObj = useMemo(() => message.input, [message.input]);
  const outputParsed = useMemo(
    () => (message.output !== undefined ? tryParseJson(message.output) : undefined),
    [message.output],
  );

  return (
    <div
      className={`msg tool tool-${status} ${header.isSkill ? "tool-skill" : ""} ${header.isMcp ? "tool-mcp" : ""}`}
      id={`msg-${message.id}`}
    >
      <div className="avatar" aria-hidden>
        {header.isSkill ? "🧠" : header.isMcp ? "⚙" : iconFor(message.tool)}
      </div>
      <div className="body">
        <button
          type="button"
          className="tool-header"
          onClick={() => setOpen((v) => !v)}
          aria-expanded={open}
        >
          <span className={`tool-chevron ${open ? "open" : ""}`} aria-hidden>
            ▶
          </span>
          <span className="tool-verb">{header.verb}</span>
          {header.target && (
            <code className="tool-target" title={header.target}>
              {header.target}
            </code>
          )}
          {header.detail && (
            <span className="tool-detail">{header.detail}</span>
          )}
          {header.isMcp && (
            <span
              className={`tool-badge mcp ${mcpHealth ? `mcp-${mcpHealth}` : ""}`}
              title={mcpHealth ? `MCP ${mcpHealth}` : "MCP tool"}
            >
              MCP
            </span>
          )}
          {header.isSkill && (
            <span className="tool-badge skill">Skill</span>
          )}
          <StatusChip status={status} duration={duration} />
        </button>

        {open && (
          <div className="tool-card">
            {/* For Edit/Write/MultiEdit, show a diff preview of the input. */}
            {showDiff && <DiffPreview input={inputObj} />}

            {/* Bash gets terminal styling for the output. */}
            {showTerminal && status !== "pending" && (
              <TerminalOutput
                command={
                  inputObj && typeof inputObj === "object"
                    ? ((inputObj as Record<string, unknown>).command as string | undefined)
                    : undefined
                }
                description={
                  inputObj && typeof inputObj === "object"
                    ? ((inputObj as Record<string, unknown>).description as string | undefined)
                    : undefined
                }
                output={message.output ?? ""}
                isError={message.is_error}
              />
            )}

            {/* Generic input/output panels (skipped for diff/terminal). */}
            {!showDiff && (
              <Section title="Input">
                <pre>{stringify(inputObj)}</pre>
                <div className="section-actions">
                  <CopyButton text={stringify(inputObj)} />
                </div>
              </Section>
            )}
            {!showTerminal && message.output !== undefined && (
              <Section title={message.is_error ? "Error" : "Output"}>
                <pre className={message.is_error ? "tool-output-error" : ""}>
                  {typeof outputParsed === "string"
                    ? outputParsed
                    : stringify(outputParsed)}
                </pre>
                <div className="section-actions">
                  <CopyButton text={message.output} />
                </div>
              </Section>
            )}
            {status === "pending" && message.output === undefined && (
              <div className="tool-pending">
                <span className="streaming-dot" aria-hidden />
                running…
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

function StatusChip({
  status,
  duration,
}: {
  status: "pending" | "done" | "error";
  duration?: string;
}) {
  if (status === "pending") {
    return (
      <span className="status-chip pending" title="Running">
        <span className="streaming-dot" aria-hidden /> running
      </span>
    );
  }
  if (status === "error") {
    return (
      <span className="status-chip error" title="Failed">
        ✗ failed{duration ? ` · ${duration}` : ""}
      </span>
    );
  }
  return (
    <span className="status-chip done" title="Completed">
      ✓ done{duration ? ` · ${duration}` : ""}
    </span>
  );
}

function Section({
  title,
  children,
}: {
  title: string;
  children: React.ReactNode;
}) {
  return (
    <div className="tool-section">
      <div className="tool-section-title">{title}</div>
      {children}
    </div>
  );
}

function iconFor(tool: string | undefined): string {
  switch (tool) {
    case "Read":
      return "📄";
    case "Edit":
    case "MultiEdit":
    case "Write":
    case "NotebookEdit":
      return "✎";
    case "Bash":
      return "⌘";
    case "Grep":
    case "Glob":
      return "⌕";
    case "WebFetch":
    case "WebSearch":
      return "🌐";
    case "Agent":
    case "Task":
      return "⚡";
    case "TodoWrite":
      return "☑";
    default:
      return "T";
  }
}
