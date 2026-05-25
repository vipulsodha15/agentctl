// Smart per-tool header formatting.
//
// Most cloud-agent UIs (Cursor, Claude Code Cloud) collapse raw tool JSON in
// favor of action-verb summaries like "Read foo.py · L1-50" or "Ran npm test".
// This module centralizes that mapping so it can grow without touching the
// message component itself.

export interface ToolHeader {
  verb: string;           // "Read", "Edited", "Ran", …
  target?: string;        // primary noun (path, command, pattern)
  detail?: string;        // secondary qualifier (line range, hunk count, server)
  isMcp: boolean;
  mcpServer?: string;
  mcpTool?: string;
  isSkill: boolean;
  skillName?: string;
}

// mcp__<server>__<tool> is the Claude SDK convention for MCP-exposed tools.
const MCP_RE = /^mcp__([^_]+(?:_[^_]+)*?)__(.+)$/;

function parseInput(input: unknown): Record<string, unknown> {
  if (input && typeof input === "object" && !Array.isArray(input)) {
    return input as Record<string, unknown>;
  }
  if (typeof input === "string") {
    try {
      const v = JSON.parse(input);
      if (v && typeof v === "object" && !Array.isArray(v)) {
        return v as Record<string, unknown>;
      }
    } catch {
      // not JSON; ignore
    }
  }
  return {};
}

function basename(p: string): string {
  const i = Math.max(p.lastIndexOf("/"), p.lastIndexOf("\\"));
  return i >= 0 ? p.slice(i + 1) : p;
}

function clip(s: string, n: number): string {
  if (s.length <= n) return s;
  return s.slice(0, Math.max(0, n - 1)) + "…";
}

export function formatToolHeader(
  toolName: string | undefined,
  rawInput: unknown,
): ToolHeader {
  const tool = toolName ?? "?";
  const input = parseInput(rawInput);

  // MCP detection (e.g. mcp__github__create_pull_request).
  const m = MCP_RE.exec(tool);
  if (m) {
    const server = m[1];
    const inner = m[2];
    return {
      verb: prettyVerbForMcp(inner),
      target: inner.replace(/_/g, " "),
      detail: `via ${server}`,
      isMcp: true,
      mcpServer: server,
      mcpTool: inner,
      isSkill: false,
    };
  }

  // Skill invocation — the Claude SDK exposes a "Skill" tool that takes
  // `{skill, args}`. Surface this as a distinct banner.
  if (tool === "Skill") {
    const skill = (input.skill as string | undefined) ?? "?";
    return {
      verb: "Used skill",
      target: skill,
      isMcp: false,
      isSkill: true,
      skillName: skill,
    };
  }

  // Local Claude Code tools.
  switch (tool) {
    case "Read": {
      const fp = (input.file_path as string | undefined) ?? "";
      const offset = input.offset as number | undefined;
      const limit = input.limit as number | undefined;
      let detail: string | undefined;
      if (typeof offset === "number" || typeof limit === "number") {
        const start = offset ?? 1;
        const end = limit ? start + limit : undefined;
        detail = end ? `L${start}-${end}` : `from L${start}`;
      }
      return {
        verb: "Read",
        target: fp ? basename(fp) : undefined,
        detail,
        isMcp: false,
        isSkill: false,
      };
    }
    case "Edit":
    case "MultiEdit": {
      const fp = (input.file_path as string | undefined) ?? "";
      const edits = input.edits as unknown[] | undefined;
      const detail =
        Array.isArray(edits) && edits.length > 1
          ? `${edits.length} edits`
          : undefined;
      return {
        verb: "Edited",
        target: fp ? basename(fp) : undefined,
        detail,
        isMcp: false,
        isSkill: false,
      };
    }
    case "Write": {
      const fp = (input.file_path as string | undefined) ?? "";
      return {
        verb: "Wrote",
        target: fp ? basename(fp) : undefined,
        isMcp: false,
        isSkill: false,
      };
    }
    case "NotebookEdit": {
      const fp = (input.notebook_path as string | undefined) ?? "";
      return {
        verb: "Edited notebook",
        target: fp ? basename(fp) : undefined,
        isMcp: false,
        isSkill: false,
      };
    }
    case "Bash": {
      const cmd = (input.command as string | undefined) ?? "";
      const desc = input.description as string | undefined;
      return {
        verb: "Ran",
        target: desc && desc.length <= 60 ? desc : clip(cmd, 60),
        isMcp: false,
        isSkill: false,
      };
    }
    case "Grep": {
      const pat = (input.pattern as string | undefined) ?? "";
      const path = input.path as string | undefined;
      return {
        verb: "Searched",
        target: `"${clip(pat, 40)}"`,
        detail: path ? `in ${basename(path)}` : undefined,
        isMcp: false,
        isSkill: false,
      };
    }
    case "Glob": {
      const pat = (input.pattern as string | undefined) ?? "";
      return {
        verb: "Matched",
        target: pat,
        isMcp: false,
        isSkill: false,
      };
    }
    case "WebFetch": {
      const url = (input.url as string | undefined) ?? "";
      return {
        verb: "Fetched",
        target: clip(url, 60),
        isMcp: false,
        isSkill: false,
      };
    }
    case "WebSearch": {
      const q = (input.query as string | undefined) ?? "";
      return {
        verb: "Searched the web",
        target: `"${clip(q, 60)}"`,
        isMcp: false,
        isSkill: false,
      };
    }
    case "Agent":
    case "Task": {
      const desc =
        (input.description as string | undefined) ??
        (input.subagent_type as string | undefined);
      return {
        verb: "Delegated",
        target: desc ? clip(desc, 60) : undefined,
        isMcp: false,
        isSkill: false,
      };
    }
    case "TodoWrite":
      return { verb: "Updated todos", isMcp: false, isSkill: false };
    case "ExitPlanMode":
      return { verb: "Exited plan mode", isMcp: false, isSkill: false };
    case "AskUserQuestion": {
      const qs = input.questions;
      const count = Array.isArray(qs) ? qs.length : 0;
      return {
        verb: "Asked you",
        target:
          count > 0
            ? `${count} question${count === 1 ? "" : "s"}`
            : undefined,
        isMcp: false,
        isSkill: false,
      };
    }
    default: {
      // Fallback: best-effort guess from inputs.
      const fp =
        (input.file_path as string | undefined) ??
        (input.path as string | undefined);
      const cmd = (input.command as string | undefined);
      return {
        verb: tool,
        target: fp ? basename(fp) : cmd ? clip(cmd, 60) : undefined,
        isMcp: false,
        isSkill: false,
      };
    }
  }
}

function prettyVerbForMcp(inner: string): string {
  const t = inner.toLowerCase();
  if (t.startsWith("get_") || t.startsWith("list_") || t.startsWith("read_")) {
    return "Fetched";
  }
  if (t.startsWith("create_") || t.startsWith("add_") || t.startsWith("post_")) {
    return "Created";
  }
  if (t.startsWith("update_") || t.startsWith("edit_") || t.startsWith("set_")) {
    return "Updated";
  }
  if (t.startsWith("delete_") || t.startsWith("remove_")) {
    return "Deleted";
  }
  if (t.startsWith("search_") || t.startsWith("query_") || t.startsWith("find_")) {
    return "Searched";
  }
  return "Called";
}

export function isFileEditTool(name: string | undefined): boolean {
  return name === "Edit" || name === "MultiEdit" || name === "Write";
}

export function isBashTool(name: string | undefined): boolean {
  return name === "Bash";
}
