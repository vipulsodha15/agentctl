# agentctl — Ticket-Driven Workflows (v0 Requirements)

This document specifies the **ticket-driven, multi-agent workflow** system
layered on top of agentctl's v1 session/skill/MCP primitives. It introduces
three new top-level objects — **Agents**, **Workflows**, and **Tickets** —
and the chat-thread UI that drives them.

This is the input to a separate technical-architecture pass. Items not
pinned down here are in §10 (open product questions) or §11 (out of scope
for v0).

## 1. Overview

In v1, a developer starts an agentctl session, attaches to it, and chats
with one agent against a repo. The agent's role, skill set, and tools are
fixed per session and configured at start time.

The product gap this document closes:

- A *ticket* (bug report, feature request, enhancement) is the unit of work a
  developer or PM creates, not "a session." Today there is no first-class
  way to attach an agent (or a *sequence* of agents) to a ticket.
- Different ticket types want different work patterns. A bug wants
  investigate → plan → execute. A feature might want spec → design →
  implement. A spike might want research → write-up.
- Real work needs role-distinct agents — an investigator should not be the
  same agent (same prompt, same tools) as a planner or an executor.
- Handoff between phases should be a human-reviewed checkpoint, not an
  invisible automatic transition.

**Vision.** A developer or PM creates a ticket, points it at a workflow,
and the workflow runs an ordered chain of role-distinct agents over a
shared workspace. Each agent does its phase, distills its findings as a
handoff message into the chat thread, the user reviews and refines via
chat, then advances to the next agent. The user sees one continuous chat
thread per ticket with role-shifts at each handoff seam.

**v0 scope.** Build the agent/workflow/ticket primitives end-to-end against
one concrete vertical slice — a `bug` workflow with three built-in agents
(`bug-investigator`, `bug-planner`, `bug-executor`). The primitives are
generic; custom workflows and custom agents are authorable from day one.
Manual ticket creation only — label-triggered/webhook-driven runs are v0.5.

**Why this matters.** Devs do not want another ticket tracker. They want
their existing ticket to *become* a unit of agent work. agentctl already
has the session/container/skill/MCP substrate; this layers the workflow
abstraction on top without forking the primitive.

## 2. Components and glossary

The system has six actors. Two exist today; three are new top-level objects;
one is a new implicit child.

| Actor | Status | What it is | Where it lives |
|---|---|---|---|
| **Skill** | existing | A `SKILL.md` — prompt fragment / instructions, reusable as a building block for an Agent's role. | `~/.local/share/agentctl/{builtin,custom}-skills/<name>/SKILL.md` |
| **MCP server** | existing | External tool server (GitHub, filesystem, custom). | `mcp_registry` table in sqlite. |
| **Agent** | **new** | A reusable session template — role prompt (free-text or skill-backed), skills allowlist, MCP allowlist, model, avatar+colour, handoff template. | `~/.local/share/agentctl/agents/<name>.yaml` |
| **Workflow** | **new** | An ordered chain of Agents with per-step `approval_required` and optional per-step `notes`. | `~/.local/share/agentctl/workflows/<name>.yaml` |
| **Ticket** | **new** | A live run: *this workflow + this issue/task + this repo*. Spans many Stages (and many Sessions) over its lifetime. The chat thread is its body. | `tickets` table in sqlite. |
| **Stage** | **new** | One agent's run within a ticket. Status: `pending` / `active` / `awaiting-approval` / `done` / `paused-for-backtrack`. Backed by one Session under the hood. | `stages` table in sqlite. |

Additional terminology:

- **Handoff message** — the agent's distilled synthesis emitted at stage
  end, structured per that agent's handoff template. It is *the chat
  message* the agent sends in response to the auto-prompt fired by the
  user's "Hand off" click. It serves three purposes: (a) the visible end
  of the current stage in the thread, (b) the seed user-message for the
  next stage, (c) the durable record of what was passed between agents.
  There is no separate artifact file.
- **Workspace** — a shared Docker volume + git branch
  (`claude/ticket-<ticket_id>`) that all stages of a single ticket operate
  over. Materialized at ticket start, torn down at ticket termination.
- **Seam** — the visual rule in the chat thread marking a stage
  transition (`─── handed off to Planner ───` or `─── handed back to
  Investigator ───`).

## 3. Architecture principles

Constraints every requirement and the technical design must respect:

1. **Tickets sit above Sessions; Sessions remain the schedulable unit.**
   agentd still owns containers, volumes, MCP wiring, message streams.
   Tickets are a higher-level grouping object; a stage is backed by
   exactly one session at any moment.
2. **One stage active per ticket at a time.** v0 enforces a strict linear
   in-flight pointer per ticket. Multiple tickets run in parallel (each in
   its own workspace) as v1 already supports.
3. **Handoff content lives in the chat thread.** The "artifact" between
   agents is a chat message — produced on demand by the agent in response
   to a user-triggered auto-prompt. There is no separate document UI, no
   filesystem-as-API convention, no `submit_artifact` tool.
4. **Agents are reusable across workflows; workflows are reusable across
   tickets.** Workflows reference agents by name (not by embedding their
   config). Editing an agent updates every workflow that references it.
   Tickets snapshot the workflow at start time (mid-flight workflow edits
   do not disturb a running ticket; see §10).
5. **CLI and Web UI parity preserved** (mirrors v1 R4). Every ticket /
   workflow / agent operation reachable in the UI is reachable via
   `agentctl`, and vice versa.
6. **Chat-first refinement, no editor in between.** The user shapes a
   stage's output by talking to its agent. There is no Monaco / artifact
   editor between stages. Pushing back in chat ("section 3 is thin, add
   X") is the only refinement mechanism.
7. **Workspaces are durable per ticket.** The shared volume and branch
   persist until the ticket is explicitly terminated (or auto-pruned by
   policy). All stages of a ticket see the same filesystem and git state.

## 4. Non-functional requirements

| Area | Target |
|---|---|
| Stage transition latency (Approve & advance → next stage's first reply streams) | ≤ 10s p50, ≤ 20s p99 (incl. container start) |
| Backtrack latency (Hand back → previous stage's composer is interactive again) | ≤ 10s p50 |
| Concurrent tickets on a developer machine | ≥ 5 with default resource caps; bounded by v1 concurrent-session budget |
| Ticket workspace disk usage | ≤ 1 GB for typical bug workflows; no enforced cap in v0 |
| Chat thread render performance | ≤ 100ms render for threads with ≤ 1000 messages |
| Handoff message round-trip (Hand off click → handoff message rendered) | ≤ 5s p50 (excludes model inference time) |
| Workflow YAML size | ≤ 64 KB, ≤ 50 stages (linear) |
| Agent definition size | ≤ 32 KB per agent YAML |

## 5. Default values

| Setting | Default | Override |
|---|---|---|
| Built-in agents shipped | `bug-investigator`, `bug-planner`, `bug-executor` | Custom agents via Agents UI / CLI |
| Built-in workflows shipped | `bug` | Custom workflows via Workflows UI / CLI |
| Default approval gates | `investigate`: required; `plan`: required; `execute`: not required (PR is the gate) | Per-workflow per-stage |
| Default agent model | Inherits from `[session].default_model` in `config.toml` | Per-agent override in agent YAML |
| Stage colour palette (built-in) | Blue (investigate), Purple (plan), Green (execute) | Per-agent in Agents UI |
| Workspace branch naming | `claude/ticket-<ticket_id>` | Not configurable in v0 |
| Workspace volume retention after ticket termination | 7 days, then pruned | `ticket.workspace_ttl_days` |
| Stage idle timeout (no user activity, no streaming) | 60 min, then session paused (volume preserved) | `ticket.stage_idle_timeout` |

---

## Requirements

### R-W1. Agent as a first-class object

**Goal.** A developer creates, edits, lists, and removes named agents.
An agent bundles everything a session needs to run a single role: prompt,
skills, MCPs, model, identity, and handoff contract. Agents are referenced
by name from workflows.

**Agent schema** (`~/.local/share/agentctl/agents/<name>.yaml`):

```yaml
name: bug-investigator                      # required, unique slug
description: Investigates bug tickets...    # one-liner for pickers
avatar: emoji-or-png-path                   # optional; defaults to first letter
colour: "#3B82F6"                           # optional; hex; defaults from palette
model: claude-opus-4-7                      # optional; falls back to default_model
role:
  # exactly one of `prompt` or `skill`
  prompt: |
    You are a bug investigator. Read the issue, explore the repo...
  skill: bug-investigator                   # alt: points at SKILL.md by name
skills_allowed:                             # optional; allowlist from skill library
  - filesystem-nav
  - git-archeology
mcps_allowed:                               # optional; allowlist from MCP registry
  - github
  - filesystem
handoff_template: |                         # required; Markdown structure
  ## Problem statement
  ## Findings
  ## Evidence (file:line refs, logs, links, repro)
  ## Risks and unknowns
  ## Open questions for next stage
  ## Recommended approach for the planner
builtin: false                              # set true for shipped agents; read-only in UI
```

**Storage.** YAML files at `~/.local/share/agentctl/agents/<name>.yaml`.
Built-in agents ship in the install template and are copied to the
user's directory at `agentctl init` (same model as built-in skills). The
agentd in-memory index of agents is reloaded on file change (fsnotify),
on `agentctl agent reload`, and on agentd start.

**Built-in agents shipped in v0.**

| Name | Role | Skills allowed | MCPs allowed | Approval required |
|---|---|---|---|---|
| `bug-investigator` | Reads issue, explores repo, gathers context, asks clarifying questions. | `filesystem-nav`, `git-archeology` | `github`, `filesystem` | yes |
| `bug-planner` | Reads investigator's handoff, proposes a fix plan with evidence and a test plan, refines via chat. | `code-review`, `test-design` | `github` | yes |
| `bug-executor` | Reads planner's handoff, makes code changes, runs tests, opens a PR. | `git-commit`, `test-runner` | `github`, `filesystem` | no (PR is the gate) |

Each ships with a strong handoff template; see `builtin-skills/agents/`
for the seed content.

**CLI surface.**

| Command | Behavior |
|---|---|
| `agentctl agent list` | Tabular list of agents (name, description, builtin/custom, model). |
| `agentctl agent show <name>` | Print the agent's YAML. |
| `agentctl agent add <name> --from <path>` | Add a new custom agent from a YAML file. |
| `agentctl agent edit <name>` | Open the agent's YAML in `$EDITOR`. Built-ins refuse without `--fork-to <new-name>`. |
| `agentctl agent remove <name>` | Delete a custom agent. Refuses if any workflow references it. |
| `agentctl agent fork <builtin> <new-name>` | Copy a built-in's YAML to a new custom agent for editing. |
| `agentctl agent reload` | Re-read all agent YAMLs from disk. |

**Web UI surface — Agents tab** (new, sibling to Skills and MCPs in the
nav). Two-pane layout:

- **Left pane**: searchable list of agents. Each row shows colour swatch,
  avatar, name, "builtin" badge if applicable. "+ New Agent" button at
  top.
- **Right pane** when an agent is selected, or when "+ New Agent" is
  clicked: edit form with the following fields, top to bottom:
  - Name (text)
  - Description (text, one line)
  - Avatar (emoji picker or image upload)
  - Colour (colour picker, hex)
  - Model (dropdown: Opus / Sonnet / Haiku / inherit default)
  - Role: tab switcher [Free-text prompt | Use a skill]. Free-text =
    Markdown textarea. Use-a-skill = dropdown picker of skills.
  - Skills allowed (multi-select chip picker; empty = all)
  - MCPs allowed (multi-select chip picker; empty = all)
  - Handoff template (Markdown editor with live preview pane on hover)
  - Save / Cancel / "View YAML" disclosure showing the rendered YAML.
- Built-in agents are read-only in the UI; an "Edit a copy" button forks
  them to a custom agent.

**Acceptance criteria.**

- A new custom agent created via the UI is immediately usable in the
  Workflow composer.
- The same set of agents is visible from `agentctl agent list` and the
  Agents tab.
- Editing an agent's YAML on disk causes `agentctl agent list` and the
  Agents tab to reflect the change within 2s (fsnotify).
- Removing an agent referenced by a workflow fails with a clear error
  listing the referencing workflows.
- Built-in agents cannot be edited or removed in v0; forking is the only
  modification path.

**Error and edge cases.**

- Duplicate name on add → reject with clear error.
- YAML missing `name`, `role`, or `handoff_template` → invalid; reject with
  field-level diagnostics.
- `skills_allowed` references a non-existent skill → warn at load, treat
  as no-op at session start.
- `mcps_allowed` references a non-existent MCP → warn at load, skip at
  session start (matches v1 R3 behavior).

**Dependencies.** Existing skills system (R3, R9 in v1), MCP registry
(R5 in v1).

**Out of scope (this requirement).** Agent versioning, agent
import/export to git, multi-tenant agent libraries.

---

### R-W2. Workflow as an ordered chain of agents

**Goal.** A developer composes a workflow by chaining named agents in
order, with per-step approval gates and per-step notes. The workflow is
the template; tickets are its runs.

**Workflow schema** (`~/.local/share/agentctl/workflows/<name>.yaml`):

```yaml
name: bug                                   # required, unique slug
description: Bug fix workflow
tag: bug                                    # optional; used to auto-suggest
                                            # when a ticket carries this tag
stages:                                     # required; ordered list, ≥1 entry
  - agent: bug-investigator                 # required; references an agent by name
    approval_required: true                 # required; gate before advancing
    notes: ""                               # optional; appended to agent's role
                                            # prompt for THIS workflow only
  - agent: bug-planner
    approval_required: true
    notes: |
      Prefer minimal-blast-radius fixes. Surface risks explicitly.
      Always include a test plan.
  - agent: bug-executor
    approval_required: false
builtin: false
```

**Storage.** YAML files at `~/.local/share/agentctl/workflows/<name>.yaml`.
Built-in workflows ship via the install template, copied at
`agentctl init`. Reload on fsnotify, `agentctl workflow reload`, or
agentd start.

**v0 constraint: linear chains only.** Each workflow is a sequence of
stages, each consuming the previous stage's handoff and producing its own.
Branching, conditional, parallel, or DAG-shaped workflows are out of scope
(§11).

**CLI surface.**

| Command | Behavior |
|---|---|
| `agentctl workflow list` | Tabular list (name, description, stage count, builtin/custom). |
| `agentctl workflow show <name>` | Print the workflow YAML. |
| `agentctl workflow add <name> --from <path>` | Add a new custom workflow from a YAML file. |
| `agentctl workflow edit <name>` | Open the workflow YAML in `$EDITOR`. |
| `agentctl workflow remove <name>` | Delete a custom workflow. Refuses if any running ticket references it. |
| `agentctl workflow validate <name>` | Lint: check all referenced agents exist, no cycles, ≥1 stage. |
| `agentctl workflow reload` | Re-read all workflow YAMLs from disk. |

**Web UI surface — Workflows tab** (new, sibling to Agents/Skills/MCPs).
Two-pane layout:

- **Left pane**: list of workflows. "+ New Workflow" button.
- **Right pane** = composer when a workflow is selected or "+ New" is
  clicked:
  - Name, description, tag (text inputs).
  - **Stages**: vertically stacked ordered list. Each row contains:
    - drag handle (vertical grip; reorder via drag-and-drop)
    - agent picker (avatar + colour pill + name)
    - `approval_required` toggle
    - "notes" textarea (collapsible; expands inline)
    - trash icon
  - "+ Add stage" button below the list opens an agent picker modal
    showing cards of every available agent (avatar, colour, name,
    description, builtin badge).
  - Save / Cancel / "View YAML" disclosure.
  - **Validation banner** at top showing any errors (missing agents,
    empty stage list).

```
┌────────────────────────────────────────────────────────────┐
│ Workflows                                                  │
├──────────────┬─────────────────────────────────────────────┤
│ + New        │ Name:        [ bug                       ]  │
│              │ Description: [ Bug fix workflow          ]  │
│ ▌ bug        │ Tag:         [ bug                       ]  │
│   builtin    │                                             │
│              │ Stages                                      │
│ ▌ feature    │ ┌─────────────────────────────────────────┐ │
│   builtin    │ │ ⋮⋮ 🟦 bug-investigator                 │ │
│              │ │     [✓] approval required          🗑   │ │
│ ▌ my-spike   │ │     ▶ notes (empty)                    │ │
│              │ └─────────────────────────────────────────┘ │
│              │ ┌─────────────────────────────────────────┐ │
│              │ │ ⋮⋮ 🟪 bug-planner                      │ │
│              │ │     [✓] approval required          🗑   │ │
│              │ │     ▼ notes: Prefer minimal-blast-…    │ │
│              │ └─────────────────────────────────────────┘ │
│              │ ┌─────────────────────────────────────────┐ │
│              │ │ ⋮⋮ 🟩 bug-executor                     │ │
│              │ │     [ ] approval required          🗑   │ │
│              │ │     ▶ notes (empty)                    │ │
│              │ └─────────────────────────────────────────┘ │
│              │ [ + Add stage ]                             │
│              │                                             │
│              │ [ Save ] [ Cancel ]    ▶ View YAML          │
└──────────────┴─────────────────────────────────────────────┘
```

**Built-in workflows shipped in v0.**

| Name | Stages |
|---|---|
| `bug` | `bug-investigator` (approval) → `bug-planner` (approval) → `bug-executor` (no approval) |

Additional built-in workflows (feature, enhancement, spike) are explicitly
deferred until the bug vertical slice is exercised; see §11.

**Acceptance criteria.**

- A new custom workflow created in the composer is immediately startable
  as a ticket.
- Reordering stages via drag-and-drop preserves correctness; the YAML
  reflects the new order on save.
- Editing a workflow's YAML on disk is reflected in the Workflows tab
  within 2s.
- Removing a workflow with running tickets fails with a clear error
  listing the active ticket IDs.
- `agentctl workflow validate` rejects: a stage referencing a non-existent
  agent; an empty `stages` list; a duplicate `name`.

**Error and edge cases.**

- Workflow YAML references an agent that gets removed → ticket starts
  for this workflow fail at validation time with a clear error pointing
  at the broken stage; existing in-flight tickets continue (they
  snapshot the workflow at start; see §10).
- Per-stage `notes` longer than 8 KB → reject with a clear error.

**Dependencies.** R-W1 (Agents).

**Out of scope (this requirement).** Branching, conditionals, parallel
stages, sub-workflows, workflow versioning, workflow templates from a
shared library.

---

### R-W3. Ticket lifecycle

**Goal.** A developer creates a ticket by selecting a workflow and
pointing it at an issue or freeform task description plus a repo. The
ticket runs its workflow's stages in order, exposes a chat thread, and
terminates when the workflow completes or the user stops it.

**Ticket creation.** Inputs:

- **Source** (one of):
  - GitHub issue URL (e.g., `https://github.com/owner/repo/issues/123`).
    agentd fetches the issue title + body via GitHub MCP at start; stores
    them on the ticket row and writes them to
    `/workspace/.agentctl/ticket/issue.md` for the first stage to read.
  - Freeform task — title + body typed into a form. No external source.
- **Repo URL** — same `--repo` semantics as v1 R2. The workspace clones
  this repo on a new branch (see Workspace below).
- **Workflow** — name of an existing workflow.
- **Name** (optional) — display label; defaults to the issue title or
  first 60 chars of the freeform task.

**Status lifecycle.**

```
              ┌─────────┐
              │ pending │  ticket created, workspace materializing
              └────┬────┘
                   │ workspace ready, stage 1 spawned
                   ▼
              ┌─────────┐
       ┌──────│ running │──────┐
       │      └────┬────┘      │
       │           │           │
       │           │ all stages complete
       │           ▼           │
       │     ┌─────────┐       │
       │     │  done   │       │
       │     └─────────┘       │
       │                       │
       │ user "Stop ticket"    │ unrecoverable error
       ▼                       ▼
   ┌─────────┐            ┌─────────┐
   │ stopped │            │ failed  │
   └─────────┘            └─────────┘
```

A ticket is in `running` for the entire span where at least one stage is
not yet `done`. The currently-active stage's status is what the UI
displays as the ticket's "current phase."

**Workspace materialization.** On ticket creation:

1. agentd allocates a Docker volume `agentctl-ticket-<ticket_id>` (or a
   bind-mount under `~/.local/share/agentctl/tickets/<ticket_id>/`).
2. Clones the repo on a new branch `claude/ticket-<ticket_id>` based on
   the repo's default branch HEAD at clone time. The base SHA is recorded
   on the ticket row.
3. Writes the resolved task description to
   `/workspace/.agentctl/ticket/issue.md`.
4. All stages of this ticket bind the same volume into their session
   container. Code changes made by an earlier stage are visible to
   later stages.

**Workspace teardown.** On ticket termination (`done`, `stopped`, or
`failed`):

- Stage sessions are stopped.
- The branch is preserved (the user may have pushed it; even if not,
  destroying it is destructive and should not be automatic in v0).
- The Docker volume persists for `ticket.workspace_ttl_days` (default 7
  days), then is pruned by a daily agentd janitor pass.

**CLI surface.**

| Command | Behavior |
|---|---|
| `agentctl ticket start --workflow <name> --repo <url> [--issue <gh-url> \| --task <title>] [--name <name>]` | Create a ticket; attach to its chat stream. |
| `agentctl ticket ls` | List tickets with status, workflow, current stage, last activity, cost. |
| `agentctl ticket show <id>` | Detailed status: stage timeline, current handoff message, PR URL if any. |
| `agentctl ticket attach <id>` | Attach the terminal to the ticket's chat thread. |
| `agentctl ticket handoff <id>` | Trigger handoff on the current stage (CLI equivalent of the UI button). |
| `agentctl ticket handback <id> [--question "..."]` | Trigger backtrack on the current stage to the previous one, optionally with a question to seed the previous stage. |
| `agentctl ticket approve <id>` | Approve the current stage's handoff and advance to the next stage. |
| `agentctl ticket stop <id>` | Terminate the ticket (workspace retained per TTL). |
| `agentctl ticket logs <id>` | Tail agentd-side logs for this ticket. |

**Acceptance criteria.**

- `agentctl ticket start` returns within the cold-start budget (§4) with
  stage 1 active and the chat thread attached.
- A ticket created in the UI appears in `agentctl ticket ls` immediately
  and vice versa (mirrors v1 R4 parity).
- `issue.md` is present in the workspace at stage 1 start when an
  `--issue` is given.
- The workspace branch exists in the cloned repo and is named
  `claude/ticket-<ticket_id>`.
- Terminating a ticket stops its current stage's session within 5s.
- Diff and Cost views on the ticket page aggregate across all stages of
  the ticket (re-using v1 R8/R10 machinery scoped to the workspace).

**Error and edge cases.**

- Invalid GitHub issue URL or unreachable issue → ticket creation fails
  with a clear error before workspace allocation.
- Repo clone fails → ticket transitions to `failed`; workspace is
  cleaned up; the agentd error is surfaced.
- The user destroys the branch out-of-band → diff/cost views still work
  (volume snapshot), but execute-stage PR creation fails with a clear
  error.
- Two tickets target the same repo → each gets its own workspace volume
  and its own branch; no cross-contamination.

**Dependencies.** R-W2 (Workflows), R-W4 (Stage execution), R2 (v1 session
provisioning), R8 (v1 diff view), R10 (v1 cost).

**Out of scope (this requirement).** Webhook-triggered ticket creation,
label-triggered workflow selection, bulk ticket operations, ticket
templating.

---

### R-W4. Stage execution model

**Goal.** Each stage of a running ticket is backed by exactly one
session. Stages run sequentially; only one stage is active at a time per
ticket. Stage state transitions are deterministic and durable.

**Stage status lifecycle.**

```
   ┌─────────┐
   │ pending │  ticket created or prior stage approved
   └────┬────┘
        │ agentd spawns session
        ▼
   ┌─────────┐
   │ active  │◀──────────────────────────────────┐
   └────┬────┘                                   │
        │                                        │ user pushes back
        │ user clicks "Hand off"                 │ in chat → new
        ▼                                        │ synthesis
   ┌──────────────────┐                          │
   │ awaiting-approval │──────────────────────────┘
   └────┬─────────────┘
        │ user clicks "Approve & advance"
        ▼
   ┌─────────┐
   │  done   │
   └─────────┘

   From any state, "Hand back" from a later stage triggers:
   ┌──────────────────────┐
   │ paused-for-backtrack │
   └──────────────────────┘
   (this stage's session is stopped; the prior stage's status
    is restored from `done` to `active` and a new session is spawned.)
```

**Stage-to-session mapping.**

- On stage transition to `active`, agentd:
  1. Reads the agent YAML for the stage's referenced agent.
  2. Spawns a new agentctl session against the ticket's workspace volume,
     with the agent's role/skills/MCPs/model applied. The per-stage
     `notes` from the workflow YAML are appended to the agent's `role`
     prompt at session start.
  3. Records the session's `session_id` on the stage row.
  4. Seeds the session with its first user-message:
     - **Stage 1**: contents of `/workspace/.agentctl/ticket/issue.md`
       (or the freeform task body), prefixed with a short standard
       instruction frame *(e.g., "A new ticket has been opened. Read
       and begin per your role.")*.
     - **Stage N > 1**: the prior stage's locked-in handoff message,
       wrapped in: *"You are receiving handoff from `<prev_agent>`.
       Here is their synthesis: ```<handoff>``` Per your role, begin
       work."*
  5. UI: appends a stage seam to the thread; renders the seeded user
     message; streams the agent's first reply.

- On stage transition to `done`, agentd:
  1. Locks the latest agent reply in the chat thread as `handoff_message`
     on the stage row.
  2. Stops the session (container down, volume preserved). v0 chooses
     **stop-and-respawn** over pause-and-resume for simplicity; see §10.
  3. Transitions the next stage to `active` (see above), or transitions
     the ticket to `done` if this was the last stage.

**Per-stage `notes` injection.** Workflow-defined `notes` for a stage are
appended to the agent's `role.prompt` (or to the contents of the
referenced skill's SKILL.md) at session start. The combined prompt is the
session's system prompt. This is the only place per-workflow customization
of agent behavior is supported in v0.

**One stage active at a time invariant.** v0 enforces strict
serialization within a ticket. The UI composer is disabled and a non-active
stage's session does not exist. The current stage is recorded in the
`tickets.current_stage_id` column.

**Cost and diff scope.** A ticket's aggregate cost is the sum of its
stages' session costs (mirrors v1 R10 per session). The diff view is
sourced from the ticket's workspace volume, not per session, so it
reflects all stages' cumulative changes.

**Acceptance criteria.**

- At any moment a `running` ticket has exactly one stage with status
  `active` or `awaiting-approval`.
- Stage transitions are durable across agentd restart: if agentd
  crashes mid-handoff, on restart the stage resumes the correct status
  based on the persisted message log.
- A stage's session is reachable via `agentctl attach <session_id>` for
  debugging (raw session console), even though the primary UI path is
  the ticket thread.
- Cost on the ticket page equals the sum of cost on each backing
  session.

**Error and edge cases.**

- Stage session crashes mid-stream → agentd marks the stage `failed`
  and the ticket `failed`. (v0 has no automatic stage retry; user can
  start a new ticket against the same issue.)
- User attempts to message a `done` or `paused-for-backtrack` stage →
  composer is read-only with a tooltip explaining why.
- The agent does not respond to the handoff auto-prompt within a
  configurable timeout (default 5 min) → stage stays `active`; UI shows
  a "still working" indicator; no automatic abort.

**Dependencies.** R-W1 (Agents), R-W3 (Ticket lifecycle), v1 R2/R3
(session provisioning + pre-loaded environment), v1 R6 (continuity).

**Out of scope (this requirement).** Parallel stages, sub-stages, retry
policies, live MCP toggling within a stage.

---

### R-W5. Handoff mechanism (chat-message-as-artifact)

**Goal.** The output of a stage is a single chat message — the agent's
distilled synthesis — produced on demand by the agent in response to a
user-triggered auto-prompt, structured per the agent's `handoff_template`.
That message is both the visible end of the stage and the seed input
to the next stage. No separate artifact files, no document editor, no
filesystem convention.

**Trigger.** The user clicks "Hand off to <next-agent>" on the composer
of an `active` stage (or invokes `agentctl ticket handoff <id>`).

**Auto-prompt.** agentd injects the following user-role message into the
session and then drives a single agent turn:

```
Produce your handoff message now using this exact structure:

<handoff_template>

Fill in each section based on what you have done in this stage. Each
section must be substantive — concrete evidence (file:line refs, logs,
links, repro steps), not gloss. If a section truly does not apply,
say so explicitly and why.
```

The agent's reply is sent down the chat stream normally; the UI renders
it like any other agent message but visually marks it as the *candidate
synthesis* (see UI Refinement loop below). Stage status transitions to
`awaiting-approval`.

**Refinement loop.** While `awaiting-approval`:

- The composer remains active.
- The user can push back in chat: *"section 3 is thin, add the repro from
  the Slack thread"* — the agent responds normally and emits a revised
  message. **Each agent reply during the awaiting-approval state replaces
  the candidate synthesis pointer.** The most recent agent reply is
  always the candidate.
- The user can re-trigger the auto-prompt explicitly by clicking "Hand
  off" again — agentd resends the same auto-prompt, getting a fresh
  synthesis.
- There is no document editor. The user reshapes the synthesis only by
  talking.

**Approval.** The user clicks "Approve & continue" (or invokes
`agentctl ticket approve <id>`). agentd:

1. Locks the candidate synthesis as the stage's `handoff_message` on the
   stage row (durable).
2. Transitions the stage to `done`.
3. Triggers Stage N+1 (per R-W4).

For stages with `approval_required: false` (e.g., the executor in the
bug workflow), the "Approve & continue" button is replaced by an
immediate transition: as soon as the agent emits the synthesis, the
stage advances. The user can still cancel the ticket if they disagree.

**UI affordances.**

- The candidate synthesis message in the thread has a subtle border
  highlight and a footer chip reading *"Awaiting approval — refine in
  chat or approve to continue."*
- The composer's right side replaces the "Hand off" button (visible
  during `active`) with two buttons during `awaiting-approval`:
  *[Approve & continue ▸]* and *[Refine in chat]* (the latter just
  closes the awaiting state and returns to `active` without a new
  auto-prompt — for the case where the user already typed pushback in
  the composer before clicking).

**Concrete handoff template — `bug-investigator` (v0).**

```markdown
## Problem statement
A 2–4 sentence restatement of the bug as you understand it after
investigation.

## Findings
Bulleted summary of what you discovered. Cause hypothesis if you have
one; "still unknown" if you do not.

## Evidence
File-line references (`path/to/file.ts:120-145`), log excerpts,
GitHub issue/PR links, repro steps. Concrete, no hand-waving.

## Risks and unknowns
What could be wrong about the above? What did you not investigate?

## Open questions for next stage
Specific questions the planner should answer or the user should
clarify before planning.

## Recommended approach for the planner
A 2–5 sentence pointer at how a planner should think about this.
Not the plan itself.
```

**Concrete handoff template — `bug-planner` (v0).**

```markdown
## Approach
The chosen direction, in 2–4 sentences.

## Files to touch
Bulleted list of paths with one-line rationale each.

## Plan
Numbered, ordered steps the executor will follow.

## Test plan
Bulleted list of unit/integration/manual tests to run, and what each
verifies.

## Risks
Anything that could break, regress, or surprise.

## Out of scope for this PR
Adjacent issues we will not fix in this ticket.
```

**Concrete handoff template — `bug-executor` (v0).**

```markdown
## Summary
1–2 sentences on what was changed.

## Commits
List of commit SHAs and one-line subjects.

## PR
The PR URL. If the PR could not be created, a clear reason and the
branch name the user can push manually.

## Tests
Result of running the planned tests (pass/fail per test, with
output).

## Follow-ups
Anything notable that came up but is left for after merge.
```

**Acceptance criteria.**

- Clicking "Hand off" causes a single agent turn that emits a Markdown
  message matching the agent's `handoff_template` structure.
- The candidate synthesis is visibly distinguished in the thread.
- Pushing back in chat causes a new candidate synthesis to be emitted;
  the prior candidate is no longer the active candidate (but remains
  visible in the thread as message history).
- "Approve & continue" locks the candidate as `handoff_message` and
  starts the next stage within the stage-transition budget (§4).
- The next stage's first user-message contains the prior stage's
  handoff message verbatim, wrapped in the standard frame.

**Error and edge cases.**

- The agent's reply to the auto-prompt is empty or malformed (does not
  match the template structure) → stage stays `awaiting-approval`; the
  candidate is still recorded; the user can push back to ask for
  reformat or re-trigger handoff.
- The user clicks "Approve" before any candidate synthesis exists (no
  agent reply yet after handoff trigger) → button is disabled; tooltip
  explains why.

**Dependencies.** R-W4 (Stage execution), R-W1 (Agents — handoff_template
comes from agent YAML), v1 R6 (continuity).

**Out of scope (this requirement).** Structured (JSON) artifacts,
multi-message handoffs, agent-callable `submit_artifact` tool,
filesystem-as-API conventions, per-workflow handoff template overrides.

---

### R-W6. Backtracking (Hand back)

**Goal.** While inside a later stage, the user can re-enter the
immediately preceding stage to clarify, correct, or expand its handoff
message — without losing the current stage's work in progress.

**Trigger.** User clicks "◂ Hand back" on the composer of an `active` or
`awaiting-approval` stage (stage N), optionally typing a question first.
CLI equivalent: `agentctl ticket handback <id> [--question "..."]`.

**Backtrack mechanics.** agentd:

1. Marks stage N as `paused-for-backtrack`. Stops stage N's session
   (container down, volume preserved). The chat thread retains all stage-N
   messages.
2. Restores stage N-1's status from `done` to `active`. Spawns a fresh
   session against the same workspace, applying the same agent's
   config. (v0 chooses stop-and-respawn over pause-and-resume; see §10.)
3. Seeds stage N-1's restored session with the prior `handoff_message`
   (so it has its own context) plus the new user message:
   ```
   Stage N (<next_agent>) needs more from you:

   <question>

   Update your understanding and produce a revised handoff when ready.
   ```
   If no question is provided, the seed is generic: *"Stage N is asking
   you to revisit. Please re-examine and revise."*
4. UI: appends a backwards seam to the thread (`─── handed back to
   Investigator ───`), shifts the role badge and composer focus back to
   stage N-1's agent.

**Resume forward.** When the user is satisfied with the re-handoff from
stage N-1 (i.e., clicks "Hand off to <stage N's agent>" again), agentd:

1. Locks the new candidate as stage N-1's revised `handoff_message`.
2. Restores stage N from `paused-for-backtrack` by spawning a fresh
   session and **seeding it with the new handoff** (stage N does not
   resume its prior session; v0 starts fresh).
3. UI: appends a forward seam again; the chat thread now visibly shows
   forward → backward → forward seams.

**Multi-step backtrack.** v0 supports backtracking only one stage at a
time. From stage 3, the user can hand back to stage 2; from stage 2
they can then hand back further to stage 1. The intermediate stages
move through `paused-for-backtrack`. Re-advancing replays the same
process in reverse.

**Chat thread visual model.** Seams are bidirectional markers; the
thread retains all messages from all stage entries. Re-entries into a
prior stage append new messages below the prior stage's old messages
(the thread is purely chronological — it does *not* try to merge or
collapse stage segments). Stage rail at the top of the page shows the
current stage; status dots reflect current statuses; clicking a rail
pill jump-scrolls to the **latest** seam for that stage.

**Acceptance criteria.**

- "Hand back" from stage N transitions stage N to
  `paused-for-backtrack` and stage N-1 to `active` within the
  backtrack-latency budget (§4).
- The chat thread shows a clear backward seam.
- The re-entered stage N-1's session sees the original `handoff_message`
  plus the new question as its seed user-messages.
- "Hand off" from a re-entered stage transitions back forward and seeds
  a fresh stage N session with the revised handoff.
- The stage rail at the page top reflects status changes in real time.

**Error and edge cases.**

- User clicks "Hand back" from stage 1 → button is disabled (no prior
  stage); tooltip says so.
- Multiple Hand-back/Hand-off cycles between the same two stages → each
  cycle appends fresh seams; the chat thread shows all of them in
  order.
- agentd crash mid-backtrack → on restart, status transitions resume from
  the durable stage rows; the chat thread is intact.

**Dependencies.** R-W4 (Stage execution), R-W5 (Handoff).

**Out of scope (this requirement).** Backtracking N stages in a single
click; cross-stage agent-to-agent dialogue (the planner directly
chatting with the investigator without user involvement); preserving the
old stage-N session for fast resume (v0 stops-and-respawns).

---

### R-W7. Web UI — Agents page

See R-W1 for the full Agents page spec. This requirement enumerates
acceptance criteria specifically for the UI surface.

**Page nav.** The Agents tab is a top-level sibling of Skills, MCPs, and
the existing Sessions surface. It appears in the left-rail nav of the
Web UI.

**Acceptance criteria (UI-only).**

- The Agents page loads within 500ms on a developer machine with ≤50
  saved agents.
- Creating a custom agent that references a non-existent skill or MCP
  emits an inline validation warning at save time.
- Forking a built-in agent prefills the new agent's form with the
  built-in's YAML.
- The colour picker accepts any hex string; the avatar accepts emoji
  characters or a PNG ≤32 KB.
- Saving a valid agent persists it to disk and emits a UI event so any
  open Workflows composer refreshes its agent picker.

**Dependencies.** R-W1.

---

### R-W8. Web UI — Workflows page

See R-W2 for the full Workflows page spec. This requirement enumerates
acceptance criteria specifically for the UI surface.

**Page nav.** Workflows tab sits next to Agents.

**Acceptance criteria (UI-only).**

- The composer's drag-and-drop reordering is keyboard accessible (up/down
  arrows on a focused stage row).
- "+ Add stage" opens an agent picker that includes a search input and
  filters by builtin/custom.
- The "View YAML" disclosure shows live-updated YAML reflecting the
  current composer state.
- Saving a workflow with a stage referencing a non-existent agent fails
  with an inline error pointing at the broken stage row.

**Dependencies.** R-W2.

---

### R-W9. Web UI — Ticket page (chat thread + stage rail)

**Goal.** The Ticket page is where 99% of user interaction with a
running workflow happens. It presents the ticket as a single continuous
chat thread (vertical), with stage transitions marked by horizontal
seams and role-shifts visible via avatar+colour changes. A stage rail
at the top gives spatial overview of the workflow's progress. A right-
hand tab switcher exposes Diff and Cost views scoped to the whole
ticket.

**Top-level layout.**

```
┌──────────────────────────────────────────────────────────────┐
│ Ticket #42 · "Cart abandons on Safari iOS 17"     [ Stop ]   │
│ workflow: bug · status: running · current stage: Planner     │
├──────────────────────────────────────────────────────────────┤
│  ✅ Investigator ──── ◔ Planner ──── ○ Executor              │
│                       ↑ current                              │
├─────────────────────────────────────────────┬────────────────┤
│                                             │ ◉ Chat         │
│                                             │ ○ Diff (12)    │
│  ┃ issue.md (seeded)                        │ ○ Cost         │
│  ┃   Users on Safari iOS 17 lose cart…      │ ○ Timeline     │
│                                             │                │
│  ▌🟦 You                                    │                │
│  ▌   investigate                            │                │
│                                             │                │
│  ▌🟦 bug-investigator                       │                │
│  ▌   Looking at cart/session.ts and ITP…    │                │
│  ▌   [tool: read cart/session.ts:120-180]   │                │
│  ▌   Finding: SameSite=None cookie is…      │                │
│                                             │                │
│  ▌🟦 You                                    │                │
│  ▌   also check yesterday's order-flow log  │                │
│                                             │                │
│  ▌🟦 bug-investigator                       │                │
│  ▌   [tool: github.mcp.search …]            │                │
│  ▌   Synthesis (handoff):                   │                │
│  ▌     ## Problem statement …               │                │
│  ▌     ## Findings …                        │                │
│  ▌     ## Evidence …                        │                │
│                                             │                │
│  ─────── handed off to Planner ───────      │                │
│                                             │                │
│  ▌🟪 bug-planner                            │                │
│  ▌   Received handoff. Reading evidence…    │                │
│  ▌   Proposed plan:                         │                │
│  ▌     1. Fall back to localStorage when… │                │
│                                             │                │
│  ▌🟪 You                                    │                │
│  ▌   reduce blast radius — keep cookie path │                │
│                                             │                │
│  ▌🟪 bug-planner                            │                │
│  ▌   Revised plan: …                        │                │
│                                             │                │
├─────────────────────────────────────────────┴────────────────┤
│ talking to: 🟪 bug-planner                                   │
│ ┌──────────────────────────────────────────────────────┐     │
│ │ type a message…                                      │     │
│ └──────────────────────────────────────────────────────┘     │
│ [ Send ]  [ Hand off to Executor ▸ ]  [ ◂ Hand back ]        │
└──────────────────────────────────────────────────────────────┘
```

**Layout elements.**

- **Header row**: ticket name + ID, "Stop ticket" action, workflow name,
  ticket status, current stage badge.
- **Stage rail** (horizontal, immediately below the header): one pill per
  stage, in order, connected by lines. Each pill shows the agent's
  avatar + colour, agent name, and a status dot (`✅` done, `◔` active,
  `⏸` awaiting-approval, `○` pending, `↩` paused-for-backtrack).
  Current stage's pill is highlighted. Clicking a pill jump-scrolls the
  thread to the latest seam for that stage.
- **Center column**: the chat thread, vertically scrollable.
  - Each message has a left colour stripe matching its stage's colour.
  - Agent messages show the agent's avatar + name in the header.
  - Tool calls (Read, Bash, GitHub MCP, etc.) render with a compact
    inline rendering (re-using v1 R4's tool-call rendering).
  - The most recent candidate synthesis during `awaiting-approval` has
    a subtle border and a footer chip *"Awaiting approval — refine in
    chat or approve to continue."*
  - Seams are full-width horizontal rules with a center label
    (`─── handed off to Planner ───` or `─── handed back to
    Investigator ───`).
- **Right column** (~240 px wide): vertical tab switcher.
  - **Chat** (default): the chat thread itself.
  - **Diff**: cumulative diff of the ticket's workspace vs. the recorded
    base SHA. Reuses v1 R8's diff renderer.
  - **Cost**: aggregate cost across all stages. Reuses v1 R10's cost
    panel.
  - **Timeline**: a vertical timeline of stage transitions, handoff
    events, backtracks, and significant tool calls. Used for retrospect
    after a ticket is done.
- **Composer** (sticky at the bottom):
  - "talking to: <agent>" label above the input, with the agent's avatar
    + colour.
  - Multi-line message input.
  - Buttons (left to right): `[Send]`, `[Hand off to <next> ▸]`,
    `[◂ Hand back]`.
  - During `awaiting-approval`, `[Hand off]` is replaced by
    `[Approve & continue ▸]` and `[Refine in chat]`.
  - During `done`/`stopped`/`failed` ticket state, the composer is
    read-only with an explanatory banner.

**UX invariants.**

- **One composer, always pointed at the current stage's agent.** You
  cannot directly talk to a `done` or `paused-for-backtrack` stage
  (backtracking is the only path).
- **Vertical scroll history**, no horizontal stage columns. The chat
  thread is one continuous narrative.
- **Role identity via avatar + colour stripe**, repeated on every
  message in that stage. Long threads remain scannable.
- **Composer header always names the current agent** so the user is
  never confused mid-scroll.
- **Workflow-progression actions only on the composer.** No alternative
  paths via context menus, headers, or message hover affordances.

**Acceptance criteria.**

- A ticket page with ≤1000 messages renders in ≤100ms after route
  navigation.
- The stage rail reflects status changes within 200ms of agentd
  emitting them.
- Seams render with correct labels for handoff (forward) and handback
  (backward).
- The composer correctly switches between Hand-off and
  Approve-and-continue modes per the current stage status.
- Multiple browser tabs open on the same ticket stay synchronized
  (mirrors v1 R4).
- The Diff and Cost tabs scope to the entire ticket workspace, not a
  single stage.

**Error and edge cases.**

- Browser ↔ agentd WebSocket drops → reconnect and replay missed
  messages (v1 R6 mechanic).
- A second client clicks "Approve" microseconds before the first → the
  first wins; the second sees a graceful "stage already advanced"
  state with no error toast.
- The ticket transitions to `failed` mid-stream → an error banner
  appears at the top of the chat thread with the failure reason;
  composer becomes read-only.

**Dependencies.** R-W3, R-W4, R-W5, R-W6, v1 R4/R6/R8/R10.

**Out of scope (this requirement).** Mobile layout, dark/light theme
switch (inherits global), exporting the ticket thread as a transcript
file (v0.5), commenting on individual stage messages (v0.5).

---

### R-W10. CLI parity for tickets, workflows, and agents

**Goal.** Every operation reachable in the Web UI is reachable from
`agentctl`. This mirrors v1 R4: CLI and UI are peers.

**Command summary.** (Detailed semantics under R-W1, R-W2, R-W3.)

| Command group | Commands |
|---|---|
| `agentctl agent` | `list`, `show`, `add`, `edit`, `remove`, `fork`, `reload` |
| `agentctl workflow` | `list`, `show`, `add`, `edit`, `remove`, `validate`, `reload` |
| `agentctl ticket` | `start`, `ls`, `show`, `attach`, `handoff`, `handback`, `approve`, `stop`, `logs` |

**Stream attach.** `agentctl ticket attach <id>` connects the terminal to
the ticket's chat thread. The terminal renders:

- A header line with ticket name, workflow, current stage.
- Streaming messages as they arrive, prefixed by role badge and agent
  name with simple ANSI colouring keyed off the agent's `colour` field.
- Seams as full-width horizontal rules with the transition label.
- The input prompt shows the current agent: `[bug-planner] >`.
- Slash-style controls in the terminal:
  - `/handoff` — trigger handoff (equivalent to UI button).
  - `/handback [question]` — trigger backtrack.
  - `/approve` — approve the awaiting candidate synthesis.
  - `/stop` — stop the ticket.

**Acceptance criteria.**

- All UI workflow-progression actions have equivalent CLI commands and
  slash-controls.
- Output of `agentctl ticket ls` and the Tickets page list the same set
  of tickets with the same status at any moment.
- `agentctl ticket show <id>` includes the locked-in handoff messages
  for done stages, the current stage's candidate synthesis if any, and
  the workspace branch + base SHA.

**Dependencies.** R-W1 through R-W6.

---

### R-W11. Data model

**Goal.** Define the new sqlite tables and on-disk YAML layout
introduced by this feature, sufficient for an architect to design
schema migrations.

**New sqlite tables** (added to `agentd.db`):

`tickets`:

| Column | Type | Notes |
|---|---|---|
| `ticket_id` | text, PK | UUID. |
| `name` | text | Display label. |
| `workflow_snapshot_json` | text | Full workflow YAML serialized at ticket start. Pins this ticket to that workflow definition; subsequent edits to the workflow do not affect this ticket. |
| `repo_url` | text | Source repo. |
| `branch` | text | `claude/ticket-<ticket_id>`. |
| `base_sha` | text | Commit SHA at clone time. |
| `volume_id` | text | Docker volume / bind-mount path. |
| `source_kind` | text | `github_issue` \| `freeform`. |
| `source_url` | text, optional | GitHub issue URL if `source_kind = github_issue`. |
| `current_stage_id` | text, FK → stages.stage_id | The currently active or awaiting-approval stage. |
| `status` | text | `pending` \| `running` \| `done` \| `stopped` \| `failed`. |
| `created_at`, `started_at`, `ended_at` | timestamps | |

`stages`:

| Column | Type | Notes |
|---|---|---|
| `stage_id` | text, PK | UUID. |
| `ticket_id` | text, FK → tickets | |
| `position` | int | 1-indexed position in the workflow's stage list. |
| `agent_name` | text | The referenced agent at start time. |
| `notes` | text | Per-workflow per-stage notes from workflow YAML. |
| `approval_required` | bool | |
| `session_id` | text, FK → sessions, nullable | The session currently backing this stage (null if `pending` or after termination). |
| `handoff_message` | text, nullable | Locked-in synthesis once `done`. |
| `status` | text | `pending` \| `active` \| `awaiting-approval` \| `done` \| `paused-for-backtrack` \| `failed`. |
| `started_at`, `ended_at` | timestamps, nullable | |

`stage_events` (append-only audit log; powers the Timeline tab):

| Column | Type | Notes |
|---|---|---|
| `event_id` | text, PK | |
| `stage_id` | text, FK → stages | |
| `kind` | text | `stage_started` \| `handoff_triggered` \| `synthesis_emitted` \| `approved` \| `handback_triggered` \| `resumed` \| `failed`. |
| `payload_json` | text, optional | Kind-specific structured data. |
| `occurred_at` | timestamp | |

**On-disk YAML layout.**

```
~/.local/share/agentctl/
├── agents/
│   ├── bug-investigator.yaml          # built-in
│   ├── bug-planner.yaml               # built-in
│   ├── bug-executor.yaml              # built-in
│   └── my-custom-agent.yaml           # user-authored
├── workflows/
│   ├── bug.yaml                       # built-in
│   └── my-spike.yaml                  # user-authored
└── tickets/                           # per-ticket workspace bind-mounts
    └── <ticket_id>/
        ├── repo/                      # cloned repo on workspace branch
        └── .agentctl/
            └── ticket/
                ├── issue.md           # seeded at start
                └── meta.json          # ticket_id, workflow snapshot ref, etc.
```

**Workspace volume contents.** Stages bind-mount the per-ticket workspace at
`/workspace/` inside their session container. The convention:

- `/workspace/repo/` — the cloned repo on the ticket's branch.
- `/workspace/.agentctl/ticket/issue.md` — seeded ticket source.
- `/workspace/.agentctl/ticket/meta.json` — small metadata read-only file.

Agents have ordinary filesystem access throughout the workspace (subject
to their `mcps_allowed` and `skills_allowed`); they may write files (e.g.,
the executor commits code). Such files are *not* workflow handoff
artifacts — they are side effects living in git, surfaced via the Diff
tab.

**Acceptance criteria.**

- A complete ticket end-to-end produces durable rows in `tickets`,
  `stages` (one per stage), and `stage_events` (an audit trail).
- Workflow snapshot stored at ticket creation isolates the ticket from
  subsequent workflow edits.
- Backtracks produce additional `stage_events` rows (e.g., one
  `handback_triggered` and one `resumed` per backtrack cycle).
- Stage rows persist correctly across agentd restart.

**Dependencies.** v1 sqlite schema, v1 sessions table.

---

### R-W12. Ticket source: GitHub issue (v0)

**Goal.** In v0 a ticket can be created from a GitHub issue URL; agentd
fetches the issue text at start and seeds the workspace with it. Webhook /
label-triggered creation is deferred (v0.5).

**Behavior on `--issue <gh-url>`.**

1. Parse the URL into `owner/repo#number`.
2. Issue a GitHub MCP fetch of the issue title + body + top-level
   comments (no nested discussions in v0).
3. Concatenate into `/workspace/.agentctl/ticket/issue.md` with a small
   frontmatter block (`source_url`, `fetched_at`).
4. Record `source_kind = github_issue` and `source_url` on the ticket
   row.

**Behavior on `--task "<title>"`.**

1. The user is prompted for a body in `$EDITOR` (CLI) or a textarea (UI).
2. Title + body are written to `/workspace/.agentctl/ticket/issue.md`
   with `source_kind = freeform`.

**No automatic GitHub write-back in v0.** The ticket does not post
comments back to the source issue, does not change labels, does not
update assignees. The executor stage *does* open a PR (via GitHub MCP)
when its workflow includes one — but PR creation is a side effect of
the executor agent's behavior, not a workflow-runtime feature.

**Acceptance criteria.**

- `issue.md` is present at stage 1 start and contains the issue title
  + body.
- A 404 or 401 from the GitHub MCP fails ticket creation with a clear
  error before workspace allocation.
- The freeform path produces the same `issue.md` shape from a manually
  typed title+body.

**Dependencies.** v1 GitHub MCP (R5).

**Out of scope (this requirement).** Webhook-driven ticket creation,
label-based workflow auto-selection, syncing ticket status back to the
GitHub issue.

---

## 10. Open product questions — for the architect to resolve

These need decisions during the technical-architecture pass. Each has a
recommended starting point but is not yet pinned down.

### 10.1. Session lifecycle on stage transition: stop-and-respawn vs pause-and-resume

**Question.** When a stage completes (or is paused for backtrack), do we
stop the underlying container and respawn a fresh one on the next entry,
or pause-and-resume the original container?

**Recommendation (v0).** Stop-and-respawn. Simpler, matches v1's
session-stop semantics, no new "paused container" lifecycle state. Cost:
~5–10s latency on backtrack as a fresh container starts. Acceptable for
v0.

**Why deferred.** Pause-and-resume needs (a) Docker container pause as a
real lifecycle state in agentd (out of scope per v1's §16), (b) re-attach
plumbing for the runtime shim, (c) careful handling of skill snapshot
drift if skills changed between pause and resume. The latency win is
small and a v0.5 optimization.

### 10.2. Workflow snapshot vs. live-follow

**Question.** When a workflow YAML is edited while tickets running that
workflow are in progress, should running tickets pick up the new
definition for not-yet-started stages?

**Recommendation (v0).** Snapshot at ticket start. The full workflow
YAML is serialized into `tickets.workflow_snapshot_json` and the ticket
follows that snapshot to completion. Live-follow is confusing (changing
the planner mid-flight mid-ticket) and creates surprising failure modes.

**Why deferred from a fuller story.** A v0.5 might offer "apply latest
workflow definition" as an explicit user-triggered action on a running
ticket, with a clear diff preview.

### 10.3. Synthesis as Markdown vs. structured (JSON)

**Question.** Handoff messages are free-form Markdown in v0. Should
later versions support structured (JSON) artifacts to enable
machine-readable handoff (e.g., the planner emits a structured
"files-to-touch + steps" object the executor parses)?

**Recommendation (v0).** Markdown only. Agents read Markdown well; the
template-driven structure already gives consistent shape. Structured
artifacts add schema, validation, and editor surface.

**Why deferred.** Real workflows will reveal whether the LLM-vs-LLM
reliability of structured handoff is worth the complexity. Revisit in
v0.5 or v1.

### 10.4. Multi-step backtrack semantics

**Question.** When the user wants to backtrack two stages (e.g., from
executor all the way back to investigator), should there be a one-click
multi-step path?

**Recommendation (v0).** No. Single-step only — each backtrack moves
exactly one stage. Multi-step is composable (executor → planner → then
planner → investigator) at the cost of two seam pairs.

**Why deferred.** Multi-step needs UI for selecting a target stage, plus
semantics for whether intermediate stages should reset to `pending` or
remain `paused-for-backtrack`. Defer until users ask for it.

### 10.5. Cross-agent direct dialogue

**Question.** Can stage N's agent ask stage N-1's agent a question
directly, without user mediation, before producing its own handoff?

**Recommendation (v0).** No. All cross-agent communication is user-
mediated, via either forward handoff or user-triggered backtrack with a
question. This preserves the audit trail (every message visible in the
thread) and avoids the complexity of agent-spawned context flows.

**Why deferred.** Once the linear chain is solid, agent-to-agent
direct queries are a v1+ exploration with significant new failure modes
(infinite Q&A loops, runaway cost, context bloat) that need design.

### 10.6. Per-workflow per-agent role override

**Question.** v0 allows per-workflow per-stage `notes` (appended to the
agent's role prompt). Should it also allow full role override (replace
the agent's prompt entirely for this workflow)?

**Recommendation (v0).** No. If a workflow needs a different role, fork
the agent. Notes-append covers the 80% case (qualification, emphasis).

**Why deferred.** Full override pulls workflow YAML into "almost defining
agents inline," blurring the clean separation. The fork-the-agent path
is simple and explicit.

### 10.7. Concurrent stages within a single ticket

**Question.** Should v0+ ever allow two stages to run in parallel within
one ticket (e.g., investigator + a separate security-reviewer working
concurrently)?

**Recommendation (v0).** No. Strict linear chain, one active stage.
Parallelism within a ticket is a DAG concern.

**Why deferred.** DAG workflows in general are out of scope (§11); when
they come, parallel-stage execution is part of that design.

### 10.8. Ticket from non-GitHub sources (Jira, Linear, etc.)

**Question.** Should v0 support ticket sources other than GitHub
issues and freeform tasks?

**Recommendation (v0).** No. GitHub issues + freeform only. Jira/Linear
are MCP-shaped extensions that can plug in via an "issue source" MCP
contract in v0.5 without changing the ticket runtime.

---

## 11. Out of scope for v0

The following are *not* targets for the v0 vertical slice. Each is a
plausible v0.5 or v1+ direction.

- **Webhook / label-triggered ticket creation.** v0 is manual-start only.
  v0.5 will add a GitHub webhook listener (`agentd` exposes a local
  webhook endpoint behind a tunnel or a polling worker) and an
  `agent:bug` label convention that auto-creates a ticket with a chosen
  workflow.
- **DAG / branching / conditional workflows.** v0 is linear-chain only.
  Branching ("if executor's tests fail, hand back to planner") and
  multi-output stages are deferred.
- **Parallel stages within a ticket.** One active stage at a time.
- **In-flight editing of a running workflow definition.** v0 snapshots
  the workflow at ticket start.
- **Agent versioning / changelogs.** Agents are mutable YAML; v0 has no
  history.
- **In-UI YAML editor for agents/workflows.** v0 ships form-based UIs
  for both; raw YAML edit is via `$EDITOR` through the CLI commands and
  the "View YAML" disclosure (read-only in UI).
- **Team-shared agent/workflow library.** Same hand-distribution story
  as v1 custom skills.
- **Multi-ticket batch operations** (`agentctl ticket stop --all`,
  workflow-level rollouts).
- **Cost limits / budgets / alerts per ticket.** Cost is visible; no
  enforcement.
- **Ticket templates** (preset combinations of workflow + repo +
  defaults).
- **Cross-stage agent-to-agent direct dialogue** without user mediation.
- **Multi-step backtracking** in a single click.
- **Structured (JSON) handoff artifacts.**
- **Sub-workflows** (a stage that itself runs a workflow).
- **Exporting the ticket transcript as a file.**
- **Commenting on individual stage messages** (per-message threading).
- **Mobile UI for the ticket page.**
- **Auto-syncing ticket status back to the source GitHub issue.**
- **Container pause/resume as a third lifecycle state** (v0 is
  stop-and-respawn).

---

## 12. Cross-references

- `requirements.md` — v1 base spec for sessions, MCPs, skills, CLI/UI
  parity, diff and cost views, and the data-model and architecture
  principles this feature builds on.
- `v2-requirements.md` — v2 candidates including network egress
  restrictions and live skill reload, which intersect with the multi-
  stage session model.
- `architecture/overview.md`, `architecture/data-model.md`,
  `architecture/api.md` — to be extended in the architecture pass.
- `architecture/decisions/` — new ADRs expected: workflow runtime,
  ticket workspace model, handoff message semantics, stage session
  lifecycle.
