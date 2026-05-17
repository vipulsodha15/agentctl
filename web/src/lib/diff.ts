// Diff utilities for the SPA.
//
// Two separate concerns live here:
//
//   1. `diffFromEditInput` — synthesizes a line-based diff from the input of
//      an Edit/Write/MultiEdit tool call. Used inline in the conversation
//      transcript (`messages/DiffPreview.tsx`) to show what an individual
//      tool call changed. The tool's input doesn't carry a real `git diff`,
//      so we approximate one from `old_string`/`new_string`.
//
//   2. `parseUnifiedDiff` — parses real `git diff` output (the shim's
//      `git diff --unified=3 <base>` plus appended untracked-as-new patches)
//      into a structured `FileDiff[]` so the Changes panel can render a
//      file tree and per-file views. Handles renames, new/deleted files,
//      binary markers, and the missing-trailing-newline marker.

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

// ──────────────────────────────────────────────────────────────────────
//  Unified-diff parser (for the Changes panel)
// ──────────────────────────────────────────────────────────────────────

export type FileStatus = "added" | "deleted" | "modified" | "renamed";

export interface FileDiff {
  /** Path the file has after the change. For renames, the new path. */
  path: string;
  /** Original path; only set for renames. */
  oldPath?: string;
  status: FileStatus;
  /** True for `Binary files … differ` blocks; `hunks` will be empty. */
  isBinary: boolean;
  added: number;
  removed: number;
  hunks: ParsedHunk[];
  /** The raw patch text for this file, sliced from the input — useful for
   * "copy patch" and "download just this file". */
  rawPatch: string;
}

export interface ParsedHunk {
  oldStart: number;
  oldLines: number;
  newStart: number;
  newLines: number;
  /** The trailing context shown after `@@`, e.g. function signature. */
  header: string;
  lines: ParsedLine[];
}

export type ParsedLine =
  | { kind: "add"; text: string; newLineNo: number }
  | { kind: "del"; text: string; oldLineNo: number }
  | { kind: "ctx"; text: string; oldLineNo: number; newLineNo: number };

/**
 * Parse the concatenated output of one or more `git diff` invocations into
 * structured per-file diffs. Tolerates the small variations the shim
 * produces (notably `--no-index /dev/null path` for untracked files).
 *
 * Returns an empty array on empty/whitespace input. Malformed sections are
 * skipped rather than throwing — the goal is to render whatever is well-
 * formed and ignore the rest, since one bad section shouldn't blank the
 * whole panel.
 */
export function parseUnifiedDiff(patch: string): FileDiff[] {
  if (!patch) return [];
  const lines = patch.split("\n");
  const files: FileDiff[] = [];

  let current: FileDiff | null = null;
  let currentHunk: ParsedHunk | null = null;
  let oldLineNo = 0;
  let newLineNo = 0;
  // Index of the line where `current` started, so we can slice rawPatch when
  // we close it out (either at the next file or at end-of-input).
  let currentStart = 0;

  const closeCurrent = (endLine: number) => {
    if (current) {
      current.rawPatch = lines.slice(currentStart, endLine).join("\n");
    }
  };

  const startFile = (initialPath: string, startLine: number): FileDiff => {
    closeCurrent(startLine);
    const f: FileDiff = {
      path: initialPath,
      status: "modified",
      isBinary: false,
      added: 0,
      removed: 0,
      hunks: [],
      rawPatch: "",
    };
    files.push(f);
    currentHunk = null;
    currentStart = startLine;
    return f;
  };

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];

    if (line.startsWith("diff --git ")) {
      // `diff --git a/<old> b/<new>` — paths may contain spaces. We use the
      // last `b/...` segment as a best-effort initial path; the canonical
      // path comes from the `+++ ` header below.
      const rest = line.slice("diff --git ".length);
      const initial = guessPathFromDiffLine(rest);
      current = startFile(initial, i);
      continue;
    }
    if (!current) continue;

    if (line.startsWith("new file mode")) {
      current.status = "added";
      continue;
    }
    if (line.startsWith("deleted file mode")) {
      current.status = "deleted";
      continue;
    }
    if (line.startsWith("rename from ")) {
      current.status = "renamed";
      current.oldPath = line.slice("rename from ".length);
      continue;
    }
    if (line.startsWith("rename to ")) {
      current.path = line.slice("rename to ".length);
      continue;
    }
    if (line.startsWith("Binary files ")) {
      current.isBinary = true;
      // Format: `Binary files <left> and <right> differ`. The null side may
      // be `/dev/null` (bare) or `a/dev/null` / `b/dev/null` when
      // `--no-index` was used. Defer to status already inferred from
      // `new file mode` / `deleted file mode` lines when present.
      if (current.status === "modified") {
        const m = line.match(/^Binary files (.+) and (.+) differ$/);
        if (m) {
          const left = m[1];
          const right = m[2];
          if (left === "/dev/null" || left === "a/dev/null") {
            current.status = "added";
          } else if (right === "/dev/null" || right === "b/dev/null") {
            current.status = "deleted";
          }
        }
      }
      continue;
    }

    if (line.startsWith("--- ")) {
      const raw = line.slice(4).trim();
      if (isDevNullPath(raw)) {
        current.status = "added";
      }
      continue;
    }
    if (line.startsWith("+++ ")) {
      const raw = line.slice(4).trim();
      if (isDevNullPath(raw)) {
        current.status = "deleted";
      } else {
        current.path = stripGitPrefix(raw);
      }
      continue;
    }

    // Hunk header: @@ -oldStart[,oldLines] +newStart[,newLines] @@ context
    if (line.startsWith("@@")) {
      const m = line.match(
        /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(.*)$/,
      );
      if (!m) {
        currentHunk = null;
        continue;
      }
      const oldStart = Number(m[1]);
      const oldLines = m[2] !== undefined ? Number(m[2]) : 1;
      const newStart = Number(m[3]);
      const newLines = m[4] !== undefined ? Number(m[4]) : 1;
      currentHunk = {
        oldStart,
        oldLines,
        newStart,
        newLines,
        header: m[5] || "",
        lines: [],
      };
      current.hunks.push(currentHunk);
      oldLineNo = oldStart;
      newLineNo = newStart;
      continue;
    }

    if (!currentHunk) continue;

    // "\ No newline at end of file" annotates the preceding line; ignore it
    // for display since it doesn't move the line counter.
    if (line.startsWith("\\")) continue;

    const tag = line.charAt(0);
    const text = line.slice(1);
    if (tag === "+") {
      currentHunk.lines.push({ kind: "add", text, newLineNo });
      current.added++;
      newLineNo++;
    } else if (tag === "-") {
      currentHunk.lines.push({ kind: "del", text, oldLineNo });
      current.removed++;
      oldLineNo++;
    } else if (tag === " ") {
      currentHunk.lines.push({ kind: "ctx", text, oldLineNo, newLineNo });
      oldLineNo++;
      newLineNo++;
    }
    // Any other leading character (or empty trailing split line) is a
    // structural marker we already handled, or noise — skip.
  }
  closeCurrent(lines.length);
  return files;
}

function stripGitPrefix(p: string): string {
  if (p.startsWith("a/") || p.startsWith("b/")) return p.slice(2);
  // Some tools emit quoted paths with escapes; strip surrounding quotes so
  // the path is at least readable. We don't unescape \t/\n — those paths
  // are exotic enough that displaying the raw form is acceptable.
  if (p.length >= 2 && p.startsWith('"') && p.endsWith('"')) {
    return p.slice(1, -1);
  }
  return p;
}

function isDevNullPath(p: string): boolean {
  return (
    p === "/dev/null" || p === "a/dev/null" || p === "b/dev/null"
  );
}

function guessPathFromDiffLine(rest: string): string {
  // `a/foo.txt b/foo.txt` is the common case; for paths with spaces git
  // emits `"a/has space" "b/has space"`. We try the simple split first and
  // fall back to a quoted split.
  if (rest.startsWith('"')) {
    const m = rest.match(/^"([^"]+)"\s+"([^"]+)"$/);
    if (m) return stripGitPrefix(m[2]);
  }
  const parts = rest.split(" ");
  if (parts.length >= 2) {
    return stripGitPrefix(parts[parts.length - 1]);
  }
  return rest;
}

// ──────────────────────────────────────────────────────────────────────
//  Side-by-side projection
// ──────────────────────────────────────────────────────────────────────

export interface SplitRow {
  left?: { text: string; lineNo?: number; kind: "del" | "ctx" | "empty" };
  right?: { text: string; lineNo?: number; kind: "add" | "ctx" | "empty" };
}

/**
 * Project a unified hunk into paired left/right rows for side-by-side
 * rendering. Consecutive del/add runs are zipped, with the shorter side
 * padded with blank rows so the alignment matches what `git diff
 * --color-words` style tools produce.
 */
export function splitHunk(hunk: ParsedHunk): SplitRow[] {
  const rows: SplitRow[] = [];
  const dels: { text: string; lineNo: number }[] = [];
  const adds: { text: string; lineNo: number }[] = [];

  const flush = () => {
    const n = Math.max(dels.length, adds.length);
    for (let i = 0; i < n; i++) {
      const d = dels[i];
      const a = adds[i];
      rows.push({
        left: d
          ? { text: d.text, lineNo: d.lineNo, kind: "del" }
          : { text: "", kind: "empty" },
        right: a
          ? { text: a.text, lineNo: a.lineNo, kind: "add" }
          : { text: "", kind: "empty" },
      });
    }
    dels.length = 0;
    adds.length = 0;
  };

  for (const l of hunk.lines) {
    if (l.kind === "del") {
      dels.push({ text: l.text, lineNo: l.oldLineNo });
    } else if (l.kind === "add") {
      adds.push({ text: l.text, lineNo: l.newLineNo });
    } else {
      flush();
      rows.push({
        left: { text: l.text, lineNo: l.oldLineNo, kind: "ctx" },
        right: { text: l.text, lineNo: l.newLineNo, kind: "ctx" },
      });
    }
  }
  flush();
  return rows;
}
