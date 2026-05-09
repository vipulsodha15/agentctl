# agentctl — v1 Requirements

## Overview

`agentctl` is a local tool that lets a developer spin up isolated AI coding-agent sessions on their own machine. Each session runs in its own Docker container, pre-loaded with the team's skills and MCP integrations, and is reachable from both a CLI and a local Web UI.

## Components

- **`agentctl`** — the CLI client. What developers run from a terminal.
- **`agentd`** — a long-running daemon installed as a system service (systemd on Linux, launchd on macOS). Owns container lifecycle, session state, the MCP registry, the local DB (sqlite), and the local Web UI HTTP/WebSocket endpoint.
- **Session container** — a Docker container running the agent runtime, one per session, provisioned on demand from a pre-built base image.
- **Web UI** — a browser app served on localhost by `agentd`.

The CLI and Web UI are peer clients of `agentd`; they hold no session state.

---

## Requirements

### 1. One-command setup and session start

A developer can install and run `agentctl` with two commands:

- **`agentctl init`** — runs once per machine. Pulls the base session image, prompts the developer for their `ANTHROPIC_API_KEY` and GitHub Personal Access Token (PAT), stores them locally with restricted permissions (`~/.config/agentctl/`, mode 0600), and installs `agentd` as a system service so it starts on boot. Re-runnable to rotate tokens or repair the install.
- **`agentctl start`** — launches a new isolated session container via the running `agentd` and opens both the CLI and the local Web UI ready to use.

No manual Docker commands, no config-file editing, no per-session daemon management.

### 2. On-demand local session provisioning

Every `agentctl start` (whether from CLI or the Web UI's "New Session" button) creates a fresh, isolated Docker container on the developer's machine within a few seconds. Each session is its own container, lifecycle-tied to that session.

The container is provisioned from a pre-built base image so startup is image-pull-free in steady state.

**Lifecycle ownership:** `agentd` owns all container start/stop/restart decisions. The container itself never self-terminates.

**Idle behavior:** after N minutes (default 15) with no activity, `agentd` stops the session's container to free RAM and CPU. Conversation state (history, working directory, repo clones, scratch files) lives on a host-managed volume that survives the stop. When the next message for that session arrives — via CLI reattach or the Web UI — `agentd` detects the container is stopped, restarts it, the volume re-mounts, and the session resumes. Cold resume target: same few-seconds budget as a fresh start.

Sessions are torn down for good (container removed, volume deleted) only on explicit end (`agentctl stop` or "End Session" in the UI).

### 3. Pre-loaded session environment

Every new session starts with everything a developer needs already wired up — no per-session setup.

The base image ships with:

- The agent runtime (Claude Code) installed
- The team's curated skills, slash commands, and agent definitions baked in
- Standard dev tooling (`git`, language runtimes, build tools as needed)

At session start, `agentd` injects per-session configuration into the container:

- `ANTHROPIC_API_KEY` from the host secrets store (set during `agentctl init`)
- The developer's GitHub PAT, used by both `git` (for clone/push) and the GitHub MCP server
- The list of internal MCP server URLs (reachable over the internal network, no auth required for non-GitHub MCPs)

The developer runs `agentctl start` and the session has its key, its GitHub access, and every MCP/skill ready to use immediately. No env files to edit, no MCPs to register by hand.

### 4. CLI and Web UI as equal clients

The CLI (`agentctl`) and the local Web UI are peer clients of `agentd`, not different products. Anything you can do in one, you can do in the other:

- Start a new session in the CLI → it appears in the Web UI's session list, and you can send/receive messages there
- Start a session in the Web UI → you can `agentctl attach <session>` from the terminal and continue
- Conversation history, current state, and live messages stream to both clients in real time; multiple clients can be attached to the same session simultaneously

**Streaming model:** the session container talks only to `agentd`. `agentd` is the fan-out point — it tracks which clients are attached to each session and streams events to all of them in parallel (CLI over a local Unix socket / HTTP+WebSocket, browser over a localhost HTTP+WebSocket served by `agentd`). Messages from any client go to `agentd`, which forwards them into the container. Clients hold no session state, so they stay in sync automatically.

### 5. MCP registry and per-session selection

**Registry (managed by `agentd`).** `agentd` keeps the list of available MCP servers in its local DB (sqlite). At `agentctl init` time, it seeds the registry with the team's known internal MCPs and the GitHub MCP. Each entry stores a name, URL, and any per-MCP config flags.

The developer can manage the registry from either client:

- **Web UI:** a "Settings → MCPs" view to add, edit, or remove MCP entries (fields: name, URL).
- **CLI:** `agentctl mcp list`, `agentctl mcp add <name> --url <url>`, `agentctl mcp remove <name>`.

**Per-session selection.** When starting a session:

- **Web UI:** the "New Session" screen shows the registry as checkboxes. All on by default; the developer can untick.
- **CLI:** `agentctl start --mcps github,jira,...` or `--no-mcp foo`. Default is all enabled.

Selection is **start-time only** — the chosen MCP set is fixed for the session's lifetime. To change it, the developer ends the session and starts a new one. Registry changes (adding/removing MCPs) take effect for newly started sessions only; running sessions are not affected. Two concurrent sessions on the same machine can have different MCP sets.

### 6. Conversation continuity

A session's full conversation history and working state persist across:

- multiple messages in the session (baseline)
- client disconnect/reconnect (close the terminal or browser tab, come back, history is intact)
- multiple clients attached at once (CLI + Web UI seeing the same thread, per Requirement 4)
- container idle-stop and restart (per Requirement 2 — state is restored from the host-mounted volume)
- `agentd` restarts and host reboots (since `agentd` is a system service per Requirement 1, it comes back automatically and re-discovers existing sessions from its DB and the host volumes)

Each session has:

- a host-mounted volume holding the agent's conversation history, working directory, repo clones, and scratch files
- a row in `agentd`'s DB with metadata (id, name, created-at, last-activity, MCP set, container ref, volume path)

State is destroyed only on explicit "End Session" (`agentctl stop` or UI button), which removes the container and deletes the volume.

### 7. Isolation between concurrent sessions

Multiple sessions can run on the same machine without interfering with each other. Each session gets:

- **Its own container** — separate filesystem, processes, and root context. Nothing a session does to its filesystem or installed packages is visible to any other session.
- **Its own host-mounted volume** (per Requirement 6) — sessions cannot read or write each other's volumes.
- **Its own injected secrets** — env vars are scoped to that container; one session cannot read another's `ANTHROPIC_API_KEY` or PAT.
- **Network isolation from peers** — sessions can reach the internal MCP network and the public internet (for the Anthropic API), but cannot reach other sessions' containers directly.
- **Resource limits** — `agentd` applies per-session CPU and memory caps (configurable, sensible defaults) so a runaway session can't starve the others.

`agentd` orchestrates all of this at container creation; the developer doesn't configure isolation manually.

### 8. Code change visibility and export

When the agent works on a cloned repo inside a session, the developer can see and extract the changes without entering the container.

**Visibility:**

- **Web UI:** a "Changes" view per session shows the live diff against the original branch — files added, modified, deleted, with side-by-side or unified diff rendering. Updates as the agent edits.
- **CLI:** `agentctl diff <session>` prints the same diff in the terminal.

**Export:**

- `agentctl export <session> --patch [path]` writes the diff to a `.patch` file on the host.
- `agentctl export <session> --push <branch>` pushes the working tree to a branch on the remote (using the session's GitHub PAT) so the developer can open a PR.
- Web UI exposes the same two actions as buttons.

Multiple repos in one session are handled — the views and exports list each cloned repo separately.

### 9. Explicit skill invocation by name

Skills baked into the image (per Requirement 3) are not only available for the agent to discover contextually — the developer can invoke any of them explicitly by name, ensuring the skill runs regardless of model judgment.

- **In both CLI and Web UI:** typing `/<skill-name>` in the message input invokes that skill directly. Same syntax in both clients.
- **Discoverability:** both clients support autocomplete on `/` — type `/` and a list of available skills appears with short descriptions, filtered as the developer types.
- `/help` (or equivalent) lists all available skills with descriptions.

The list of skills comes from what's baked into the session image, so all sessions see the same skill set unless the image is updated.

### 10. Per-session cost visibility

Every session tracks its own Anthropic API usage and cost. Developers can see what each session has spent and what they've spent in aggregate.

**Tracking:** `agentd` captures token usage (input, output, cache reads, cache writes) and the model used from the event stream of every agent response, persisting it per-session in its DB. Cost is computed from the current per-model pricing.

**Display:**

- **Web UI:** each session in the list shows a running total (e.g. "$0.42 — 12k in / 38k out"). A session detail view shows a breakdown by model and a turn-by-turn cost timeline. A top-level "Usage" page shows totals across all sessions with a date range filter.
- **CLI:** `agentctl ls` includes a cost column. `agentctl cost <session>` shows per-session detail. `agentctl cost --since 7d` shows aggregate.

Costs persist past session end (the DB row outlives the volume), so historical usage is queryable.
