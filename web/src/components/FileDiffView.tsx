import { useMemo, useState } from "react";
import type { FileDiff, ParsedHunk, SplitRow } from "../lib/diff";
import { splitHunk } from "../lib/diff";
import { CopyButton } from "./messages/CopyButton";

export type DiffViewMode = "unified" | "split";

interface Props {
  file: FileDiff;
  mode: DiffViewMode;
}

const LARGE_LINE_THRESHOLD = 2000;

export function FileDiffView({ file, mode }: Props) {
  const [collapsed, setCollapsed] = useState(false);
  const [forceRender, setForceRender] = useState(false);

  const totalLines = useMemo(
    () => file.hunks.reduce((n, h) => n + h.lines.length, 0),
    [file.hunks],
  );

  const heavy = totalLines > LARGE_LINE_THRESHOLD && !forceRender;

  return (
    <section className="diff-file" aria-label={file.path}>
      <header className="diff-file-header">
        <button
          type="button"
          className={`diff-file-toggle ${collapsed ? "" : "open"}`}
          onClick={() => setCollapsed((v) => !v)}
          aria-expanded={!collapsed}
          aria-label={collapsed ? "Expand file" : "Collapse file"}
        >
          ▶
        </button>
        <span className="diff-file-path" title={file.path}>
          {file.oldPath ? `${file.oldPath} → ${file.path}` : file.path}
        </span>
        <span className={`diff-file-status diff-status-${file.status}`}>
          {labelFor(file.status)}
        </span>
        {file.isBinary && <span className="diff-file-binary">binary</span>}
        <span className="diff-file-counts">
          {file.added > 0 && <span className="diff-add">+{file.added}</span>}
          {file.removed > 0 && (
            <span className="diff-rem">−{file.removed}</span>
          )}
        </span>
        {file.rawPatch && (
          <div className="diff-file-actions">
            <CopyButton text={file.rawPatch} />
          </div>
        )}
      </header>

      {!collapsed && (
        <div className="diff-file-body">
          {file.isBinary ? (
            <div className="diff-empty">
              Binary file{" "}
              {file.status === "added"
                ? "added"
                : file.status === "deleted"
                  ? "removed"
                  : "changed"}
              .
            </div>
          ) : file.hunks.length === 0 ? (
            <div className="diff-empty">
              {file.status === "renamed"
                ? "Renamed without content change."
                : "No textual changes."}
            </div>
          ) : heavy ? (
            <div className="diff-empty">
              Large diff ({totalLines.toLocaleString()} lines).{" "}
              <button
                type="button"
                className="link-button"
                onClick={() => setForceRender(true)}
              >
                Render anyway
              </button>
            </div>
          ) : mode === "split" ? (
            <SplitHunks hunks={file.hunks} />
          ) : (
            <UnifiedHunks hunks={file.hunks} />
          )}
        </div>
      )}
    </section>
  );
}

function labelFor(s: FileDiff["status"]): string {
  switch (s) {
    case "added":
      return "Added";
    case "deleted":
      return "Deleted";
    case "renamed":
      return "Renamed";
    default:
      return "Modified";
  }
}

function UnifiedHunks({ hunks }: { hunks: ParsedHunk[] }) {
  return (
    <div className="diff-unified" role="table">
      {hunks.map((h, i) => (
        <div key={i} className="diff-hunk-block">
          <div className="diff-hunk-header" role="row">
            <span className="diff-hunk-range">
              @@ -{h.oldStart},{h.oldLines} +{h.newStart},{h.newLines} @@
            </span>
            {h.header && (
              <span className="diff-hunk-context" title={h.header.trim()}>
                {h.header.trim()}
              </span>
            )}
          </div>
          {h.lines.map((l, j) => {
            const cls =
              l.kind === "add"
                ? "add"
                : l.kind === "del"
                  ? "rem"
                  : "ctx";
            const oldNo = l.kind === "add" ? "" : String(l.oldLineNo);
            const newNo = l.kind === "del" ? "" : String(l.newLineNo);
            const sign = l.kind === "add" ? "+" : l.kind === "del" ? "−" : " ";
            return (
              <div key={j} className={`diff-row ${cls}`} role="row">
                <span className="diff-gutter diff-gutter-old">{oldNo}</span>
                <span className="diff-gutter diff-gutter-new">{newNo}</span>
                <span className="diff-sign" aria-hidden>
                  {sign}
                </span>
                <span className="diff-text">{l.text || " "}</span>
              </div>
            );
          })}
        </div>
      ))}
    </div>
  );
}

function SplitHunks({ hunks }: { hunks: ParsedHunk[] }) {
  return (
    <div className="diff-split">
      {hunks.map((h, i) => {
        const rows = splitHunk(h);
        return (
          <div key={i} className="diff-hunk-block">
            <div className="diff-hunk-header" role="row">
              <span className="diff-hunk-range">
                @@ -{h.oldStart},{h.oldLines} +{h.newStart},{h.newLines} @@
              </span>
              {h.header && (
                <span className="diff-hunk-context" title={h.header.trim()}>
                  {h.header.trim()}
                </span>
              )}
            </div>
            <div className="diff-split-grid">
              {rows.map((r, j) => (
                <SplitRowView key={j} row={r} />
              ))}
            </div>
          </div>
        );
      })}
    </div>
  );
}

function SplitRowView({ row }: { row: SplitRow }) {
  const leftCls =
    row.left?.kind === "del"
      ? "rem"
      : row.left?.kind === "empty"
        ? "empty"
        : "ctx";
  const rightCls =
    row.right?.kind === "add"
      ? "add"
      : row.right?.kind === "empty"
        ? "empty"
        : "ctx";
  return (
    <>
      <div className={`diff-split-cell ${leftCls}`}>
        <span className="diff-gutter diff-gutter-old">
          {row.left?.lineNo ?? ""}
        </span>
        <span className="diff-sign" aria-hidden>
          {row.left?.kind === "del" ? "−" : " "}
        </span>
        <span className="diff-text">{row.left?.text || " "}</span>
      </div>
      <div className={`diff-split-cell ${rightCls}`}>
        <span className="diff-gutter diff-gutter-new">
          {row.right?.lineNo ?? ""}
        </span>
        <span className="diff-sign" aria-hidden>
          {row.right?.kind === "add" ? "+" : " "}
        </span>
        <span className="diff-text">{row.right?.text || " "}</span>
      </div>
    </>
  );
}
