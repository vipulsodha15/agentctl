# Todo Progress Tracker — UI plan

Plan for adding a persistent "current plan" panel to the session detail
page so users can watch the agent tick off TODO items in real time
instead of hunting for the latest `TodoWrite` card in the transcript.

This is a **frontend-only** feature — the data already flows through the
shared `tool.call` / `tool.result` events (both Claude and Codex emit
TodoWrite-shaped payloads after the fix in
`image/shim/runtime/codex_driver.py`). No driver, daemon, or wire-format
changes are required.

File paths and line numbers reference the current tree on
`claude/fix-codex-tool-calls-8gHup`; treat as anchors, not contracts.

---

## TL;DR

| Aspect | Choice |
|---|---|
| Layout | Collapsible sticky panel at the top of `.conversation`, above the message stream |
| Source of truth | Latest `TodoWrite` tool message per session, derived via selector (no reducer change in v1) |
| Inline rendering | Collapse `TodoWrite` cards to a one-line "📋 Updated plan (3 of 7 done)" pill; the full list lives in the panel |
| Schema | Normalize Claude's `{content, status, activeForm}` and Codex's `{text, completed}` into a single `NormalizedTodo` shape at the selector boundary |
| Status states | `pending` · `in_progress` · `completed` (Codex's bool → maps to `pending`/`completed`; no `in_progress` available for Codex today) |
| Empty state | Panel hidden when no TodoWrite has been emitted on the current session |
| Persistence | None in v1 — derived from in-memory `messages`; rehydrated automatically on snapshot/replay because TodoWrite cards are in the transcript |

---

## 1. Problem statement

Today, when an agent calls `TodoWrite` (Claude) or emits a `todo_list`
item (Codex), the UI renders it as just another collapsed tool card in
the message stream:

```
☑  Updated todos                                           ✓ done · 12ms
```

The user has to:
1. Scroll back to find the latest TodoWrite card,
2. Expand it,
3. Read JSON-pretty-printed `todos` array to see what's done.

If the agent updates the plan five times during a long turn (which
Claude does — it ticks items off as it completes them), the user sees
five separate collapsed cards instead of one live checklist. There's
no at-a-glance answer to "what is the agent working on right now" or
"how far through the plan are we."

---

## 2. Goals & non-goals

**Goals**
- Persistent visibility of the agent's current plan without scrolling.
- Live progress: items tick off as the agent works through them.
- De-noise the transcript: collapse repeated TodoWrite cards.
- Provider-agnostic: works for both Claude and Codex without per-provider UI code.
- Cheap: no reducer rework, no wire-format changes, no new event kinds.

**Non-goals (v1)**
- Multi-stage plan tracking across a task-chat (each stage has its own session today; out of scope).
- User-editable todos (read-only view; users can't reorder or check off).
- Per-turn history of plan revisions (only the latest plan is shown; older revisions remain as collapsed inline pills).
- Cross-session plan summary on the dashboard.
- Notifications when items complete.
- Animations beyond a simple fade/strike-through on status change.

---

## 3. User stories

1. **"What's the agent doing right now?"** — At any point during a long
   turn, glance at the panel and see the in-progress item highlighted.

2. **"How far through the plan are we?"** — A progress chip in the
   panel header (`3/7 · 43%`) gives a quick read.

3. **"Did the agent skip a step?"** — Scroll the panel to see the full
   ordered list with `pending` / `completed` markers.

4. **"When did the agent update the plan?"** — The inline collapsed
   pill (in the message stream) timestamps the update; expanding it
   shows the diff from the previous revision (v2 nice-to-have).

5. **"Hide the panel; I want full message height."** — Toggle the panel
   collapsed; preference persists per session (or per user via
   localStorage).

---

## 4. Data model

### 4.1 Provider input shapes (current state)

**Claude SDK `TodoWrite`** — emitted as a normal tool call via
`image/shim/runtime/claude_driver.py:393` (the SDK serializes
`ToolUseBlock` for `TodoWrite` with this input shape):

```jsonc
{
  "todos": [
    {
      "content": "Read the auth module",       // human-readable task
      "status": "completed",                   // pending | in_progress | completed
      "activeForm": "Reading the auth module"  // present-progressive label
    },
    {
      "content": "Find all callers",
      "status": "in_progress",
      "activeForm": "Finding all callers"
    },
    { "content": "Write a fix", "status": "pending", "activeForm": "Writing a fix" }
  ]
}
```

> **Verify against a captured fixture before implementation.** The
> Claude SDK has changed the TodoWrite schema before (`text` → `content`,
> adding `activeForm` in late 2024). Land the selector behind a small
> defensive normalizer (accept `content || text`, default `status` to
> `pending`) so a schema bump doesn't blank the panel.

**Codex `todo_list`** — emitted as a `TodoWrite` tool call after the
codex driver fix at `image/shim/runtime/codex_driver.py:776-780`:

```jsonc
{
  "todos": [
    { "text": "Read the auth module", "completed": true },
    { "text": "Find all callers",     "completed": false },
    { "text": "Write a fix",          "completed": false }
  ]
}
```

Codex has **no `in_progress` state** today — only a boolean. Two design
choices for v1:
- **A. Faithful**: codex todos are either `pending` or `completed`; no item is ever highlighted as "in progress." The progress chip still works.
- **B. Synthetic in-progress**: the first non-completed item is rendered as `in_progress` while the codex tool result is still pending. Lossy but matches the Claude UX.

**Recommendation: A in v1.** B is brittle (a multi-step plan where the
agent is parallelizing two items lies to the user). Add a hint in the
panel's footer that codex doesn't report in-progress state if it
becomes a usability complaint.

### 4.2 Normalized shape

A single internal type both providers map onto:

```ts
// web/src/lib/todos.ts (NEW)
export type TodoStatus = "pending" | "in_progress" | "completed";

export interface NormalizedTodo {
  text: string;          // task description (Claude content / Codex text)
  status: TodoStatus;    // Codex bool collapses to pending|completed
  activeForm?: string;   // Claude-only present-progressive label
}

export interface TodoSnapshot {
  todos: NormalizedTodo[];
  source_message_id: string;  // id of the underlying ConversationMessage
  updated_at: number;         // wall-clock ms (from started_at/ended_at)
  turn_id?: string;           // turn that produced this snapshot
}
```

### 4.3 Normalizer

```ts
// web/src/lib/todos.ts
export function normalizeTodos(raw: unknown): NormalizedTodo[] {
  if (!raw || typeof raw !== "object") return [];
  const todos = (raw as { todos?: unknown }).todos;
  if (!Array.isArray(todos)) return [];
  return todos.flatMap((t): NormalizedTodo[] => {
    if (!t || typeof t !== "object") return [];
    const o = t as Record<string, unknown>;
    const text =
      (typeof o.content === "string" && o.content) ||
      (typeof o.text === "string" && o.text) ||
      "";
    if (!text) return [];
    let status: TodoStatus;
    if (typeof o.status === "string") {
      // Claude path. Trust the enum; clamp anything unknown to pending.
      status =
        o.status === "completed" || o.status === "in_progress"
          ? o.status
          : "pending";
    } else if (typeof o.completed === "boolean") {
      // Codex path.
      status = o.completed ? "completed" : "pending";
    } else {
      status = "pending";
    }
    const activeForm =
      typeof o.activeForm === "string" ? o.activeForm : undefined;
    return [{ text, status, activeForm }];
  });
}
```

Drop entries with no `text` rather than rendering an empty row — a
provider schema flip would show as fewer items rather than a broken
panel.

---

## 5. State management

### 5.1 Approach: derive, don't store

The latest TodoWrite is **derivable** from `ConversationState.messages`:
filter for `m.kind === "tool" && m.tool === "TodoWrite"`, pick the last
one. No reducer changes, no new state field, no migration. Snapshot
replay just works because the underlying tool messages come back from
the snapshot.

```ts
// web/src/lib/todos.ts
export function selectLatestTodoSnapshot(
  state: ConversationState,
): TodoSnapshot | null {
  // Walk messages in reverse for O(1) common case (latest plan is the
  // most recent tool message in active sessions).
  for (let i = state.messages.length - 1; i >= 0; i--) {
    const m = state.messages[i];
    if (m.kind !== "tool" || m.tool !== "TodoWrite") continue;
    const todos = normalizeTodos(m.input);
    if (todos.length === 0) continue;
    return {
      todos,
      source_message_id: m.id,
      updated_at: m.ended_at ?? m.started_at ?? 0,
      turn_id: m.turn_id,
    };
  }
  return null;
}
```

Memoize at the component boundary with `useMemo` keyed on
`state.messages` (referentially stable thanks to the reducer's
immutable updates — every `tool.call` / `tool.result` re-creates the
array). The walk is bounded by `messages.length` but short-circuits on
the first hit; cost is negligible vs. the existing snapshot replay
work.

### 5.2 When to upgrade to stored state

Move to a reducer-stored `latestTodoSnapshot` field if any of these
become true:
- We add per-turn history view (need to keep older revisions addressable).
- We add cross-session aggregation (need to project todos onto sessions list).
- Profiling shows the selector is hot enough to matter (unlikely for transcripts under ~10k messages).

Until then, derive.

---

## 6. Component architecture

```
ConversationView                              (existing)
└─ <TodoPanel snapshot={…} />                 (NEW, sticky)
└─ <div className="conversation">             (existing)
   └─ TurnGroup                               (existing)
      └─ MessageRow                           (existing)
         └─ ToolBlock                         (existing — TodoWrite path NEW: render pill)
```

### 6.1 `TodoPanel` — sticky checklist

`/home/user/agentctl/web/src/components/TodoPanel.tsx` (NEW)

```tsx
import { useState, useMemo } from "react";
import type { TodoSnapshot, NormalizedTodo } from "../lib/todos";

interface Props {
  snapshot: TodoSnapshot | null;
}

export function TodoPanel({ snapshot }: Props) {
  const [collapsed, setCollapsed] = useState(false);
  if (!snapshot || snapshot.todos.length === 0) return null;

  const { todos } = snapshot;
  const done = todos.filter((t) => t.status === "completed").length;
  const inProgress = todos.find((t) => t.status === "in_progress");
  const total = todos.length;
  const pct = total === 0 ? 0 : Math.round((done / total) * 100);

  return (
    <aside
      className={`todo-panel ${collapsed ? "collapsed" : ""}`}
      aria-label="Agent plan"
    >
      <button
        type="button"
        className="todo-panel-header"
        onClick={() => setCollapsed((c) => !c)}
        aria-expanded={!collapsed}
      >
        <span className="todo-panel-chevron" aria-hidden>
          {collapsed ? "▶" : "▼"}
        </span>
        <span className="todo-panel-title">
          Plan
          {inProgress && (
            <span className="todo-panel-active">
              · {inProgress.activeForm ?? inProgress.text}
            </span>
          )}
        </span>
        <span className="todo-panel-progress" title={`${done} of ${total} done`}>
          {done}/{total} · {pct}%
        </span>
      </button>

      {!collapsed && (
        <ol className="todo-panel-list">
          {todos.map((t, i) => (
            <TodoRow key={`${i}-${t.text}`} todo={t} />
          ))}
        </ol>
      )}
    </aside>
  );
}

function TodoRow({ todo }: { todo: NormalizedTodo }) {
  const icon =
    todo.status === "completed" ? "✓" :
    todo.status === "in_progress" ? "●" : "○";
  return (
    <li className={`todo-row todo-${todo.status}`}>
      <span className="todo-icon" aria-hidden>{icon}</span>
      <span className="todo-text">
        {todo.status === "in_progress" && todo.activeForm
          ? todo.activeForm
          : todo.text}
      </span>
    </li>
  );
}
```

### 6.2 Inline pill replacement

`/home/user/agentctl/web/src/components/messages/ToolBlock.tsx` — currently
renders TodoWrite with the standard tool card. Change the render path
for `message.tool === "TodoWrite"` to a compact one-line pill:

```tsx
// inside ToolBlock, before the default card render
if (message.tool === "TodoWrite") {
  const todos = normalizeTodos(message.input);
  const done = todos.filter((t) => t.status === "completed").length;
  return (
    <div className="msg tool tool-todo-pill" id={`msg-${message.id}`}>
      <div className="avatar" aria-hidden>📋</div>
      <div className="body">
        <span className="tool-verb">Updated plan</span>
        <span className="tool-detail">
          {done}/{todos.length} done
        </span>
      </div>
    </div>
  );
}
```

Rationale: the full list lives in `TodoPanel` (always visible) — duplicating it inline is noise. The pill keeps the "plan changed at this moment" signal in the timeline without taking up vertical space.

### 6.3 ConversationView wiring

`/home/user/agentctl/web/src/components/ConversationView.tsx` — add one
import, one selector call, one component:

```tsx
import { TodoPanel } from "./TodoPanel";
import { selectLatestTodoSnapshot } from "../lib/todos";

// inside the component, after `const groups = groupByTurn(visible);`
const todoSnapshot = useMemo(
  () => selectLatestTodoSnapshot({ messages } as ConversationState),
  [messages],
);

// inside the return, before the existing <div className="conversation">:
return (
  <div className="conversation-wrap">
    <TodoPanel snapshot={todoSnapshot} />
    <div className="conversation" ref={scrollRef} onScroll={onScroll}>
      {/* …existing children… */}
    </div>
    {/* …existing jump-pill… */}
  </div>
);
```

The selector accepts the full `ConversationState` shape so it composes
with the eventual "promote to stored state" migration; for v1 we pass
just `{ messages }` cast.

---

## 7. Layout & styling

Add to `/home/user/agentctl/web/src/styles.css`. Use the existing
design tokens (`--c-surface`, `--c-border`, `--c-fg`, `--c-accent`,
`--c-muted`).

```css
.todo-panel {
  position: sticky;
  top: 0;
  z-index: 5;
  background: var(--c-surface);
  border-bottom: 1px solid var(--c-border);
  padding: 8px 32px;
  /* Slight shadow when scrolled so the panel reads as elevated */
  box-shadow: 0 2px 8px -4px rgba(0, 0, 0, 0.08);
}

.todo-panel-header {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  padding: 0;
  background: none;
  border: 0;
  cursor: pointer;
  font: inherit;
  color: var(--c-fg);
  text-align: left;
}

.todo-panel-chevron {
  font-size: 0.7em;
  color: var(--c-muted);
}

.todo-panel-title {
  font-weight: 600;
}

.todo-panel-active {
  font-weight: 400;
  color: var(--c-muted);
  /* Keep on one line; ellipsize if the active task is long. */
  display: inline-block;
  max-width: 40ch;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  vertical-align: middle;
}

.todo-panel-progress {
  margin-left: auto;
  font-variant-numeric: tabular-nums;
  color: var(--c-muted);
  font-size: 0.875em;
}

.todo-panel-list {
  margin: 8px 0 4px;
  padding: 0;
  list-style: none;
  /* Cap height — the panel shouldn't dominate the screen on plans with 30+ items. */
  max-height: 280px;
  overflow-y: auto;
}

.todo-row {
  display: flex;
  align-items: baseline;
  gap: 8px;
  padding: 3px 0;
  font-size: 0.9375em;
  line-height: 1.4;
}

.todo-row.todo-completed .todo-text {
  text-decoration: line-through;
  color: var(--c-muted);
}

.todo-row.todo-in_progress {
  /* Subtle accent so the eye finds it. */
  font-weight: 500;
}

.todo-row.todo-in_progress .todo-icon {
  color: var(--c-accent);
  /* Optional: pulse the dot. Add only if it doesn't violate prefers-reduced-motion. */
  animation: todo-pulse 1.6s ease-in-out infinite;
}

.todo-row.todo-pending .todo-icon {
  color: var(--c-muted);
}

.todo-icon {
  font-family: var(--font-mono, monospace);
  width: 1em;
  flex-shrink: 0;
}

@keyframes todo-pulse {
  0%, 100% { opacity: 1; }
  50%      { opacity: 0.4; }
}

@media (prefers-reduced-motion: reduce) {
  .todo-row.todo-in_progress .todo-icon {
    animation: none;
  }
}

/* Collapsed pill in the transcript */
.msg.tool.tool-todo-pill .body {
  flex-direction: row;
  align-items: center;
  gap: 8px;
  font-size: 0.9em;
  color: var(--c-muted);
}
.msg.tool.tool-todo-pill .tool-verb {
  font-weight: 500;
  color: var(--c-fg);
}
```

**Sticky offset gotcha:** if the page already has a sticky header above
`.conversation-wrap`, `top: 0` puts the todo panel under it. Inspect
`SessionDetail.tsx` (the parent) and set `top` to the header height,
or use `top: var(--app-header-height, 0)` and define the token at the
root.

---

## 8. Edge cases

| Case | Behavior |
|---|---|
| No TodoWrite ever emitted | Panel doesn't render. |
| TodoWrite called with empty `todos: []` | Treat as "plan cleared" — panel doesn't render. |
| Plan has only completed items | Panel renders; progress chip shows `N/N · 100%`. |
| Plan has 30+ items | Panel list scrolls within `max-height: 280px`; outer page scroll unaffected. |
| Codex emits multiple `item.updated` for a todo_list mid-turn | Each update arrives as a separate `tool.result` and updates the underlying tool message; the selector naturally picks the latest. (Today's wire path only emits `tool.call` on `item.started` and `tool.result` on `item.completed`, so this is moot until we wire `item.updated` — out of scope.) |
| Snapshot replay on session reload | Tool messages come back in the snapshot; selector finds them automatically. No special handling. |
| User switches sessions mid-stream | `messages` prop changes; selector re-runs; panel updates or hides. |
| TodoWrite from a sub-agent (Claude `Task` tool) | The sub-agent's TodoWrite arrives as a tool call inside the sub-agent's context; surface depends on how today's code handles sub-agent events. **Defer**: out of scope for v1, but worth a follow-up if sub-agent usage is common. |
| Plan revisions during a turn (Claude updates multiple times) | Selector picks the latest revision. Older revisions remain as collapsed inline pills in the transcript, preserving history. |
| TodoWrite input is malformed (missing `todos` field) | `normalizeTodos` returns `[]`; panel doesn't render. No crash. |

---

## 9. Accessibility

- Panel header is a `<button>` with `aria-expanded`, so screen readers
  announce the collapse state.
- `<aside aria-label="Agent plan">` exposes the region by role.
- `<ol>` (ordered list) for the todo list — order matters semantically.
- In-progress pulse animation respects `prefers-reduced-motion`.
- Status icons (`✓`, `●`, `○`) are decorative; the row's
  `data-status`-like class (`todo-completed` / `todo-in_progress` /
  `todo-pending`) drives screen-reader visibility via a `.sr-only`
  span if needed — add when a real SR pass surfaces problems.
- Keyboard: header `<button>` is tab-stop; Enter/Space toggles. No
  custom keyboard handlers needed.

---

## 10. Testing

### 10.1 Unit tests — `web/src/lib/todos.test.ts` (NEW)

| Test | Input | Expected |
|---|---|---|
| `normalizeTodos` handles Claude shape | `{todos:[{content,status,activeForm}]}` | One `NormalizedTodo`, status preserved |
| `normalizeTodos` handles Codex shape | `{todos:[{text,completed}]}` | One `NormalizedTodo`, status mapped from bool |
| `normalizeTodos` clamps unknown status | `{todos:[{content:"x",status:"foo"}]}` | status === "pending" |
| `normalizeTodos` drops empty text | `{todos:[{content:""},{text:"ok"}]}` | One entry, text "ok" |
| `normalizeTodos` handles missing input | `null`, `{}`, `{todos:"oops"}` | `[]` |
| `selectLatestTodoSnapshot` picks the most recent TodoWrite | Two TodoWrite messages | Returns the second |
| `selectLatestTodoSnapshot` returns null with no TodoWrite | Mixed tool messages, no TodoWrite | `null` |
| `selectLatestTodoSnapshot` ignores empty TodoWrite | TodoWrite with `todos: []` | Falls through to earlier non-empty TodoWrite |

### 10.2 Component tests — `web/src/components/TodoPanel.test.tsx` (NEW)

| Test | Assertion |
|---|---|
| Renders nothing when `snapshot === null` | `container.firstChild` is null |
| Renders progress chip `done/total · pct%` | Text content includes `"3/7 · 43%"` |
| Renders in-progress activeForm in header | When `inProgress` has activeForm, it appears next to title |
| Falls back to `text` when no activeForm | activeForm undefined → use plain `text` |
| Toggling header collapses/expands list | Click header twice → list disappears then reappears |
| Completed items get strike-through class | `.todo-completed .todo-text` exists |

### 10.3 Manual / browser tests

- [ ] Send a Claude session, run a multi-step task; watch panel update live.
- [ ] Send a Codex session that emits a todo_list; verify panel renders with pending/completed (no in_progress).
- [ ] Reload mid-session; verify panel rehydrates from snapshot.
- [ ] Trigger 30-item plan; verify list scrolls inside the panel.
- [ ] Toggle reduced-motion in OS; verify pulse animation disabled.
- [ ] Test on mobile width (≤640px); verify panel stays usable.

### 10.4 Integration / regression

- Run existing `web/` tests; nothing should break.
- Verify the inline `TodoWrite` pill renders correctly when filter mode is `"tools"` (`ConversationView.filterMessages` already includes `kind === "tool"`).
- Verify the inline pill is excluded from the `"text"` filter (already excluded since `kind === "tool"`).

---

## 11. Implementation plan

Land in three small PRs to keep review surface tight.

### PR 1 — Data layer + selector (no UI yet)
- `web/src/lib/todos.ts`: `NormalizedTodo`, `TodoSnapshot`, `normalizeTodos`, `selectLatestTodoSnapshot`.
- `web/src/lib/todos.test.ts`: full unit coverage of the normalizer and selector.
- **Acceptance**: tests pass; selector usable from a console / Storybook.

### PR 2 — `TodoPanel` component + styles + ConversationView wiring
- `web/src/components/TodoPanel.tsx` + tests.
- CSS additions in `styles.css` (panel, list, rows, pulse animation, reduced-motion).
- `ConversationView.tsx`: import, memoized selector call, render the panel above `.conversation`.
- Verify sticky offset doesn't collide with the page header.
- **Acceptance**: manual browser test on a live Claude session shows the panel; toggle works; collapsed state persists across reload (use `localStorage` keyed by session id, optional polish).

### PR 3 — Inline pill replacement + de-noise
- `ToolBlock.tsx`: short-circuit `tool === "TodoWrite"` to the compact pill.
- Adjust styles for `.msg.tool.tool-todo-pill`.
- Verify a session with 5 plan updates shows 5 pills + 1 live panel, not 5 expandable cards.
- **Acceptance**: transcript is visibly less noisy on plan-heavy sessions.

Each PR is independently shippable; PR 1 alone adds dead code, PR 2
adds the visible feature, PR 3 polishes the inline behavior.

---

## 12. Future work / nice-to-haves

- **Plan diff in the inline pill.** Expanding the pill shows the diff
  vs. the previous revision ("+2 completed, +1 new task").
- **Sticky offset for fullscreen mode.** When the user collapses the
  app header (if such a mode exists), the panel adjusts.
- **Click-through.** Clicking a completed todo scrolls the transcript
  to the moment that item was marked done.
- **Cross-stage view for task-chat.** When a task moves through
  multiple stages (each its own session), aggregate todos across the
  task. Requires a new server-side endpoint that walks the task's
  stages — not just a frontend change.
- **Promote to stored state.** Move `selectLatestTodoSnapshot` into
  the reducer as `latestTodoSnapshot` when we need per-turn history or
  cross-session projection.
- **Optimistic UI for AskUserQuestion.** Out of scope here, but the
  same panel pattern (sticky, derived from the latest tool event)
  works for "agent is waiting for your answer" prompts once interactive
  Q&A lands.

---

## 13. Open questions

1. **Should the panel be per-session or per-task?** A task-chat
   navigates between session detail pages; today the panel would reset
   per page. Acceptable for v1?
2. **Persist collapsed state where?** Per-session in localStorage, or
   per-user global? Recommend per-user global (one preference, applies
   everywhere) unless usability testing says otherwise.
3. **What if the agent calls TodoWrite with the same todos but a
   different order?** The selector currently treats it as a new
   snapshot. Probably fine — agents don't reorder casually.
4. **Mobile width?** Should the panel become a collapsible drawer
   triggered from a header icon? Out of scope for v1, but worth
   testing how the current desktop layout degrades at <640px.

---

## 14. Anti-goals (explicitly **not** doing)

- **Don't add a new wire event.** Today's `tool.call` / `tool.result`
  carry everything we need. Adding a `plan.update` event kind would
  couple the contract to UI concerns.
- **Don't normalize the input shape inside the driver.** Codex's
  `{text, completed}` differs from Claude's `{content, status,
  activeForm}` — keep both as-is on the wire; normalize only at the
  rendering boundary. This keeps the driver layer faithful to each
  provider's native shape (useful for debugging and for any non-UI
  consumer).
- **Don't store TodoWrite history separately in the daemon.** The
  message stream already preserves it.
- **Don't render the panel for any tool other than TodoWrite.** This
  is a TodoWrite-specific UX, not a generic "tool with progress"
  pattern. If we later add a `Plan` tool or similar, design that
  separately.
