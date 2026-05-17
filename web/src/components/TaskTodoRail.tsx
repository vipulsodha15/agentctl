import { useEffect, useMemo, useState } from "react";
import type { ConversationMessage } from "../types";
import {
  deriveTodoGroups,
  progressOf,
  type NormalizedTodo,
  type TodoGroup,
} from "../lib/todos";

interface Props {
  messages: readonly ConversationMessage[];
}

const RAIL_COLLAPSED_KEY = "agentctl.taskTodoRail.collapsed";
const NARROW_VIEWPORT_PX = 1280;

function readInitialCollapsed(): boolean {
  if (typeof window === "undefined") return false;
  try {
    const stored = localStorage.getItem(RAIL_COLLAPSED_KEY);
    if (stored === "1") return true;
    if (stored === "0") return false;
  } catch {
    // storage may be disabled
  }
  return window.innerWidth < NARROW_VIEWPORT_PX;
}

export function TaskTodoRail({ messages }: Props) {
  const groups = useMemo(() => deriveTodoGroups(messages), [messages]);
  const [collapsed, setCollapsed] = useState<boolean>(readInitialCollapsed);
  // Per-group expansion overrides. Default: only the latest group is open.
  const [overrides, setOverrides] = useState<Record<string, boolean>>({});

  useEffect(() => {
    try {
      localStorage.setItem(RAIL_COLLAPSED_KEY, collapsed ? "1" : "0");
    } catch {
      // ignore
    }
  }, [collapsed]);

  if (groups.length === 0) return null;

  const latest = groups[groups.length - 1];
  const latestProgress = progressOf(latest);

  const isExpanded = (idx: number, id: string): boolean => {
    if (id in overrides) return overrides[id];
    return idx === groups.length - 1;
  };

  const toggleGroup = (id: string, current: boolean) => {
    setOverrides((prev) => ({ ...prev, [id]: !current }));
  };

  if (collapsed) {
    return (
      <button
        type="button"
        className="task-todo-rail collapsed"
        onClick={() => setCollapsed(false)}
        aria-label="Show plan"
        aria-expanded={false}
        title={`Plan ${latestProgress.done}/${latestProgress.total}`}
      >
        <span className="task-todo-rail-chip-icon" aria-hidden>
          ☑
        </span>
        <span className="task-todo-rail-chip-progress">
          {latestProgress.done}/{latestProgress.total}
        </span>
      </button>
    );
  }

  return (
    <aside className="task-todo-rail" aria-label="Agent plan">
      <header className="task-todo-rail-head">
        <span className="task-todo-rail-title">Plan</span>
        <span className="task-todo-rail-count" aria-hidden>
          {groups.length > 1 ? `${groups.length} revisions` : ""}
        </span>
        <button
          type="button"
          className="task-todo-rail-collapse"
          onClick={() => setCollapsed(true)}
          aria-label="Hide plan"
          title="Hide"
        >
          ›
        </button>
      </header>
      <div className="task-todo-rail-body">
        {groups.map((g, i) => (
          <PlanGroup
            key={g.id}
            group={g}
            index={i}
            isLast={i === groups.length - 1}
            expanded={isExpanded(i, g.id)}
            onToggle={(cur) => toggleGroup(g.id, cur)}
          />
        ))}
      </div>
    </aside>
  );
}

function PlanGroup({
  group,
  index,
  isLast,
  expanded,
  onToggle,
}: {
  group: TodoGroup;
  index: number;
  isLast: boolean;
  expanded: boolean;
  onToggle: (currentlyExpanded: boolean) => void;
}) {
  const { done, total, inProgress } = progressOf(group);
  const pct = total === 0 ? 0 : Math.round((done / total) * 100);

  return (
    <section className={`plan-group${isLast ? " is-latest" : ""}`}>
      <button
        type="button"
        className="plan-group-head"
        aria-expanded={expanded}
        onClick={() => onToggle(expanded)}
      >
        <span className={`plan-group-caret${expanded ? " open" : ""}`} aria-hidden>
          ▸
        </span>
        <span className="plan-group-label">
          Plan {index + 1}
        </span>
        <span className="plan-group-progress" aria-label={`${done} of ${total} done`}>
          {done}/{total}
        </span>
      </button>
      {!expanded && inProgress && (
        <div className="plan-group-active" title={inProgress.text}>
          {inProgress.activeForm ?? inProgress.text}
        </div>
      )}
      <div
        className="plan-group-progress-bar"
        role="progressbar"
        aria-valuenow={pct}
        aria-valuemin={0}
        aria-valuemax={100}
      >
        <span style={{ width: `${pct}%` }} />
      </div>
      {expanded && (
        <ol className="plan-group-list">
          {group.todos.map((t, i) => (
            <TodoRow key={`${i}-${t.text}`} todo={t} />
          ))}
        </ol>
      )}
    </section>
  );
}

function TodoRow({ todo }: { todo: NormalizedTodo }) {
  const icon =
    todo.status === "completed" ? "✓" : todo.status === "in_progress" ? "●" : "○";
  return (
    <li className={`todo-row todo-${todo.status}`}>
      <span className="todo-icon" aria-hidden>
        {icon}
      </span>
      <span className="todo-text">
        {todo.status === "in_progress" && todo.activeForm
          ? todo.activeForm
          : todo.text}
      </span>
    </li>
  );
}
