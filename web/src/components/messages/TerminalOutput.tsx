import { useState } from "react";
import { CopyButton } from "./CopyButton";

interface Props {
  command?: string;
  description?: string;
  output: string;
  isError?: boolean;
}

const MAX_LINES = 30;
const MAX_CHARS = 6000;

export function TerminalOutput({ command, description, output, isError }: Props) {
  const [expanded, setExpanded] = useState(false);
  const lines = output.split("\n");
  const tooLong = lines.length > MAX_LINES || output.length > MAX_CHARS;
  const shown = !tooLong || expanded ? output : truncate(output);

  return (
    <div className={`terminal ${isError ? "terminal-error" : ""}`}>
      {(command || description) && (
        <div className="terminal-header">
          <span className="terminal-prompt">$</span>
          <span className="terminal-cmd" title={command}>
            {description || command}
          </span>
          {command && <CopyButton text={command} label="Copy" className="copy-btn ghost" />}
        </div>
      )}
      <pre className="terminal-body">{shown || "(no output)"}</pre>
      {tooLong && (
        <button
          type="button"
          className="terminal-expand"
          onClick={() => setExpanded((v) => !v)}
        >
          {expanded
            ? "Collapse"
            : `Show full output (${lines.length.toLocaleString()} lines)`}
        </button>
      )}
    </div>
  );
}

function truncate(s: string): string {
  const lines = s.split("\n");
  if (lines.length > MAX_LINES) {
    return lines.slice(0, MAX_LINES).join("\n") + `\n… (${(lines.length - MAX_LINES).toLocaleString()} more lines)`;
  }
  if (s.length > MAX_CHARS) {
    return s.slice(0, MAX_CHARS) + "\n… (truncated)";
  }
  return s;
}
