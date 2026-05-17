import { useMemo, useState } from "react";
import type { FileDiff, FileStatus } from "../lib/diff";

interface Props {
  files: FileDiff[];
  selectedPath: string | null;
  onSelect: (path: string) => void;
}

type Node =
  | { kind: "dir"; name: string; fullPath: string; children: Node[] }
  | { kind: "file"; name: string; fullPath: string; file: FileDiff };

/**
 * Collapse single-child directory chains so `src/components/messages/Foo.tsx`
 * collapses to one row `src/components/messages` → `Foo.tsx` when nothing
 * else lives at the intermediate levels. Matches GitHub's PR file tree.
 */
function simplify(node: Node): Node {
  if (node.kind === "file") return node;
  node.children = node.children.map(simplify);
  if (
    node.children.length === 1 &&
    node.children[0].kind === "dir" &&
    node.name !== ""
  ) {
    const only = node.children[0];
    return {
      kind: "dir",
      name: `${node.name}/${only.name}`,
      fullPath: only.fullPath,
      children: only.children,
    };
  }
  return node;
}

function buildTree(files: FileDiff[]): Node[] {
  const root: Node = { kind: "dir", name: "", fullPath: "", children: [] };
  for (const f of files) {
    const parts = f.path.split("/").filter(Boolean);
    let cur = root;
    for (let i = 0; i < parts.length; i++) {
      const isLast = i === parts.length - 1;
      const name = parts[i];
      if (isLast) {
        cur.children.push({ kind: "file", name, fullPath: f.path, file: f });
      } else {
        const segPath = parts.slice(0, i + 1).join("/");
        let dir = cur.children.find(
          (c): c is Node & { kind: "dir" } =>
            c.kind === "dir" && c.fullPath === segPath,
        );
        if (!dir) {
          dir = {
            kind: "dir",
            name,
            fullPath: segPath,
            children: [],
          };
          cur.children.push(dir);
        }
        cur = dir;
      }
    }
  }
  const sort = (n: Node): void => {
    if (n.kind !== "dir") return;
    n.children.sort((a, b) => {
      if (a.kind !== b.kind) return a.kind === "dir" ? -1 : 1;
      return a.name.localeCompare(b.name);
    });
    for (const c of n.children) sort(c);
  };
  sort(root);
  return root.children.map(simplify);
}

export function FileTree({ files, selectedPath, onSelect }: Props) {
  const tree = useMemo(() => buildTree(files), [files]);
  // Default: every directory expanded. We track *collapsed* dirs so the
  // initial render with no state shows everything.
  const [collapsed, setCollapsed] = useState<Set<string>>(() => new Set());

  if (files.length === 0) {
    return <div className="diff-tree-empty">no changes</div>;
  }

  const toggle = (path: string) => {
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (next.has(path)) next.delete(path);
      else next.add(path);
      return next;
    });
  };

  return (
    <ul className="diff-tree" role="tree">
      {tree.map((n) => (
        <TreeRow
          key={`${n.kind}:${n.fullPath || n.name}`}
          node={n}
          depth={0}
          collapsed={collapsed}
          onToggle={toggle}
          selectedPath={selectedPath}
          onSelect={onSelect}
        />
      ))}
    </ul>
  );
}

function TreeRow({
  node,
  depth,
  collapsed,
  onToggle,
  selectedPath,
  onSelect,
}: {
  node: Node;
  depth: number;
  collapsed: Set<string>;
  onToggle: (path: string) => void;
  selectedPath: string | null;
  onSelect: (path: string) => void;
}) {
  // Indent visually; the leading offset matches the chevron width so the
  // labels align between dirs and files.
  const indentPx = depth * 14;

  if (node.kind === "file") {
    const sel = selectedPath === node.fullPath;
    const f = node.file;
    return (
      <li role="treeitem" aria-selected={sel}>
        <button
          type="button"
          className={`diff-tree-file ${sel ? "selected" : ""}`}
          style={{ paddingLeft: 12 + indentPx }}
          onClick={() => onSelect(node.fullPath)}
          title={node.fullPath}
        >
          <StatusBadge status={f.status} binary={f.isBinary} />
          <span className="diff-tree-name">{node.name}</span>
          <DiffCounts added={f.added} removed={f.removed} />
        </button>
      </li>
    );
  }

  const isCollapsed = collapsed.has(node.fullPath);
  const fileCount = countFiles(node);
  return (
    <li role="treeitem" aria-expanded={!isCollapsed}>
      <button
        type="button"
        className="diff-tree-dir"
        style={{ paddingLeft: 6 + indentPx }}
        onClick={() => onToggle(node.fullPath)}
        title={node.fullPath}
      >
        <span
          className={`diff-tree-chevron ${isCollapsed ? "" : "open"}`}
          aria-hidden
        >
          ▶
        </span>
        <span className="diff-tree-dir-name">{node.name}</span>
        <span className="diff-tree-dir-count">{fileCount}</span>
      </button>
      {!isCollapsed && (
        <ul role="group">
          {node.children.map((c) => (
            <TreeRow
              key={`${c.kind}:${c.fullPath || c.name}`}
              node={c}
              depth={depth + 1}
              collapsed={collapsed}
              onToggle={onToggle}
              selectedPath={selectedPath}
              onSelect={onSelect}
            />
          ))}
        </ul>
      )}
    </li>
  );
}

function countFiles(n: Node): number {
  if (n.kind === "file") return 1;
  let total = 0;
  for (const c of n.children) total += countFiles(c);
  return total;
}

function StatusBadge({
  status,
  binary,
}: {
  status: FileStatus;
  binary: boolean;
}) {
  const letter =
    status === "added"
      ? "A"
      : status === "deleted"
        ? "D"
        : status === "renamed"
          ? "R"
          : "M";
  const label =
    status === "added"
      ? "Added"
      : status === "deleted"
        ? "Deleted"
        : status === "renamed"
          ? "Renamed"
          : "Modified";
  return (
    <span
      className={`diff-status diff-status-${status}`}
      title={binary ? `${label} (binary)` : label}
      aria-label={label}
    >
      {letter}
    </span>
  );
}

function DiffCounts({ added, removed }: { added: number; removed: number }) {
  if (added === 0 && removed === 0) return null;
  return (
    <span className="diff-tree-counts">
      {added > 0 && <span className="diff-add">+{added}</span>}
      {removed > 0 && <span className="diff-rem">−{removed}</span>}
    </span>
  );
}
