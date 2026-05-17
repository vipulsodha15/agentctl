# agentctl — Task-Driven Assembly lines (v0 Requirements)

This document specifies the **task-driven, multi-agent assembly line** system
layered on top of agentctl's v1 session/skill/MCP primitives. It introduces
three new top-level objects — **Agents**, **Assembly lines**, and **Tasks** —
and the chat-thread UI that drives them.

This is the input to a separate technical-architecture pass.

## 1. Overview

In v1, a developer starts an agentctl session and chats with one agent
against a repo. The agent's role, skills, and tools are fixed per session.

The gap this document closes:

- A *task* (bug, feature, enhancement) is the unit of work, not "a session."
- Different task types want different multi-step patterns — a bug wants
  investigate → plan → execute; a feature might want spec → design →
  implement.
- Real work needs role-distinct agents, not one prompt doing everything.

**Vision.** A developer creates a task, attaches a assembly line to it, and the
assembly line runs an ordered chain of role-distinct agents. Each agent does
its phase in full process and disk isolation (own container, own volume),
produces a synthesis when the user clicks Hand off, and that synthesis
seeds the next agent. The user sees one continuous chat thread per task
with role shifts at each handoff seam.

**v0 scope.** Build the agent/assembly line/task primitives end-to-end against
one concrete vertical slice — a `bug` assembly line with three built-in agents
(`bug-investigator`, `bug-planner`, `bug-executor`). The primitives are
generic; custom assembly lines and custom agents are authorable from day one.
Manual task creation only.

**Explicitly out of v0:** backtracking, approval gates per stage, shared
workspace across stages, Timeline tab, audit-log table, per-agent handoff
templates, per-stage assembly line notes, idle timeouts, workspace TTL/janitor,
DAG / branching / parallel assembly lines.

## 2. Components and glossary

| Actor | Status | What it is | Where it lives |
|---|---|---|---|
| **Skill** | existing | A `SKILL.md` — prompt fragment / instructions. | `~/.local/share/agentctl/{builtin,custom}-skills/<name>/SKILL.md` |
| **MCP server** | existing | External tool server. | `mcp_registry` table |
| **Agent** | **new** | A reusable session template: prompt, MCP allowlist, skill allowlist, model. | `~/.local/share/agentctl/agents/<name>.yaml` |
| **Assembly line** | **new** | An ordered list of agent names with a name and description. | `~/.local/share/agentctl/assembly-lines/<name>.yaml` |
| **Task** | **new** | A live run: *this assembly line + this issue/task + this repo*. The chat thread is its body. | `tasks` table in sqlite |
| **Stage** | **new** | One agent's run within a task. Status: `pending` / `active` / `done`. Backed by one Session. | `stages` table in sqlite |

**Synthesis** — a single chat message produced by the current agent in
response to a user-triggered auto-prompt at the end of its stage. It is
the seed input to the next stage and the only thing that crosses the
stage boundary. No separate artifact files, no document editor.

**Seam** — the visual rule in the chat thread marking a forward stage
transition (`─── handed off to Planner ───`).

## 3. Architecture principles

1. **Tasks sit above Sessions; Sessions remain the schedulable unit.**
   A stage is backed by exactly one session.
2. **Per-stage isolation.** Each stage gets its own container and its own
   volume. The repo is cloned fresh into the volume at the task's
   recorded base SHA when the stage starts. The synthesis is the only
   artifact that crosses the stage boundary.
3. **One stage active per task at a time.** Strict linear sequence.
4. **Forward-only flow.** No backtracking in v0. If the user is unhappy
   with a stage, they abandon and start a new task.
5. **Chat-first refinement, no editor in between.** The user shapes a
   stage's output by talking to its agent before clicking Hand off.
6. **agentd is the sole writer of task and stage state.** Clients emit
   *intent* (`AttachAssembly line`, `Handoff`, `Complete`, `Abandon`, `Send`);
   containers stream output. agentd interprets these into state
   transitions.
7. **CLI and Web UI parity preserved** (mirrors v1 R4).

## 4. Non-functional requirements

| Area | Target |
|---|---|
| Stage transition latency (Hand off click → next stage's first reply streams) | ≤ 15s p50 (incl. fresh clone + container start) |
| Concurrent tasks on a developer machine | ≥ 5 |
| Chat thread render | ≤ 100ms for threads ≤ 100 messages |
| Assembly line YAML size | ≤ 16 KB |
| Agent YAML size | ≤ 16 KB |

## 5. Defaults

| Setting | Default |
|---|---|
| Built-in agents | `bug-investigator`, `bug-planner`, `bug-executor` |
| Built-in assembly lines | `bug` |
| Default agent model | Inherits from `[session].default_model` |
| Stage palette | Blue (investigator), Purple (planner), Green (executor); 6-swatch palette for custom agents |

---

## Requirements

### R1. Agent as a first-class object

A developer creates, edits, lists, and removes named agents. An agent
bundles everything a session needs to run a single role: prompt, MCPs,
skills, model, and a colour for UI identity.

**Agent schema** (`~/.local/share/agentctl/agents/<name>.yaml`):

```yaml
name: bug-investigator                      # required, unique slug
description: Investigates bug tickets...    # one-liner for pickers
colour: blue                                # one of: blue, purple, green, amber, red, slate
model: claude-opus-4-7                      # optional; falls back to default_model
prompt: |
  You are a bug investigator. Read the issue, explore the repo,
  gather evidence, and produce a clear synthesis for the planner.
mcps_allowed:                               # optional; allowlist from MCP registry
  - github
  - filesystem
skills_allowed:                             # optional; allowlist from skill library
  - filesystem-nav
  - git-archeology
```

**Storage.** YAML files at `~/.local/share/agentctl/agents/<name>.yaml`.
Built-in agents ship in the install template and are copied to the user's
directory at `agentctl init`. The agentd in-memory index is reloaded on
fsnotify file changes and on agentd start.

**Built-in agents shipped in v0.**

| Name | Role | MCPs | Skills |
|---|---|---|---|
| `bug-investigator` | Reads issue, explores repo, gathers evidence. | github, filesystem | filesystem-nav, git-archeology |
| `bug-planner` | Reads investigator's synthesis, proposes a fix plan with test plan. | github | code-review, test-design |
| `bug-executor` | Reads planner's synthesis, makes code changes, runs tests, opens a PR. | github, filesystem | git-commit, test-runner |

Built-in agents are read-only in the UI; "Edit a copy" creates a custom
agent prefilled from the built-in.

**CLI surface.**

| Command | Behavior |
|---|---|
| `agentctl agent list` | Tabular list (name, description, builtin/custom, model). |
| `agentctl agent show <name>` | Print agent YAML. |
| `agentctl agent add <name> --from <path>` | Add a custom agent from a YAML file. |
| `agentctl agent edit <name>` | Open the agent YAML in `$EDITOR`. Built-ins refuse. |
| `agentctl agent remove <name>` | Delete a custom agent. Refuses if any assembly line references it. |

**Web UI — Agents tab** (sibling to Skills and MCPs in the nav). Two-pane
layout: searchable list on the left, edit form on the right. Form fields:
name, description, colour (6-swatch dropdown), model (Opus/Sonnet/Haiku/
inherit), prompt (Markdown textarea), MCPs allowed (multi-select chip
picker), skills allowed (multi-select chip picker). Built-in agents show
"Edit a copy" instead of Save.

**Acceptance.**

- A new custom agent created in the UI is immediately usable in the
  Assembly line composer.
- The same set of agents is visible from `agentctl agent list` and the
  Agents tab.
- Editing an agent's YAML on disk reflects in the Agents tab within 2s.
- Removing an agent referenced by a assembly line fails with a clear error
  listing the referencing assembly lines.

**Error cases.** Duplicate name on add → reject. Missing `name` or
`prompt` → reject. References to non-existent MCPs or skills → warn at
load, skip at session start.

---

### R2. Assembly line as an ordered list of agents

A assembly line is just a name, a description, and an ordered list of agent
names. No per-stage configuration in v0.

**Assembly line schema** (`~/.local/share/agentctl/assembly-lines/<name>.yaml`):

```yaml
name: bug
description: Bug fix assembly line.
stages:
  - agent: bug-investigator
  - agent: bug-planner
  - agent: bug-executor
```

**Constraint.** Linear chains only. ≥ 1 stage. Each stage is just an agent
reference; no per-stage approval flag, no per-stage notes.

**Built-in assembly lines shipped in v0.**

| Name | Stages |
|---|---|
| `bug` | bug-investigator → bug-planner → bug-executor |
| `bug-multi-provider` | bug-investigator (anthropic) → bug-planner → bug-executor (openai) |

**Per-stage provider pins.** Stages may carry an optional `provider:` and
`model:` to pin which agent runtime spawns the stage (ADR 0020 §3 —
orchestration as the headline). When unset, the resolver picks one at
session-create time from the workspace defaults, so the same agent YAML
runs unchanged on whichever provider the user has configured. The
`bug-multi-provider` built-in is the reference example: it ships a
mixed-provider line whose `investigator` pins Anthropic (depth-of-
exploration argument) and whose `executor` pins OpenAI (edit-and-test
loop). The `planner` stage stays unpinned so it runs on the workspace
default.

```yaml
# Excerpt from internal/ttl/builtins/assembly-lines/bug-multi-provider.yaml
stages:
  - agent: bug-investigator
    provider: anthropic
  - agent: bug-planner
  - agent: bug-executor
    provider: openai
```

agentctl frames itself as an **orchestrator across providers**, not as a
client of any one of them. The mixed-provider built-in is the destination
the rest of the codex-provider work earns; surfaces (CLI run view, web
StageStrip chip) show each stage's provider+model as a first-class visual
whenever a line actually mixes runtimes, and stay invisible when it
doesn't.

**CLI surface.**

| Command | Behavior |
|---|---|
| `agentctl assembly-line list` | List assembly lines (name, description, stage count, builtin/custom). |
| `agentctl assembly-line show <name>` | Print the assembly line YAML. |
| `agentctl assembly-line add <name> --from <path>` | Add a custom assembly line. |
| `agentctl assembly-line edit <name>` | Open in `$EDITOR`. Refuses if any task in `working` references it. |
| `agentctl assembly-line remove <name>` | Delete. Refuses if any task in `working` references it. |

**Web UI — Assembly lines tab.** Two-pane layout, composer on the right:

- Name, description (text inputs).
- **Stages**: vertically stacked ordered list. Each row contains an
  agent picker (avatar + colour pill + name), up/down arrow buttons for
  reordering, and a trash icon.
- "+ Add stage" button opens an agent picker showing cards of every
  available agent (avatar, colour, name, description, builtin badge).
- Validation banner at top showing any errors (missing agents, empty
  stage list).

```
┌────────────────────────────────────────────────────────────┐
│ Assembly lines                                                  │
├──────────────┬─────────────────────────────────────────────┤
│ + New        │ Name:        [ bug                       ]  │
│              │ Description: [ Bug fix assembly line          ]  │
│ ▌ bug        │                                             │
│   builtin    │ Stages                                      │
│              │ ┌─────────────────────────────────────────┐ │
│ ▌ my-spike   │ │ 🟦 bug-investigator         ▲ ▼  🗑    │ │
│              │ └─────────────────────────────────────────┘ │
│              │ ┌─────────────────────────────────────────┐ │
│              │ │ 🟪 bug-planner              ▲ ▼  🗑    │ │
│              │ └─────────────────────────────────────────┘ │
│              │ ┌─────────────────────────────────────────┐ │
│              │ │ 🟩 bug-executor             ▲ ▼  🗑    │ │
│              │ └─────────────────────────────────────────┘ │
│              │ [ + Add stage ]                             │
│              │                                             │
│              │ [ Save ] [ Cancel ]                         │
└──────────────┴─────────────────────────────────────────────┘
```

**Acceptance.**

- A new assembly line created in the composer is immediately startable as a
  task.
- A assembly line referencing a non-existent agent fails validation at save.
- Editing a assembly line's YAML on disk is reflected in the UI within 2s.

---

### R3. Task lifecycle

**Task creation inputs.**

- **Source** (one of):
  - GitHub issue URL — agentd fetches title + body via GitHub MCP at
    task start.
  - Freeform — title + body typed into a form.
- **Repo URL** — same `--repo` semantics as v1.
- **Assembly line** — optional at create time; can be attached later.
- **Name** (optional) — display label; defaults to the issue title or
  first 60 chars of the freeform task.

**Status lifecycle (4 states).**

```
   ┌──────────────┐  user attaches assembly line
   │ not-started  │────────────────────────────┐
   └──────┬───────┘                            │
          │ created with assembly line              ▼
          │                             ┌───────────┐
          └────────────────────────────▶│  working  │
                                        └─────┬─────┘
                                              │
        ┌─────────────────────────────────────┼─────────────────────────────────┐
        │ user clicks Complete on final stage │ user clicks Abandon at any time │
        ▼                                     ▼                                 ▼
   ┌──────────┐                          ┌────────────┐
   │   done   │                          │ abandoned  │
   └──────────┘                          └────────────┘
```

- `not-started` — task exists but no assembly line attached. No stages, no
  sessions, no clones.
- `working` — assembly line attached; at least one stage is `active` or
  `pending`.
- `done` — user clicked "Complete task" on the chat composer (only
  available when the final stage is `active`).
- `abandoned` — user clicked "Abandon task" at any point.

There is no automatic transition to `done`. The final stage finishes its
work, the user reviews the chat, then clicks Complete.

**agentd writes task status in response to these intents.**

| Task transition | Intent |
|---|---|
| `not-started → working` | AttachAssembly line (or task created with assembly line) |
| `working → done` | Complete (only allowed when current stage is the final stage and `active`) |
| any → `abandoned` | Abandon |

**Base SHA recorded at assembly line attach.** When a assembly line is attached
(at task creation if a assembly line is provided, otherwise at the later
`AttachAssembly line`), agentd resolves the repo's default branch HEAD and
records it as the task's `base_sha`. Every stage clones at this SHA.

**Per-stage isolation.** When a stage transitions to `active`:

1. agentd spawns a new session against the agent's config.
2. The session's container mounts a fresh per-stage Docker volume
   (`agentctl-stage-<stage_id>`).
3. The container clones the repo at `base_sha` into the volume.
4. agentd writes `/workspace/.agentctl/task/issue.md` (the task's issue
   body).
5. agentd writes `/workspace/.agentctl/task/handoff-in.md` containing
   the prior stage's synthesis (stages 2+ only).
6. agentd seeds the session with its first user-message:
   - **Stage 1**: *"A new task has been opened. The issue is at
     `.agentctl/task/issue.md`. The next agent in this assembly line is
     `<next_agent>` (or 'You are the final stage'). Investigate per your
     role; when you are ready to hand off, say so explicitly in chat."*
   - **Stage N > 1**: *"You are receiving handoff from `<prev_agent>`.
     Their synthesis is at `.agentctl/task/handoff-in.md`. The next agent
     in this assembly line is `<next_agent>` (or 'You are the final stage').
     Begin per your role; when you are ready to hand off, say so
     explicitly in chat."*

When a stage transitions to `done`, its container is stopped and its
volume is destroyed.

**Branch handling.** Each stage operates in its own clone, so there is no
shared branch across stages. Stages 1 and 2 don't push (they only read).
Stage 3 (executor) creates `claude/task-<task_id>` locally, commits, and
pushes to the remote, then opens a PR via GitHub MCP. The branch lives on
GitHub, not in any local volume.

**Abandon mechanics.** On Abandon:

- Current stage's session is stopped within 5s; its volume is destroyed.
- Task status flips to `abandoned`. No further stages spawn.
- Chat thread is preserved.

**CLI surface.**

| Command | Behavior |
|---|---|
| `agentctl task create [--assembly line <name>] [--repo <url>] [--issue <gh-url> \| --task <title>] [--name <name>]` | Create a task. If no assembly line, status is `not-started`. |
| `agentctl task attach <id> --assembly line <name>` | Attach a assembly line to a `not-started` task. |
| `agentctl task ls` | List tasks with status, assembly line, current stage, last activity, cost. |
| `agentctl task show <id>` | Detailed status: stage timeline, current synthesis, PR URL if any. |
| `agentctl task open <id>` | Attach the terminal to the task's chat thread. |
| `agentctl task handoff <id>` | Trigger handoff on the current stage. |
| `agentctl task complete <id>` | Mark task `done`. |
| `agentctl task abandon <id>` | Abandon. |

**Acceptance.**

- `agentctl task create` with `--assembly line` returns within the stage
  transition budget with stage 1 `active`.
- A `not-started` task has no stages; on `attach`, stages are created in
  `pending` and stage 1 transitions to `active`.
- A task in `working` always has exactly one stage `active`.
- Abandon from any state transitions to `abandoned`, stopping the current
  stage's session within 5s.
- `task complete` is rejected unless the current stage is the final stage
  and is `active`.

**Error cases.**

- Invalid GitHub issue URL or unreachable issue → task creation fails
  before any stage spawns.
- Repo clone fails on stage start → chat thread surfaces the error;
  stage stays `active`; the user can retry by sending a message or
  Abandon.
- Assembly line attached but references a now-removed agent → attach fails
  with a clear error.

---

### R4. Stage execution model

**Stage status (3 states).** `pending` → `active` → `done`. No
`awaiting-approval`, no `paused-for-backtrack`, no `failed`.

```
   ┌─────────┐
   │ pending │  prior stage done OR assembly line just attached & this is stage 1
   └────┬────┘
        │ agentd spawns session, clones repo, seeds workspace
        ▼
   ┌─────────┐
   │ active  │  one stage at a time per task
   └────┬────┘
        │ user clicks Hand off → auto-prompt → agent emits synthesis
        ▼
   ┌─────────┐
   │  done   │  container stopped, volume destroyed, synthesis durable
   └─────────┘
```

**Hand off mechanics.**

1. User clicks `[Hand off to <next> ▸]` on an `active` stage (CLI:
   `agentctl task handoff <id>`).
2. agentd injects this auto-prompt as a user-role message into the
   session:

   ```
   Produce your handoff for the next stage now. The next agent only
   receives this document — your chat history is not carried forward,
   so anything you want them to have must appear below.

   ## Deliverable
   Your role's actual output — the plan, RCA, findings, review, design
   notes, patch summary, whatever you were asked to produce. Reproduce
   it here in full; do not compress or paraphrase. If you already wrote
   it earlier in chat, restate it here so this document stands alone.

   ## Key evidence
   Concrete pointers — file:line refs, log excerpts, repro steps, links.
   Be specific.

   ## Recommendation for the next stage
   What the next agent should do first, what to be careful about, what
   not to redo.

   ## Open questions
   Anything you could not resolve.
   ```

3. The agent emits one chat message in this structure.
4. agentd locks this message as the stage's `synthesis` (durable on the
   stage row).
5. Stage transitions `active → done`. Container stopped, volume destroyed.
6. If a next stage exists, it transitions `pending → active` per R3
   (fresh container, fresh volume, fresh clone, prior synthesis written
   to `handoff-in.md`).
7. If no next stage, the chat composer's primary button changes from
   `[Hand off to <next> ▸]` to `[Complete task ✓]`.

**Failure handling.** There is no `failed` state. If a session crashes,
the repo clone fails, or the agent emits an unrecoverable error, the
chat thread surfaces the failure, the stage stays `active`, and the user
chooses between sending another message (retry the agent) and Abandon.

**Invariants.**

1. A task in `working` has exactly one stage in `active`; earlier stages
   are `done`, later stages are `pending`.
2. A stage's container and volume exist iff its status is `active`.
3. A stage's `synthesis` is written exactly once, atomically with the
   `active → done` transition.
4. `done` and `abandoned` are terminal task statuses; no transitions out
   of them.

**Worked example (bug assembly line, happy path).**

| # | Event | Task | Stage 1 (Inv.) | Stage 2 (Pln.) | Stage 3 (Exc.) |
|---|---|---|---|---|---|
| 0 | Task created with `--assembly line bug` | `working` | `active` | `pending` | `pending` |
| 1 | User chats with investigator | `working` | `active` | `pending` | `pending` |
| 2 | User clicks Hand off → synthesis emitted | `working` | `done` | `active` | `pending` |
| 3 | User chats with planner | `working` | `done` | `active` | `pending` |
| 4 | User clicks Hand off → synthesis emitted | `working` | `done` | `done` | `active` |
| 5 | Executor commits, pushes, opens PR, posts result in chat | `working` | `done` | `done` | `active` |
| 6 | User clicks Complete task | `done` | `done` | `done` | `done` |

**Acceptance.**

- A `working` task has exactly one `active` stage at all times.
- The synthesis is durable on the stage row before the next stage
  spawns.
- Stage container + volume teardown completes within 5s of `done`.
- Stage transitions are durable across agentd restart: if agentd
  crashes after writing synthesis but before spawning the next stage,
  on restart the next stage is spawned correctly.

---

### R5. Web UI — Task page

The Task page is where 99% of user interaction happens. It presents the
task as a single continuous chat thread (vertical), with forward stage
transitions marked by horizontal seams and role shifts visible via
colour. A stage rail at the top gives spatial overview. A right-hand tab
switcher exposes Diff and Cost views.

```
┌──────────────────────────────────────────────────────────────┐
│ Task #42 · "Cart abandons on Safari iOS 17"   [ Abandon ]    │
│ assembly line: bug · status: working · current stage: Planner     │
├──────────────────────────────────────────────────────────────┤
│  ✅ Investigator ──── ◔ Planner ──── ○ Executor              │
├─────────────────────────────────────────────┬────────────────┤
│                                             │ ◉ Chat         │
│  ┃ issue.md (seeded)                        │ ○ Diff         │
│  ┃   Users on Safari iOS 17 lose cart…      │ ○ Cost         │
│                                             │                │
│  ▌🟦 bug-investigator                       │                │
│  ▌   Looking at cart/session.ts and ITP…    │                │
│  ▌   [tool: read cart/session.ts:120-180]   │                │
│  ▌   I'm ready to hand off.                 │                │
│                                             │                │
│  ▌🟦 bug-investigator (synthesis)           │                │
│  ▌   ## Deliverable …                       │                │
│  ▌   ## Key evidence …                      │                │
│  ▌   ## Recommendation …                    │                │
│  ▌   ## Open questions …                    │                │
│                                             │                │
│  ─────── handed off to Planner ───────      │                │
│                                             │                │
│  ▌🟪 bug-planner                            │                │
│  ▌   Received handoff. Proposed plan: …     │                │
│                                             │                │
├─────────────────────────────────────────────┴────────────────┤
│ talking to: 🟪 bug-planner                                   │
│ ┌──────────────────────────────────────────────────────┐     │
│ │ type a message…                                      │     │
│ └──────────────────────────────────────────────────────┘     │
│ [ Send ]                          [ Hand off to Executor ▸ ] │
└──────────────────────────────────────────────────────────────┘
```

**Layout elements.**

- **Header row**: task name + ID, "Abandon" action, assembly line name, task
  status, current stage badge.
- **Stage rail**: one pill per stage, in order. Each pill shows the
  agent's colour, name, and status dot (`✅` done, `◔` active, `○`
  pending). Clicking a pill jump-scrolls to the seam for that stage.
- **Center column**: chat thread, vertically scrollable. Each message
  has a left colour stripe matching its stage. Tool calls render
  compactly (re-using v1 R4 rendering). Synthesis messages are
  visually distinguished with a subtle border and a "(synthesis)"
  label after the agent name. Seams are full-width horizontal rules
  with center labels.
- **Right column** (~240 px wide): vertical tab switcher.
  - **Chat** (default).
  - **Diff** — current stage's clone vs base SHA. For `done` stages,
    shows the snapshot taken at hand-off time (empty for read-only
    stages, executor's commits for the last one).
  - **Cost** — sum of session costs across stages.
- **Composer** (sticky at bottom):
  - "talking to: <agent>" label with avatar + colour.
  - Multi-line message input.
  - Buttons: `[Send]` and one of:
    - `[Hand off to <next> ▸]` when current stage is not the final stage.
    - `[Complete task ✓]` when current stage is the final stage.
  - In `not-started` status, the composer is replaced by a assembly line
    picker affordance: *"Attach a assembly line to begin."*
  - In `done` or `abandoned`, the composer is read-only with a banner.

**UX invariants.**

- One composer, always pointed at the current stage's agent.
- Vertical scroll history, no horizontal stage columns.
- Role identity via colour stripe and avatar repeated on every message.
- Assembly line-progression actions only on the composer.

**Acceptance.**

- A task page with ≤ 100 messages renders in ≤ 100 ms after route
  navigation.
- Stage rail reflects status changes within 200 ms.
- Forward seams render with correct labels.
- Composer button correctly switches between Hand off, Complete, and
  read-only.
- Two browser tabs open on the same task stay synchronized.
- The Diff tab scopes to the currently-focused stage's volume.

**Error and edge cases.**

- WebSocket drop → reconnect and replay missed messages (v1 R6 mechanic).
- Two clients click Hand off near-simultaneously → first wins; second
  sees a graceful "stage already advanced" state.
- Repo clone failure surfaces as an inline chat error; composer remains
  active so the user can Abandon or message the agent.

---

### R6. CLI parity

Every UI operation has a CLI equivalent.

| Command group | Commands |
|---|---|
| `agentctl agent` | `list`, `show`, `add`, `edit`, `remove` |
| `agentctl assembly-line` | `list`, `show`, `add`, `edit`, `remove` |
| `agentctl task` | `create`, `attach`, `ls`, `show`, `open`, `handoff`, `complete`, `abandon` |

**Stream attach** (`agentctl task open <id>`) connects the terminal to
the task's chat thread:

- Header line with task name, assembly line, current stage.
- Streaming messages with simple ANSI colouring keyed to the agent's
  colour field.
- Seams as full-width horizontal rules.
- Input prompt shows the current agent: `[bug-planner] >`.
- Slash controls:
  - `/handoff` — hand off current stage.
  - `/complete` — mark task done (final stage only).
  - `/abandon` — abandon.

**Acceptance.**

- All UI assembly line-progression actions have equivalent CLI commands and
  slash controls.
- `agentctl task ls` and the Tasks page list the same set of tasks with
  the same status at any moment.
- `agentctl task show <id>` includes the locked-in synthesis for each
  `done` stage and the workspace branch + base SHA.

---

### R7. Data model

**New sqlite tables.**

`tasks`:

| Column | Type | Notes |
|---|---|---|
| `task_id` | text, PK | UUID. |
| `name` | text | Display label. |
| `assembly line_name` | text, nullable | Null while `not-started`. References `assembly lines/<name>.yaml`. |
| `repo_url` | text | Source repo. |
| `base_sha` | text, nullable | Recorded at assembly line attach. |
| `source_kind` | text | `github_issue` \| `freeform`. |
| `source_url` | text, nullable | Issue URL if `source_kind = github_issue`. |
| `issue_md` | text | Title + body, seeded into each stage's volume. |
| `current_stage_id` | text, FK → stages.stage_id, nullable | Null while `not-started`, `done`, or `abandoned`. |
| `status` | text | `not-started` \| `working` \| `done` \| `abandoned`. |
| `created_at`, `started_at`, `ended_at` | timestamps | `started_at` set on assembly line attach; `ended_at` on `done` or `abandoned`. |

`stages`:

| Column | Type | Notes |
|---|---|---|
| `stage_id` | text, PK | UUID. |
| `task_id` | text, FK → tasks | |
| `position` | int | 1-indexed in assembly line's stage list. |
| `agent_name` | text | Referenced agent at stage creation. |
| `session_id` | text, FK → sessions, nullable | Backing session; null after `done`. |
| `volume_id` | text, nullable | Per-stage volume; null after teardown. |
| `synthesis` | text, nullable | Locked-in synthesis once `done`. |
| `status` | text | `pending` \| `active` \| `done`. |
| `started_at`, `ended_at` | timestamps, nullable | |

No `stage_events` table. No `assembly line_snapshot_json`. State transitions
are reconstructible from the chat message log + stage status fields.

**On-disk YAML layout.**

```
~/.local/share/agentctl/
├── agents/
│   ├── bug-investigator.yaml          # built-in
│   ├── bug-planner.yaml               # built-in
│   ├── bug-executor.yaml              # built-in
│   └── <custom>.yaml
└── assembly lines/
    ├── bug.yaml                       # built-in
    └── <custom>.yaml
```

Per-stage Docker volumes are managed by agentd; no on-disk task
directory tree on the host.

**Workspace volume contents** (per stage, inside the container):

- `/workspace/repo/` — cloned repo at `base_sha`.
- `/workspace/.agentctl/task/issue.md` — task issue body.
- `/workspace/.agentctl/task/handoff-in.md` — prior stage's synthesis
  (stages 2+ only).

**Acceptance.**

- A completed task end-to-end produces durable rows in `tasks` and
  `stages` (one per stage).
- Stage rows persist correctly across agentd restart.
- After a stage reaches `done`, its `volume_id` is null and its
  `synthesis` is set.

---

### R8. Task source: GitHub issue (v0)

In v0 a task can be created from a GitHub issue URL; agentd fetches the
issue text at creation time. Webhook / label-triggered creation is
deferred.

**Behavior on `--issue <gh-url>`.**

1. Parse the URL into `owner/repo#number`.
2. Fetch the issue title + body via GitHub MCP (no comments in v0; the
   investigator can pull them later via MCP if needed).
3. Concatenate into the task's `issue_md` field (used as
   `/workspace/.agentctl/task/issue.md` at each stage start).
4. Record `source_kind = github_issue` and `source_url` on the task row.

**Behavior on `--task "<title>"`.**

1. Prompt for body in `$EDITOR` (CLI) or a textarea (UI).
2. Title + body written to `issue_md` with `source_kind = freeform`.

**No automatic GitHub write-back.** The task does not post comments back
to the source issue, does not change labels, does not update assignees.
The executor stage *does* open a PR (via GitHub MCP) as a side effect of
its agent behavior, not a assembly line-runtime feature.

**Acceptance.**

- `issue.md` is present at stage 1 start with the issue title + body.
- A 404 or 401 from GitHub fails task creation with a clear error before
  any stage spawns.
- The freeform path produces the same `issue.md` shape from a manually
  typed title + body.

---

## 7. Open product questions

### 7.1. Branch collision for executor pushes

The executor pushes `claude/task-<task_id>` to the remote on its first
commit. If a branch with that name already exists out of band, the
executor picks a unique suffix (`claude/task-<task_id>-2`). UUID-based
task IDs make collisions vanishingly rare but not impossible.

### 7.2. Per-stage Diff in the right column

Each stage has its own volume, so the Diff tab shows different content
depending on which stage is focused. For stages 1 and 2 of the bug
assembly line, the diff is empty (read-only stages). UI defaults to the
executor's diff when its stage is `active` or `done`.

### 7.3. Retrying a failed stage

v0: there is no in-place retry. If a stage fails, the user can either
(a) message the agent to try again, or (b) Abandon and create a new
task. The synthesis from the prior task can be copied into the new
task's body if useful.

### 7.4. Assembly line edits while tasks are running

`assembly line edit` and `assembly line remove` refuse when any task in `working`
references the assembly line. Whether to also refuse for `not-started` tasks
(which haven't yet snapshotted anything) is an architect call.

---

## 8. Out of scope for v0

- **Backtracking** (Hand back to a prior stage).
- **Approval gates per stage** (Hand-off click is the universal gate).
- **Per-stage assembly line notes** or per-assembly line agent role overrides.
- **Shared workspace across stages.** Each stage clones fresh.
- **Per-agent handoff template.** One default template is used everywhere.
- **DAG / branching / conditional / parallel assembly lines.**
- **Webhook / label-triggered task creation.**
- **Auto-syncing task status back to the source GitHub issue.**
- **Timeline tab, audit-log table** (`stage_events`).
- **Workspace TTL + janitor pass, idle timeouts.**
- **Stage retry, pause/resume, container pause as a lifecycle state.**
- **Cost limits / budgets / alerts per task.**
- **Multi-step backtrack, cross-agent direct dialogue.**
- **Structured (JSON) handoff artifacts.**
- **Sub-assembly lines** (a stage that runs a assembly line).
- **Assembly line snapshot per task** (live-follow is fine because edits to
  in-use assembly lines are refused).
- **Multi-task batch operations.**
- **Mobile UI for the task page.**
- **Exporting the task transcript as a file.**
- **Commenting on individual stage messages.**

---

## 9. Cross-references

- `requirements.md` — v1 base spec for sessions, MCPs, skills, CLI/UI
  parity, diff and cost views, data-model and architecture principles.
- `architecture/overview.md`, `architecture/data-model.md`,
  `architecture/api.md` — to be extended in the architecture pass.
- `architecture/decisions/` — new ADRs expected: per-stage isolation
  model, synthesis seeding mechanism, task state machine.
