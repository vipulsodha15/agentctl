// Unified-diff renderer for Edit/Write/MultiEdit tool calls.
//
// We don't render real `git diff` output (the tools' input doesn't carry it),
// but we synthesize a small line-based diff from old_string/new_string (Edit),
// each edit in MultiEdit, or empty→file_text (Write). The user's mental model
// matches: "show me what changed."

import { diffLines, type Change } from "diff";

export interface DiffHunk {
  changes: Change[];
  added: number;
  removed: number;
}

export function diffFromEditInput(input: unknown): DiffHunk[] {
  if (!input || typeof input !== "object") return [];
  const o = input as Record<string, unknown>;

  // Write: { file_path, content } — we treat the whole thing as added.
  if (typeof o.content === "string" && o.old_string === undefined) {
    const changes = diffLines("", o.content);
    return [summarize(changes)];
  }

  // Edit: { old_string, new_string }
  if (typeof o.old_string === "string" && typeof o.new_string === "string") {
    const changes = diffLines(o.old_string, o.new_string);
    return [summarize(changes)];
  }

  // MultiEdit: { edits: [{old_string, new_string}, ...] }
  const edits = o.edits;
  if (Array.isArray(edits)) {
    const out: DiffHunk[] = [];
    for (const e of edits) {
      if (!e || typeof e !== "object") continue;
      const er = e as Record<string, unknown>;
      if (typeof er.old_string === "string" && typeof er.new_string === "string") {
        const changes = diffLines(er.old_string, er.new_string);
        out.push(summarize(changes));
      }
    }
    return out;
  }

  return [];
}

function summarize(changes: Change[]): DiffHunk {
  let added = 0;
  let removed = 0;
  for (const c of changes) {
    const lines = c.value.split("\n");
    // diff.diffLines preserves a trailing newline as an extra empty element;
    // drop it so counts match what the user sees.
    const n = lines[lines.length - 1] === "" ? lines.length - 1 : lines.length;
    if (c.added) added += n;
    else if (c.removed) removed += n;
  }
  return { changes, added, removed };
}
