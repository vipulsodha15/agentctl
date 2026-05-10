# agentctl — v1 Requirements

## 1. Overview

`agentctl` is a local tool that lets a developer spin up isolated AI coding-agent sessions on their own machine. Each session runs in its own Docker container, pre-loaded with the team's skills and MCP integrations, and is reachable from both a CLI and a local Web UI.

This document is the v1 functional and behavioral specification. It is the input to a separate technical-architecture pass.

**Architect: read §15 before starting.** It contains the open product questions that must be resolved as part of architecture. Each has a recommended starting point but is not yet decided. Items not pinned down by this document are either in §15 or in §16 (Out of scope for v1).

## 2. Components and glossary

- **`agentctl`** — the CLI client a developer runs from a terminal.
- **`agentd`** — a long-running daemon installed as a per-user system service (systemd `--user` on Linux, launchd user agent on macOS). Owns container lifecycle, session state, the MCP registry, the local DB, and the local Web UI HTTP/WebSocket endpoint.
- **Web UI** — a browser app served on `localhost` by `agentd`.
- **Session** — a logical conversation with the agent, identified by a stable `session_id`. Has exactly one container at any moment (running or stopped) and exactly one host-mounted volume.
- **Session container** — the Docker container running the agent runtime for a session, provisioned on demand from a pre-built base image.
- **Session volume** — a host-mounted Docker volume (or bind-mount under `~/.local/share/agentctl/sessions/<session_id>/`) holding the session's persistent state: conversation history, working directory, repo clones, scratch files.
- **Base image** — the pre-built Docker image containing the agent runtime, baked-in skills, and standard dev tooling. Versioned and pinned by `agentd` config.
- **MCP registry** — a table in `agentd`'s DB listing the MCP servers known to this install (name, URL, default-enabled flag).
- **Client** — `agentctl` or a Web UI browser tab. Both are stateless consumers of `agentd`.

## 3. Architecture principles

Constraints every requirement and the technical design must respect:

1. **`agentd` is the single source of truth.** Sessions, state, and lifecycle decisions live in `agentd`. Clients never persist session state.
2. **Containers do not self-manage.** They never decide to stop, restart, or change MCP membership; `agentd` does.
3. **CLI and Web UI are peers.** Anything reachable from one is reachable from the other.
4. **Local-only by default.** No remote agentd, no remote sessions, no cloud sync. All state stays on the developer's machine.
5. **One developer per machine in v1.** A single OS user runs one `agentd`. Multi-user is out of scope (§16).
6. **Deterministic startup.** A reboot brings `agentd` back, and existing sessions remain resumable from disk state without manual intervention.

## 4. Non-functional requirements

| Area | Target |
|---|---|
| Cold session start (image cached) | ≤5s p50, ≤10s p99 from `agentctl start` to attached event stream |
| Cold session resume after idle-stop | Same budget as cold start |
| Message round-trip overhead (client ↔ agentd ↔ container) | ≤50ms p99, exclusive of model latency |
| Concurrent sessions on a developer machine | ≥10 with default resource caps, on a 16 GB / 8-core box |
| Per-session memory cap (default) | 4 GB, configurable |
| Per-session CPU cap (default) | 2 cores, configurable |
| Idle threshold before container stop (default) | 15 min, configurable |
| Hard inactivity cutoff (auto-stop floor) | 24 h, configurable |
| Disk usage per idle session (volume only) | typically <500 MB; no enforced cap in v1 |
| Supported platforms | Linux (systemd-based distros, kernel ≥5.10) and macOS (≥13). Windows via WSL2 only. |
| Required host software | Docker Engine ≥24, sqlite via embedded driver |
| Secrets at rest | File-system permissions (`0600` files, `0700` dirs) under `~/.config/agentctl/`. OS keychain integration is out of scope for v1. |
| Localhost-only network exposure | `agentd`'s HTTP/WebSocket endpoint binds to `127.0.0.1` and `::1` only |
| Telemetry | None outbound by default. All logs are local. |

## 5. Default values

| Setting | Default | Override |
|---|---|---|
| Idle timeout (container stop) | 15 min | `agentctl config set session.idle_timeout` |
| Hard inactivity cutoff | 24 h | `agentctl config set session.max_idle` |
| Per-session memory cap | 4 GB | `agentctl config set session.mem_limit` |
| Per-session CPU cap | 2 cores | `agentctl config set session.cpu_limit` |
| Web UI bind | `127.0.0.1:7777` | `agentctl config set agentd.web_addr` |
| CLI socket | `~/.local/share/agentctl/agentd.sock` | not configurable in v1 |
| Default model | TBD (see §15.3) | per-session at start |
| MCPs enabled by default for new sessions | All registered | per-session at start |

---

## Requirements

### R1. One-command setup and session start

**Goal.** A developer goes from a clean machine to a running session with two commands, no manual Docker, config editing, or admin steps.

**Commands.**

| Command | Purpose |
|---|---|
| `agentctl init` | One-time machine setup. Re-runnable. |
| `agentctl init --reset-token anthropic\|github` | Rotate a stored token without touching the rest of the install. |
| `agentctl init --repair` | Reinstall the system service and re-verify state without re-prompting for tokens. |
| `agentctl start [--name <name>] [--mcps ...] [--no-mcp ...] [--repo <url>...]` | Start a new session and attach the terminal to it. |

**`agentctl init` behavior.**

1. Verifies Docker is installed and the daemon is reachable; aborts with a clear remediation message if not.
2. Pulls the base session image from the configured registry.
3. Prompts for `ANTHROPIC_API_KEY` and validates by issuing a minimal authenticated request; rejects on 401/403.
4. Prompts for the developer's GitHub PAT and validates against `GET /user`; rejects on 401.
5. Writes secrets to `~/.config/agentctl/secrets.json` (mode `0600`); parent dir is `0700`.
6. Initializes `~/.local/share/agentctl/agentd.db` (sqlite). Seeds the MCP registry with the team's default internal MCPs and the GitHub MCP entry.
7. Installs `agentd` as a per-user system service (systemd `--user` on Linux; launchd user agent on macOS) and enables auto-start on boot/login.
8. Waits for `agentd` to report healthy (`GET /healthz` returns 200) within 10s.
9. Prints a summary: service status, Web UI URL, registered MCPs, next step (`agentctl start`).

**`agentctl start` behavior.**

1. Connects to `agentd` over the local Unix socket. If unreachable, attempts to start the service once, then aborts with diagnostics.
2. Sends a `CreateSession` request with the supplied name, MCP overrides, and any `--repo` URLs to clone.
3. On success, prints the `session_id`, the Web UI deep-link, and attaches the terminal to the session's event stream.

**Data created/modified.**

- `~/.config/agentctl/secrets.json` (file, mode `0600`)
- `~/.config/agentctl/config.toml` (file, mode `0600`)
- `~/.local/share/agentctl/agentd.db` (sqlite)
- `~/.local/share/agentctl/sessions/<session_id>/` (created on `start`)
- System service unit/agent file

**Acceptance criteria.**

- A clean Linux/macOS machine with Docker installed completes `init` end-to-end in under 2 minutes (excluding image pull time).
- `systemctl --user is-active agentd` (Linux) or `launchctl print gui/$(id -u)/com.agentctl.agentd` (macOS) reports active after `init`.
- After a reboot, `agentd` is running without manual intervention; existing sessions are listable.
- `agentctl start` returns an attached session within the cold-start budget (§4).
- Stored secrets file mode is `0600`; parent directory mode is `0700`.
- Re-running `init` is idempotent: no duplicate MCP rows, no duplicate service installs, no token re-prompt unless `--reset-token` is passed.

**Error and edge cases.**

- Docker missing or daemon unreachable → exit code 2, message naming the platform install URL.
- Anthropic key validation fails → re-prompt up to 3 times, then abort with exit code 3.
- GitHub PAT validation fails → same.
- System service install fails (e.g., no `systemd --user` available) → fall back to a foreground `agentd` and warn loudly; sessions still work but won't survive logout.
- `~/.config/agentctl/` exists with wrong perms → fix to `0700`/`0600` and warn.
- `init --repair` re-runs install steps idempotently without re-prompting unless tokens are also missing.
- `agentctl start` invoked while `agentd` is unhealthy (responding but failing checks) → return a structured error pointing to `agentctl doctor`.

**Dependencies.** Foundation for all other requirements.

**Out of scope (this requirement).**

- Windows native install (WSL2 only).
- Multi-user agentd shared across OS users.
- Remote agentd reachable over the network.

---

### R2. On-demand local session provisioning

**Goal.** Every `agentctl start` produces a fresh, isolated container within seconds; idle sessions free resources but resume transparently.

**User-facing behavior.**

- `agentctl start` and Web UI "New Session" both create a brand-new session container — never a reused or pooled one.
- After idle, the user sends a new message (CLI or Web UI) and the session resumes; the user does not run any explicit "resume" command.
- `agentctl ls` lists all sessions with status: `running`, `stopped` (idle), `terminated`.
- `agentctl stop <session>` ends a session permanently (container removed, volume deleted) after a confirmation prompt.

**System behavior.**

- `agentd` provisions each session by creating a new container from the pinned base image, mounting the session's volume at a fixed path inside the container (e.g., `/work`), injecting env vars (R3), and starting the agent runtime entrypoint.
- `agentd` records `last_activity_at` on every inbound message, every outbound model response, and every tool event.
- An `agentd` background sweeper, running every minute, finds sessions whose `last_activity_at` is older than the idle timeout (default 15 min) and whose container is `running`; it issues `docker stop` (with a configured grace period) and updates the session row.
- A second sweeper enforces the hard inactivity cutoff (default 24 h) regardless of the idle timeout.
- On any inbound message routed to a `stopped` session, `agentd` starts the existing container (preserving the volume mount), waits for the agent runtime to report ready, and then forwards the message.
- `agentd` never pauses or unpauses containers in v1; only `start`/`stop`/`remove`.

**Data created/modified.**

- `agentd.db` `sessions` row: `session_id`, `name`, `status`, `created_at`, `last_activity_at`, `container_id`, `volume_path`, `mcp_set`, `repo_list`.
- Volume contents (managed by the agent runtime): conversation history, working dir, scratch files.

**Acceptance criteria.**

- Cold start: ≤5s p50 / ≤10s p99 once the base image is cached locally.
- Idle resume: same budget.
- After idle-stop, the session row remains in `agentctl ls` with status `stopped`; conversation, repos, and scratch files are intact on resume.
- Explicit `agentctl stop` removes the container, deletes the volume, and marks the session `terminated`. The session ID is no longer reusable.
- Killing the host process or rebooting does not lose state; on `agentd` startup, all sessions previously `running` are reconciled to `stopped` (since their containers exited) and resumable.

**Error and edge cases.**

- Container fails to start (e.g., image missing locally, Docker out of disk) → session row marked `error` with a reason; client surfaces a clear message and points to `agentctl doctor`.
- Container exits unexpectedly while running (OOM, crash) → `agentd` marks `stopped` and surfaces an event to attached clients; next message triggers normal resume.
- `docker stop` exceeds grace period → `agentd` issues `docker kill` and logs the forced stop.
- Idle-stop fires while a tool call is mid-flight → `agentd` defers the stop until the in-flight turn completes (see §15.4 for the open question on interrupting long-running tool calls).
- User issues `agentctl stop` while messages are queued → queued messages are dropped, client is informed, container/volume removed.
- Disk full at start time → fail fast with a remediation message.

**Dependencies.** R1 (install), R6 (volume persistence model), R7 (isolation primitives).

**Out of scope (this requirement).**

- Pre-warmed container pools.
- Container pause/unpause as a third lifecycle state.
- Cross-machine session migration.

---

### R3. Pre-loaded session environment

**Goal.** A new session has the agent runtime, the team's skills, dev tooling, secrets, and MCP wiring ready to use without any per-session setup.

**Baked into the base image.**

- The agent runtime (Claude Code) at a pinned version.
- Team-curated skills, slash commands, and agent definitions, installed at well-known paths the runtime auto-loads.
- Standard dev tooling: `git`, common language runtimes (Node, Python, etc., per team needs), build tools.
- Non-secret base configuration for the runtime.

**Injected per session at start (by `agentd`).**

- `ANTHROPIC_API_KEY` from `secrets.json` as an environment variable.
- The developer's GitHub PAT, exposed both as an environment variable and pre-configured in the container's git credential helper so `git clone`/`push` "just work."
- An MCP-config file (or env block) listing only the URLs of the MCPs selected for this session (R5), including the GitHub MCP with the PAT attached.
- Session metadata: `SESSION_ID`, `SESSION_NAME`.

**Agent runtime startup mode.** The runtime is started with permission prompting disabled (`--dangerously-skip-permissions` or the runtime's equivalent). All tools and MCP calls are auto-approved inside the container; the container's isolation (R7) is the sole safety boundary. See §15.1 for the resolved decision.

**Container filesystem layout.**

- `/work` — bind/volume mount for the session volume (working dir, repo clones, scratch).
- `/home/agent/.config/...` — runtime config, populated from injected files.
- `/skills/...` — baked-in skills, read-only.

**Repo onboarding (open product question, see §15.2).** v1 supports at minimum: `agentctl start --repo <git_url>` clones the listed repos into `/work` before attaching the user. Cloning *during* a session via the agent's tools also works since the PAT is wired up.

**Acceptance criteria.**

- A fresh session can run an Anthropic-API-backed turn without any extra config.
- A fresh session can `git clone` a private repo the user's PAT has access to without further prompts.
- Calling any MCP from the session's enabled set works without auth setup by the user.
- Skills baked into the image are listed by `/help` (R9) on first turn.

**Error and edge cases.**

- Missing or invalid `ANTHROPIC_API_KEY` at start → session fails fast with an error directing the user to `agentctl init --reset-token anthropic`.
- PAT rejected by GitHub during a `git clone` → surfaced as a tool error in the conversation; session does not crash.
- An enabled MCP URL is unreachable at start → session starts anyway; the unreachable MCP is reported in the session log and absent from the runtime's available tools. (See §15.5.)

**Dependencies.** R1 (secrets store), R5 (MCP registry).

**Out of scope (this requirement).**

- Per-session custom skills not present in the image.
- Live updates to the base image while a session is running.

---

### R4. CLI and Web UI as equal clients

**Goal.** Anything a developer can do in the CLI they can do in the Web UI, and vice versa. Multiple clients can attach to the same session simultaneously without divergence.

**User-facing behavior.**

- Sessions started in the CLI appear in the Web UI's session list immediately and are interactive there.
- Sessions started in the Web UI are reachable via `agentctl attach <session>` in a terminal.
- Both clients render the same conversation, the same status, the same diffs (R8), the same costs (R10), the same skill autocomplete (R9).
- Multiple Web UI tabs and one or more `agentctl attach` processes can be open against the same session at once. All see the same live stream. Any can send a message.
- `agentctl detach` (or closing the terminal) disconnects only that client; the session continues.

**Streaming model.**

- The container talks only to `agentd`, over a single bidirectional channel (e.g., a Unix socket inside the container). It does not know clients exist.
- `agentd` is the fan-out point. For each session, it maintains a list of attached clients and broadcasts events to all of them.
- Transport:
  - CLI ↔ agentd: local Unix socket; events as length-prefixed JSON or NDJSON.
  - Browser ↔ agentd: HTTP for control endpoints, WebSocket for the event stream, served on `127.0.0.1:7777` (default).
- Inbound messages from any client are forwarded to the container by `agentd`. Concurrency policy is defined in §15.4 (open question).

**Web UI surface (v1).**

- Session list (with status, last activity, running cost).
- Session detail view: conversation, message input, "Changes" tab (R8), cost panel (R10), MCP list (read-only post-start).
- "New Session" form: name, MCP checkboxes, optional repo URLs.
- "Settings → MCPs" view (R5).
- "Usage" view (R10).

**CLI surface (v1).**

| Command | Purpose |
|---|---|
| `agentctl start` | Create and attach to a new session (R2). |
| `agentctl ls` | List sessions with status, last activity, cost. |
| `agentctl attach <session>` | Attach the terminal to an existing session's event stream. |
| `agentctl detach` | Detach the current terminal from its session. |
| `agentctl stop <session>` | End a session permanently (R2). |
| `agentctl diff <session>` | Print diffs (R8). |
| `agentctl export <session> --patch [path]` / `--push <branch>` | Export changes (R8). |
| `agentctl mcp list\|add\|remove` | Manage MCP registry (R5). |
| `agentctl cost <session>` / `cost --since <range>` | Show cost (R10). |
| `agentctl logs <session>` | Stream agentd-side logs for a session. |
| `agentctl doctor` | Diagnose install/connectivity issues. |
| `agentctl config get\|set` | Read/write `config.toml`. |

**Acceptance criteria.**

- A message typed in any one client appears in all attached clients within the round-trip budget (§4).
- The same session displays consistent state in CLI and UI (status, last message, cost).
- Detaching a client does not affect the session or other clients.
- `agentctl ls` and the Web UI session list contain the same set of sessions at any moment.

**Error and edge cases.**

- Browser ↔ agentd WebSocket drops → client reconnects automatically and replays missed events from a server-side buffer (R6).
- CLI client process killed mid-stream → session continues; on reattach, history is intact.
- Two clients send a message at the same instant → resolved by §15.4's concurrency policy.

**Dependencies.** R6 (continuity across reconnects), R5 (MCP visibility).

**Out of scope (this requirement).**

- A separate "admin" UI distinct from the developer UI.
- Mobile UI.
- Web UI exposed beyond `localhost`.

---

### R5. MCP registry and per-session selection

**Goal.** The set of MCP servers the install knows about is data, editable from either client. The set active for a session is chosen at session start and fixed for the session's lifetime.

**Registry data model.**

`agentd.db` `mcp_registry` table:

| Column | Type | Notes |
|---|---|---|
| `name` | text, primary key | Short slug (e.g., `github`, `jira`). |
| `url` | text | MCP server URL. |
| `transport` | text | Wire transport. v1 recognizes `http` (Streamable HTTP) and `sse` (Server-Sent Events). Freeform — new transports add without a schema migration; an MCP with an unrecognized transport is skipped at session start with a clear event. |
| `kind` | text | Freeform identifier for the auth style. v1 recognizes `none` (no auth) and `github_pat` (uses the developer's PAT). New kinds can be added without a schema migration; an MCP with an unrecognized kind is skipped at session start. |
| `auth_config_json` | text, optional | Kind-specific structured config (e.g., header overrides, OAuth client id for future kinds). v1's two kinds need none. |
| `default_enabled` | bool | Whether this MCP is checked by default in the New Session form. |
| `description` | text, optional | Free text shown in UI. |
| `created_at` | timestamp | |

`transport` and `kind` are independent: any combination is valid (e.g., a `sse` server with `github_pat` auth, an `http` server with `none`). Auth credentials are carried as request headers regardless of transport, so adding a new transport in the future does not require touching the auth layer.

**Initial seed at `init`.** `agentd` seeds the registry with the team's known internal MCPs (URLs come from a shipped config file or the install template — see §15.6) and the GitHub MCP entry.

**Web UI surface — Settings → MCPs.**

- Table of registered MCPs with name, URL, transport, kind, default-enabled toggle.
- "Add MCP" form: name, URL, transport (radio/dropdown; v1 known values `http` (default) and `sse`), kind (free-text combo box; v1 known values `none` (default) and `github_pat`), optional `auth_config` JSON, description.
- Edit and remove buttons per row.
- Changes apply only to *future* sessions; running sessions are unaffected and the UI says so.

**CLI surface.**

| Command | Behavior |
|---|---|
| `agentctl mcp list` | Tabular list of all registry entries. |
| `agentctl mcp add <name> --url <url> [--transport <t>] [--kind <kind>] [--auth-config <json>] [--default-enabled]` | Insert a new entry. `--transport` accepts any string; v1 recognizes `http` (default) and `sse`. `--kind` accepts any string; v1 recognizes `none` (default) and `github_pat`. |
| `agentctl mcp remove <name>` | Delete an entry (with confirmation). |
| `agentctl mcp set-default <name> on\|off` | Toggle default-enabled. |

**Per-session selection.**

- New Session form (UI): one checkbox per registry entry, pre-checked per `default_enabled`.
- CLI: `--mcps a,b,c` selects exactly those; `--no-mcp x` removes from defaults; flags are mutually exclusive.
- Selection is persisted on the session row as `mcp_set`.
- At container start, `agentd` writes only the selected MCPs into the runtime config (R3).

**Acceptance criteria.**

- The MCP registry survives `agentd` restarts and reboots.
- Adding/removing an MCP via either client is reflected in the other within seconds.
- Two concurrent sessions can hold different MCP sets and use them independently.
- Removing a registry entry while a session that uses it is running does not affect that session.
- Editing a registry entry's URL while a session is running does not change that session's URL.

**Error and edge cases.**

- Adding a duplicate name → reject with a clear error.
- Adding an MCP with an unreachable URL → accepted (we don't probe at registration time); start-time reachability is per R3's open question §15.5.
- Removing an MCP that is currently `default_enabled` → allowed, but warn that future-session defaults may change.

**Dependencies.** R3 (MCP wiring at start), R4 (UI/CLI parity).

**Out of scope (this requirement).**

- Live MCP toggling during a running session.
- Authentication beyond "no auth" and "GitHub PAT."
- Per-MCP scoping of which tools are exposed.

---

### R6. Conversation continuity

**Goal.** A session's conversation and working state are preserved across messages, client reconnects, idle stops, `agentd` restarts, and host reboots. Only an explicit "End Session" destroys state.

**State model.**

- **Authoritative state per session lives in two places:**
  - The **session volume** holds the agent runtime's conversation history, the working directory, repo clones, and scratch files. It is the durable, replayable record.
  - The **`agentd` DB row** holds metadata: ID, name, status, timestamps, container ID, volume path, MCP set, last-known cost (R10), and a small server-side **event buffer** (recent stream events, capped) for client reconnects.
- The container's in-memory state is treated as ephemeral.

**What persists across each scenario.**

| Scenario | What persists | How |
|---|---|---|
| Multiple messages in a session | Everything | Container memory + volume |
| Client reconnect after disconnect | Everything | `agentd` event buffer + volume; client re-fetches snapshot then resumes stream |
| Multiple clients attached at once | Everything; all clients see same stream | `agentd` fan-out (R4) |
| Idle-stop and resume (R2) | Everything except in-memory tool state | Volume re-mounted into a fresh container; runtime resumes from history files |
| `agentd` restart | All sessions, recoverable | DB + volumes; on startup, `agentd` reconciles container statuses with Docker, marks orphaned `running` rows as `stopped`, leaves volumes intact |
| Host reboot | Same as `agentd` restart | System service starts `agentd` on boot |
| Explicit End Session | Nothing | Container removed, volume deleted, row marked `terminated` |

**`agentd` startup reconciliation.**

1. Read all sessions from DB.
2. For each, query Docker for the container ID. If it exists and is running, leave as `running`. If it exists and is stopped, mark `stopped`. If it does not exist, mark `stopped` with a flag indicating "container will be re-created on next message" (`agentd` re-creates on next inbound message because Docker wiped the container; the volume is still there).
3. Replay no events. Clients reconnecting fetch the runtime's history from the volume on next attach.

**Event buffer.**

- `agentd` keeps the last N events per session (e.g., last 200 or last 5 minutes, whichever is larger) in memory or in a small on-disk ring. On client reconnect, the client provides the last event ID it saw and `agentd` replays anything newer. Events older than the buffer are reconstructed from the runtime's history files via the snapshot endpoint.

**Acceptance criteria.**

- Closing the terminal or browser tab and reopening returns the user to a session with full history visible.
- Forcing `agentd` to restart (e.g., `systemctl --user restart agentd`) does not lose any session data; sessions are listable and resumable immediately afterward.
- A reboot in the middle of a session leaves the session resumable on next login; the next message picks up from the last persisted point in the conversation history.
- "End Session" is the only action that destroys state, and it requires explicit confirmation in both clients.
- Two clients reconnecting after a network blip both end up showing identical conversation state.

**Error and edge cases.**

- Volume corruption (e.g., partial write during sudden power loss) → `agentd` surfaces a recovery prompt; in v1 the safe action is "End Session and start a new one" rather than auto-repair.
- DB corruption → `agentd` refuses to start until repaired by `agentctl doctor --repair-db`; volumes are not touched.
- Event buffer overflow during long disconnect → on reconnect, the client fetches a fresh snapshot from the runtime's history rather than replaying events.

**Dependencies.** R2 (idle stop/resume mechanics), R4 (client streaming model).

**Out of scope (this requirement).**

- Backup/restore of the DB or volumes to external storage.
- Cross-machine session migration.
- Branching or forking a session.

---

### R7. Isolation between concurrent sessions

**Goal.** Multiple sessions on the same machine cannot read each other's files, secrets, or processes, and a runaway session cannot starve the others.

**Isolation guarantees.**

- **Filesystem.** Each session has a dedicated container with its own root filesystem and a private volume mounted at `/work`. No volume is shared across sessions.
- **Processes.** Default Docker process namespacing; sessions cannot see each other's PIDs.
- **Secrets.** Env vars are set on the container at creation. The host-side `secrets.json` is never bind-mounted into containers. One session's container has no access to another's env block.
- **Network.** Each session container runs on its own per-session Docker bridge network with inter-container communication disabled (`enable_icc=false`). This prevents one session container from reaching another by hostname or IP. Egress to the public internet is **not** restricted in v1 — the container can reach Anthropic, GitHub, configured MCPs, and anywhere else its DNS can resolve. `agentd`'s admin API on `127.0.0.1` is reachable network-wise from the container, but is gated by a per-install bearer token the container has no filesystem path to read (see R3 secrets handling). Strict outbound egress filtering is deferred to v2 (`v2-requirements.md` §V2.1).
- **Resource caps.** Each container is created with `--memory` and `--cpus` flags from defaults (§5) overridable per session by `agentctl start --mem-limit ... --cpu-limit ...`.

**Trust boundary.**

- The container is treated as a hostile process from `agentd`'s perspective in this sense: `agentd`'s admin API requires a bearer token (`web_token`) the container has no path to read; the only inbound surface from a container to `agentd` is the dedicated session event channel.
- `agentd` validates and rate-limits messages from a container so a misbehaving runtime cannot flood the host.
- Per §15.1, tool prompting is disabled inside the container, so the **container's filesystem isolation, resource caps, and `agentd`'s auth-gated admin API are the safety boundary** for what the agent can and cannot affect. Anything reachable from inside `/work`, plus anywhere the container's network can route to (Docker's default egress posture), is fair game in v1. Strict outbound filtering is v2 (`v2-requirements.md` §V2.1).

**Acceptance criteria.**

- A file written in session A's `/work` is not visible in session B's `/work`.
- An env var set in session A is not readable in session B.
- A container in session A cannot reach a container in session B by hostname or IP (enforced by per-session Docker bridge with ICC disabled).
- Memory exhaustion in one session results in that container being OOM-killed by Docker (and surfaced as a session error per R2), not host-wide degradation.
- CPU saturation in one session does not prevent another session from making progress on a multi-core host.

**Error and edge cases.**

- A session attempts to reach another session container by hostname or IP → fails because the per-session Docker network has ICC disabled; surfaced as a tool error.
- A user sets resource caps so low the runtime cannot start → fail fast with a clear message.

**Dependencies.** R2 (container lifecycle), R3 (env var injection).

**Out of scope (this requirement).**

- Hardened sandboxing beyond Docker's defaults (gVisor, Kata, etc.).
- Inter-session messaging or shared volumes (could be a v2+ feature).

---

### R8. Code change visibility and export

**Goal.** When the agent edits files in a cloned repo, the developer can review and extract the changes without entering the container.

**Repository onboarding (this requirement assumes one of these — see §15.2).**

- `agentctl start --repo <url>` clones one or more repos into `/work/<repo_name>` at session start using the developer's PAT.
- The agent can also `git clone` during a session.

In both cases, the cloned repo's original branch and SHA are recorded by `agentd` at clone time so diffs have a stable base.

**Visibility — Web UI.**

- A "Changes" tab in the session detail view lists each repo present in `/work` (auto-discovered).
- Per repo: head branch, base ref (recorded at clone time), and the live diff against base.
- Files added/modified/deleted shown in a tree; clicking a file shows side-by-side or unified diff.
- Updates as the agent edits (polled every few seconds or pushed via the event stream).

**Visibility — CLI.**

- `agentctl diff <session>` prints a unified diff for all repos.
- `agentctl diff <session> --repo <name>` scopes to one repo.
- `agentctl diff <session> --stat` shows a numstat summary.

**Export.**

| Command | Behavior |
|---|---|
| `agentctl export <session> --patch [path]` | Writes a `.patch` (`git diff` format) to `path` or stdout. Multiple repos produce one patch per repo, in a directory if `path` is provided. |
| `agentctl export <session> --push <branch> [--repo <name>]` | Inside the container, runs `git checkout -B <branch>`, commits the working tree (with a configurable message), and `git push -u origin <branch>` using the session's PAT. Returns the remote URL or PR-create URL on success. |
| Web UI buttons | Same two actions exposed as "Download patch" and "Push to branch…". |

**Acceptance criteria.**

- The "Changes" view correctly reflects the working tree against the base ref for each repo, including untracked files (treated as additions).
- `agentctl diff` matches what `git diff` would produce inside the container against the recorded base.
- `--patch` output applies cleanly with `git apply` against the same base.
- `--push <branch>` succeeds for any repo the PAT can write to and surfaces a clear error otherwise.

**Error and edge cases.**

- Repo has uncommitted changes from initial clone (shouldn't normally) → diff base is the recorded SHA, not HEAD.
- Push rejected (branch protection, lack of permissions) → stderr surfaces the git error verbatim and exit code is non-zero.
- Repo path inside `/work` was deleted by the agent → "Changes" shows the repo as removed; diff is unavailable for that repo.
- Multiple repos in `/work` → views and exports list each separately.

**Dependencies.** R3 (PAT and git wiring), R7 (working dir isolation), §15.2 (repo onboarding decision).

**Out of scope (this requirement).**

- Opening a PR directly from `agentctl` (push only).
- Conflict resolution UI for rebases.
- Squash/fixup helpers.

---

### R9. Explicit skill invocation by name

**Goal.** Skills baked into the image are not only available for the agent to discover from context — the developer can run any of them by name to remove model judgment from the loop.

**User-facing behavior.**

- Typing `/<skill-name>` (optionally followed by arguments) in the message input of either client invokes that skill directly.
- Typing `/` opens an autocomplete menu listing available skills filtered as the developer types. Each entry shows the skill name and short description.
- `/help` lists all available skills with their descriptions.
- Skill invocations are visible in the conversation as the user message that triggered them, followed by the skill's effects.

**Source of skills.**

- Skills come from the baked-in image (R3). The runtime exposes a manifest listing skill names and descriptions; both clients fetch this manifest from `agentd` (which reads it from the running container) on session attach and on a "skills changed" event.
- v1 does not support adding skills at runtime; updates require a new base image.

**Acceptance criteria.**

- Every skill present in the image is invokable by `/<name>` and listed by `/help`.
- Autocomplete in CLI and Web UI both filter the same manifest and produce the same suggestions.
- An invalid skill name produces a clear error in the conversation, not silent fallthrough.
- A skill that takes arguments accepts them after the name (`/<name> <args...>`); both clients pass arguments through unchanged.

**Error and edge cases.**

- A skill fails internally → its error surfaces as a tool error in the conversation; the session is unaffected.
- Skill manifest changes mid-session (e.g., container restart with an updated image — though out of scope per R3) → clients refetch on the next attach.

**Dependencies.** R3 (skills baked into the image), R4 (CLI/UI parity).

**Out of scope (this requirement).**

- User-defined skills added per session.
- Skill marketplaces or registries.
- Skill-level permission prompting separate from tool permission (§15.1).

---

### R10. Per-session cost visibility

**Goal.** Every session tracks its Anthropic API usage and cost. Developers can see what each session has spent and what they've spent in aggregate over time.

**Tracking.**

- `agentd` extracts token-usage fields (input tokens, output tokens, cache reads, cache writes, model name) from every model response event in the session's stream.
- For each turn, `agentd` writes a row to a `usage` table:

| Column | Notes |
|---|---|
| `id` | autoincrement |
| `session_id` | foreign key |
| `at` | timestamp |
| `model` | string (e.g., `claude-opus-4-7`) |
| `input_tokens` | int |
| `output_tokens` | int |
| `cache_read_tokens` | int |
| `cache_write_tokens` | int |
| `cost_usd` | computed at insert time from a per-model price table baked into `agentd` config |

- Aggregates per session and per time window are computed by SQL on read.
- A per-model price table lives in `agentd`'s config (`config.toml`) and is updatable by the developer when prices change.

**Display — Web UI.**

- Session list: each row shows running cost (e.g., "$0.42 — 12k in / 38k out").
- Session detail: a cost panel showing total cost, breakdown by model, and a turn-by-turn timeline (small bar/line chart and a table).
- Top-level "Usage" page: totals across all sessions with a date range filter (today / last 7d / last 30d / custom) and per-session breakdown.

**Display — CLI.**

| Command | Output |
|---|---|
| `agentctl ls` | Includes a `cost` column per session (current running total). |
| `agentctl cost <session>` | Per-session detail: total, per-model breakdown, turn-level timeline (text table). |
| `agentctl cost --since <range>` | Aggregate across all sessions in the range; supports `7d`, `30d`, `2026-05-01..2026-05-09`, etc. |

**Persistence.**

- Cost rows live past session end. The session row's `terminated` status does not delete usage rows; the `usage` table outlives volumes and containers.
- `agentctl cost` with date filters returns historical totals even for terminated sessions.

**Acceptance criteria.**

- A turn that the API reports as N tokens at price P generates a `usage` row with the correct token counts and `cost_usd = N * P` (per the model's pricing entry).
- Session cost in CLI and UI never differ by more than one in-flight turn.
- Aggregating `cost_usd` across all `usage` rows in a date range matches `agentctl cost --since <range>`.
- Updating the per-model price table affects future rows only; historical rows retain their original `cost_usd`.

**Error and edge cases.**

- Model name returned by the API is not in the price table → row inserted with `cost_usd = NULL` and a warning logged; UI shows "—" with a tooltip.
- Token-usage field absent from a response (unexpected) → row inserted with whatever fields are present; missing fields are zero.
- Clock skew on the host → cost is event-ordered by sequence, not wall clock; the `at` column is informational.

**Dependencies.** R4 (event stream observation), R6 (DB persistence).

**Out of scope (this requirement).**

- Cost limits / budgets / alerts (could be v2).
- Per-developer attribution beyond "this machine's user."
- Anything other than Anthropic API costs (e.g., MCP server compute is not tracked).

---

## 15. Open product questions — for the architect to resolve

The questions below are **owned by the technical-architecture pass**. Each was deliberately deferred from this requirements document because it materially affects implementation choices the architect will be making anyway. The architect must:

1. Pick a resolution for every open item below (the recommended starting point is acceptable unless the architect has a reason to deviate).
2. Update this document by replacing each "OPEN" entry with a "RESOLVED" entry stating the decision and its consequences (mirroring the format of §15.1, which has already been resolved).
3. Surface any new questions that arise during architecture as additional `15.x` entries here, so this section remains the canonical record of resolved product questions.

**Status summary.**

| # | Topic | Status | Resolution (one-line) |
|---|---|---|---|
| 15.1 | Tool permission model | RESOLVED | Runtime runs with permission prompting disabled; container is the sole safety boundary. |
| 15.2 | Repo onboarding flow | RESOLVED | `--repo` clone-at-start and in-session clone only. No host bind-mounts in v1. |
| 15.3 | Default model and per-session selection | RESOLVED | Configurable via `agentctl config`, per-session `--model`. Default `claude-sonnet-4-6`. |
| 15.4 | Concurrency / interrupt model | RESOLVED | Per-session FIFO queue at `agentd`; explicit `Interrupt` action; arrival-order serialization across clients. |
| 15.5 | MCP reachability checks at start | RESOLVED | Soft-warn: probe enabled MCPs in parallel with a 1.5s timeout, surface failures, do not block start. |
| 15.6 | MCP registry seed source | RESOLVED | Embedded default seed in the `agentctl` binary, overridable via `/etc/agentctl/registry.seed.toml` or `~/.config/agentctl/registry.seed.toml`. |
| 15.7 | Web UI auth on localhost | RESOLVED | Per-install bearer token in `~/.config/agentctl/web_token`; required on every HTTP/WS request; strict `Origin` check; CLI hands the token to the browser via a one-shot URL fragment. |
| 15.8 | Image and skill update path | RESOLVED | Explicit `agentctl update`; pulls and pins new tag; running sessions keep their pinned image; `agentctl update --report` lists sessions whose next resume will adopt the new image and which require an explicit restart. |
| 15.9 | Logging and observability layout | RESOLVED | `agentd` daemon log goes to journald (Linux) / unified log (macOS). Per-session NDJSON log at `~/.local/share/agentctl/sessions/<id>/agentd.log` with size+age rotation. `agentctl logs <session>` tails the per-session file; `agentctl logs --daemon` shells out to the system log. |

### 15.1. Tool permission model — RESOLVED

**Decision:** the agent runtime runs **inside the container with permission prompting disabled** (`--dangerously-skip-permissions` or equivalent). All tools, including Bash, file edits, and MCP calls, are auto-approved. **The container's isolation (R7) is the sole safety boundary.**

Implications:

- No permission events are surfaced to clients; the conversation stream contains tool calls and tool results only.
- The container is treated as fully agent-controlled; nothing outside the container (host filesystem, other sessions, host network beyond what R7 allows) is reachable.
- Destructive operations *inside* the container (e.g., `rm -rf /work`) are possible and only undone by ending the session and starting a new one. The session volume is the blast-radius cap.
- Pushes to remote repos via `git` use the developer's PAT; the developer is responsible for the consequences of those pushes (including force-pushes), the same as they would be running git themselves.

### 15.2. Repo onboarding flow — RESOLVED

**Original question.** How do repos enter a session? Options included `--repo` clone, in-session `git clone`, and host bind-mounts.

**Decision:** v1 supports exactly two onboarding paths:

1. `agentctl start --repo <url> [--repo <url> ...]` clones each repo into `/work/<repo_basename>` at session start using the developer's PAT, before the runtime is exposed to clients.
2. The agent runtime running inside the container can `git clone` at any time during the session because the PAT is wired into both the env and the git credential helper (R3).

**Host bind-mounts are explicitly excluded.** Bind-mounting a working copy from the host into `/work` would let a runaway agent write back to the developer's source tree without the session-volume blast-radius cap and would break the per-session isolation guarantees in R7.

**Implications.**

- `agentd` records the resolved commit SHA and remote branch for every cloned repo (start-time clones and in-session clones discovered by the file-watcher) so R8 diffs have a stable base.
- Developers who want to feed a working tree into a session use `git push` to a branch (or a temporary remote) and `--repo` that URL. Convenience for the "I have unpushed local edits" case is deferred to a future requirement.
- The CLI does not accept `--bind` / `--mount` flags in v1. Adding them later would be additive and would not break existing flows.

See `architecture/decisions/0002-repo-onboarding.md`.

### 15.3. Default model and per-session model selection — RESOLVED

**Original question.** Is the model fixed by the base image, or selectable per session? Default Opus, Sonnet, or Haiku?

**Decision:** the model is selectable per session and defaults to **`claude-sonnet-4-6`** (the latest Sonnet at the time of v1 ship), with a default override path:

- `agentctl config set model.default <id>` writes to `config.toml` and changes the default for new sessions.
- `agentctl start --model <id>` overrides for a single session.
- The model is recorded on the `sessions` row at start time; it does not change for the lifetime of the session.
- The base image stays model-agnostic. `agentd` injects `AGENTCTL_MODEL` into the container at start; the runtime shim passes it to the agent runtime.
- The model id is validated at start against the price table (`config.toml` `[pricing]` block, R10). Unknown ids start the session anyway but log a warning and produce `cost_usd = NULL` rows (matches R10's documented behavior).

Sonnet is the default because it is the cost-balanced choice for everyday coding work; Opus and Haiku remain one-flag away.

See `architecture/decisions/0003-default-model.md`.

### 15.4. Concurrency / interrupt model — RESOLVED

**Original question.** What happens to new inbound messages while the agent is mid-turn? How are simultaneous messages from multiple clients reconciled?

**Decision:** option **A** — queue and deliver after the current turn finishes — plus an explicit `Interrupt` action exposed in both clients.

Concrete behavior:

1. `agentd` maintains a single per-session FIFO message queue. While a turn is in flight (any state from "user message accepted" through "final assistant message + tool results emitted"), additional inbound messages from any client are **queued** and a `queue.depth` event is broadcast so all clients can render a "queued" indicator.
2. When the current turn completes, `agentd` dequeues and forwards the next message. Clients see a `turn.start` event for it.
3. **Interrupt** is a separate, idempotent action (`POST /v1/sessions/<id>/interrupt`, `agentctl interrupt <session>`). It cancels the in-flight turn at the runtime by emitting `interrupt` on the control channel. The runtime stops the model stream, finalizes any tool result it was already waiting on, and emits `turn.cancelled`. The queue is preserved unless the user also passes `--clear-queue`.
4. Two clients sending at the "same instant" are serialized by arrival order at the `agentd` socket (single accept loop per session). The queue records the originating client id so clients can render "from another tab."
5. Idle-stop sweepers (R2) defer the stop while a turn is in flight or the queue is non-empty; the timer resets when both conditions are clear and `last_activity_at` ages past the threshold.

UX surface: a **Stop** button in the Web UI and `agentctl interrupt <session>` in the CLI both invoke `Interrupt`. A toggle (`agentctl config set session.queue_policy reject`) exists as an escape hatch but is not the default.

See `architecture/decisions/0004-concurrency-and-interrupt.md`.

### 15.5. MCP reachability checks — RESOLVED

**Original question.** Should `agentd` probe enabled MCPs at session start? Hard fail or soft-warn on unreachable ones?

**Decision:** **soft-warn**.

- At session start, before launching the runtime, `agentd` issues a parallel TCP-connect probe (or HTTP `GET /` if the URL has a path, with a `User-Agent: agentctl-probe`) to each enabled MCP with a **1.5s** per-probe timeout and a **3s** overall ceiling.
- Reachable MCPs are recorded as `ok` on the `sessions.mcp_status` JSON column.
- Unreachable MCPs are recorded as `unreachable` with the failure reason and surfaced as a `mcp.unreachable` stream event the moment any client attaches.
- The runtime is started with all enabled MCPs configured regardless. This lets a transiently down MCP recover later in the session without restarting it.
- The Web UI's MCP panel shows status next to each MCP; CLI shows it in `agentctl ls --verbose` and at the top of `agentctl attach`.

Rationale: hard-failing leaves developers locked out of work because of a transient internal-MCP outage; soft-warn satisfies the goal of "make problems visible" without blocking. The decision is reversible per-install if needed (`agentctl config set session.mcp_probe block_on_failure`).

See `architecture/decisions/0005-mcp-reachability-checks.md`.

### 15.6. MCP registry seed source — RESOLVED

**Original question.** Where does the initial registry seed live?

**Decision:** a **layered shipped config**:

1. The `agentctl` binary embeds a default `registry.seed.toml` (the team's known internal MCPs and the GitHub MCP).
2. Site override: `/etc/agentctl/registry.seed.toml`, if present, replaces the embedded defaults entirely.
3. User override: `~/.config/agentctl/registry.seed.toml`, if present, replaces the site default.
4. Resolution: first match wins (user → site → embedded).
5. `agentctl init` and `agentctl init --repair` apply the resolved seed via `INSERT OR IGNORE` on `mcp_registry.name`. Re-running never overwrites existing entries (R5 acceptance criterion: idempotent `init`).

No network fetch happens during `init` for registry seed — both for offline installability and to keep `init` reproducible.

Format example:

```toml
[[mcp]]
name = "github"
url = "https://api.githubcopilot.com/mcp/"
transport = "http"
kind = "github_pat"
default_enabled = true
description = "GitHub MCP server (uses developer PAT)."

[[mcp]]
name = "internal-jira"
url = "https://mcp.internal.example.com/jira/sse"
transport = "sse"
kind = "none"
default_enabled = false
description = "Team Jira MCP (SSE transport)."
```

See `architecture/decisions/0006-mcp-registry-seed.md`.

### 15.7. Web UI auth on localhost — RESOLVED

**Original question.** Should we add a token / origin check to prevent any process on the host from driving the Web UI?

**Decision:** per-install bearer token + strict `Origin` enforcement + token-handoff via fragment.

Specifics:

1. At `init`, `agentd` generates a 256-bit URL-safe random token and writes it to `~/.config/agentctl/web_token` (`0600`). It is rotatable via `agentctl init --reset-web-token`.
2. Every HTTP request and WebSocket connection to `agentd`'s Web UI port carries the token in an `Authorization: Bearer <token>` header **or** an `agentctl_token` cookie set by the loader page. Requests missing or carrying a wrong token return `401`.
3. The token is **never** in URL query strings or paths (logs, browser history, referer leakage). The single exception is the loader URL fragment (`#t=<token>`); fragments are not sent to the server and the loader page strips them from the URL after extracting the token to a `SameSite=Strict; Secure=false; HttpOnly=false` cookie.
4. CSRF protection: every state-changing request must include `Origin: http://127.0.0.1:7777` (or the configured bind). Any other Origin or a missing Origin on POST/PATCH/DELETE returns `403`. Static assets are served by `agentd` itself; no third-party origins are involved.
5. The CLI opens the UI via `agentctl ui` (or as part of `agentctl init`'s final summary): it constructs `http://127.0.0.1:7777/#t=<token>` and shells out to the platform browser launcher.
6. WebSocket connections use the cookie set by the loader; the server checks both Origin and cookie before accepting.

This is "raise the bar", not "secure against root." A process running as the same user can read `~/.config/agentctl/web_token` directly — that is the same trust boundary as `secrets.json` and is documented in `architecture/security.md`.

See `architecture/decisions/0007-web-ui-auth.md`.

### 15.8. Image and skill update path — RESOLVED

**Original question.** How are the base image and the skills inside it updated?

**Decision:** explicit, never automatic.

- `agentctl update` is the developer's one entry point. It:
  1. Resolves the configured image reference (`config.toml` `[image]`, default `agentctl/session-base:vN`) to a digest by calling Docker's pull.
  2. Updates `config.toml` `image.pinned_digest` to the new digest.
  3. Records the previous digest as `image.previous_digest` so a rollback is one command (`agentctl update --rollback`).
  4. Prints a per-session report: each session's currently-running container's image digest vs the new pinned digest, and whether the next resume will adopt the new image automatically (sessions in `stopped` status will) or whether the user needs to restart it (sessions in `running` status keep their image until the next stop/resume cycle).
- `agentctl update --report` prints the same per-session report without pulling.
- A separate flag, `agentctl update --restart-stopped`, force-restarts already-stopped sessions to validate they still come up. (No effect on running sessions.)
- `agentctl restart <session>` exists as the manual upgrade trigger for running sessions. It cancels in-flight turn (with confirmation), stops the container, recreates from the new pinned digest, preserves the volume, and reattaches.
- `agentctl update` for the agentctl CLI and `agentd` daemon binaries is **out of scope** for v1; developers re-run the canonical installer script (`curl -fsSL https://install.agentctl.dev/install.sh | bash`), which detects the existing install, verifies the new release's signature, and atomically replaces the binary in place. `agentctl init --repair` then re-stamps the system service file and restarts the daemon. A native `agentctl self-update` subcommand (no curl pipe) is a v2 candidate.
- Skills ship inside the base image and follow the same update path. The skills manifest (R9) is re-fetched from the new container on attach.

Rationale: developers own when their working agent flips to a new image. Auto-pulling mid-session would surprise mid-debugging users and complicate cost attribution.

See `architecture/decisions/0008-image-update-path.md`.

### 15.9. Logging and observability — RESOLVED

**Original question.** Where do daemon and per-session logs live, what's their format, and what does `agentctl logs <session>` tail?

**Decision:** two-tier logging.

**Daemon log (cross-session).**

- Linux: `agentd` writes structured logs to stderr; the systemd `--user` unit captures them in journald. `journalctl --user -u agentd` is the command.
- macOS: `agentd` writes to stderr; launchd redirects to `~/Library/Logs/agentctl/agentd.log` (configured in the plist) **and** to the unified log via `os_log` for daemon-level events. The on-disk file is rotated by agentd's own rotator (size/age) since `launchd` has no built-in rotation.
- Format: NDJSON, one event per line. Required fields: `ts` (RFC3339Nano), `level` (`debug|info|warn|error`), `component`, `msg`. Optional: `session_id`, `error`, `dur_ms`, plus arbitrary structured fields.

**Per-session log.**

- File: `~/.local/share/agentctl/sessions/<session_id>/agentd.log`, NDJSON, `0640`, owned by the agentd user.
- Contains all `agentd` events scoped to that session (lifecycle, control-channel I/O metadata — never message contents — sweeper actions, MCP probe results, errors).
- Rotation: in-process, rotate when file exceeds 50 MB or daily, whichever first; keep 7 generations (`agentd.log.1`..`agentd.log.7`); compressed with gzip after rotation.
- Volume: bounded by the session's lifetime + retention; sessions terminated (`agentctl stop`) delete the directory including logs.

**CLI commands.**

- `agentctl logs <session>` tails the per-session NDJSON, pretty-printing by default; `--raw` emits NDJSON; `-f` follows.
- `agentctl logs --daemon` tails the daemon log: `journalctl --user -u agentd -f` on Linux, the `~/Library/Logs/agentctl/agentd.log` file on macOS.
- `agentctl logs <session> --container` is a thin pass-through to `docker logs <container_id>` if a developer needs the runtime's stdout/stderr directly.

See `architecture/decisions/0009-logging-layout.md` and `architecture/observability.md`.

---

## 16. Out of scope for v1

Explicit deferrals so scope doesn't drift:

- **Multi-user.** A shared `agentd` serving multiple OS users on one machine.
- **Remote agentd.** A CLI on machine A talking to an `agentd` on machine B.
- **Cloud-hosted sessions.** Containers running anywhere other than the developer's machine.
- **Live MCP toggling** during a running session.
- **User-defined skills** added per session.
- **Session forking, branching, or migration.**
- **Cost limits, budgets, alerts.**
- **Backup/restore** of DB or volumes to external storage.
- **Hardened sandbox** beyond Docker defaults.
- **Mobile UI** or non-localhost web exposure.
- **Windows native** (WSL2 only).
- **Telemetry/analytics** sent to any service.
- **Pre-warmed container pools** to push start latency below cold-image times.
- **Container pause/unpause** as a third lifecycle state alongside running/stopped.
- **Strict outbound network egress filtering** for session containers (allowlist of Anthropic / GitHub / MCPs only). Deferred to v2; see `v2-requirements.md` §V2.1.

---

## 17. Cross-references

- Components and glossary: §2
- Architecture principles: §3
- Non-functional targets: §4
- Default values: §5
- Per-requirement detail: R1 – R10
- Open product questions: §15
- Out of scope: §16
