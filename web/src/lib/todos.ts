// Plan-tracking selector for the TaskDetail right rail.
//
// Both providers emit TodoWrite-shaped tool calls:
//   Claude: { todos: [{content, status, activeForm}] }
//   Codex:  { todos: [{text, completed}] }
// (codex_driver.py:783 already normalizes its native todo_list to the
// TodoWrite shape on the wire, so the UI only sees one tool name.)
//
// We normalize both to NormalizedTodo, then collapse consecutive
// TodoWrite calls into "plan groups" by Jaccard similarity on the item
// text set — same plan progressing keeps updating one group; a
// substantially different plan appends a new group.

import type { ConversationMessage } from "../types";

export type TodoStatus = "pending" | "in_progress" | "completed";

export interface NormalizedTodo {
  text: string;
  status: TodoStatus;
  activeForm?: string;
}

export interface TodoGroup {
  id: string;
  todos: NormalizedTodo[];
  first_seen_at: number;
  last_updated_at: number;
}

export function normalizeTodos(raw: unknown): NormalizedTodo[] {
  if (!raw || typeof raw !== "object") return [];
  const todos = (raw as { todos?: unknown }).todos;
  if (!Array.isArray(todos)) return [];
  const out: NormalizedTodo[] = [];
  for (const t of todos) {
    if (!t || typeof t !== "object") continue;
    const o = t as Record<string, unknown>;
    const text =
      (typeof o.content === "string" && o.content) ||
      (typeof o.text === "string" && o.text) ||
      "";
    if (!text) continue;
    let status: TodoStatus;
    if (typeof o.status === "string") {
      status =
        o.status === "completed" || o.status === "in_progress"
          ? o.status
          : "pending";
    } else if (typeof o.completed === "boolean") {
      status = o.completed ? "completed" : "pending";
    } else {
      status = "pending";
    }
    const activeForm =
      typeof o.activeForm === "string" ? o.activeForm : undefined;
    out.push({ text, status, activeForm });
  }
  return out;
}

const SAME_PLAN_THRESHOLD = 0.5;

function jaccard(a: ReadonlySet<string>, b: ReadonlySet<string>): number {
  if (a.size === 0 && b.size === 0) return 1;
  let inter = 0;
  for (const x of a) if (b.has(x)) inter++;
  const union = a.size + b.size - inter;
  return union === 0 ? 0 : inter / union;
}

export function deriveTodoGroups(
  messages: readonly ConversationMessage[],
): TodoGroup[] {
  const groups: TodoGroup[] = [];
  let lastKey: Set<string> | null = null;
  for (const m of messages) {
    if (m.kind !== "tool" || m.tool !== "TodoWrite") continue;
    const todos = normalizeTodos(m.input);
    if (todos.length === 0) continue;
    const ts = m.ended_at ?? m.started_at ?? 0;
    const key = new Set(todos.map((t) => t.text));
    const last = groups[groups.length - 1];
    if (last && lastKey && jaccard(lastKey, key) >= SAME_PLAN_THRESHOLD) {
      last.todos = todos;
      last.last_updated_at = ts || last.last_updated_at;
    } else {
      groups.push({
        id: m.id,
        todos,
        first_seen_at: ts,
        last_updated_at: ts,
      });
    }
    lastKey = key;
  }
  return groups;
}

export interface TodoProgress {
  done: number;
  total: number;
  inProgress?: NormalizedTodo;
}

export function progressOf(group: TodoGroup): TodoProgress {
  const done = group.todos.filter((t) => t.status === "completed").length;
  const inProgress = group.todos.find((t) => t.status === "in_progress");
  return { done, total: group.todos.length, inProgress };
}
