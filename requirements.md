# Local Remote-Agent Runtime: Structured Design Notes

## 1. Goal

We want to build a local, production-shaped prototype of a managed coding-agent runtime.

The target developer experience is:

```bash
agentctl start
```

This starts an isolated agent session where the developer can keep chatting with a Claude Code-like agent. The agent runs inside a container, not directly on the developer's machine.

The agent should be able to:

- Run inside an isolated local container.
- Use the Claude Agent SDK / Claude Code runtime.
- Execute tools in permissionless mode inside the container.
- Access GitHub or other repos using injected credentials.
- Pull repositories on demand.
- Use preloaded MCPs and skills.
- Preserve session context across messages.
- Support both terminal and, later, web UI access.
- Keep `agentd` simple and avoid making it understand conversations.
- Support interrupting an in-flight turn and queueing follow-up messages without restarting the container.
- **(Phase 2)** Survive container/runner/`agentd`/host crashes without losing the session. Phase 1 treats container termination mid-turn as session loss; the user must `agentctl destroy` and start fresh. See §7 for the deferred recovery design.

For now, we are focusing only on the local solution.

---

## 2. Key Mental Model

The system has three main pieces:

```text
agentctl
  CLI used by the developer.

agentd
  Local daemon / orchestrator that manages sessions and containers.

agent-runner
  Process inside the session container that runs the Claude Agent SDK.
```

The most important architectural idea is:

```text
Session = durable volume
Container = temporary executor
Claude Agent SDK / Claude Code runtime = conversation manager
agent-runner = stateful turn executor (state machine + journal)
agentd = dumb orchestrator
```

`agentd` does not understand the conversation. It does not parse chat history, summarize messages, or reconstruct prompts. It manages session lifecycle, attaches the right volume to the right container, injects credentials, and forwards opaque opcodes (`send`, `interrupt`, `attach`, etc.) between `agentctl`/UI clients and the runner.

The runner does understand a small amount of *protocol-level* state (turn-in-progress markers, pending message queue, drain status) but not conversation semantics. It journals enough on the volume to recover from crashes mid-turn.

---

## 3. Local Architecture

```text
Developer Terminal / Web UI
   |
   | agentctl start / attach / send / interrupt
   | (or HTTP+WS to agentd)
   v
agentd
   |
   | starts/stops containers
   | mounts session volume
   | injects credentials and CLAUDE_CONFIG_DIR
   | multiplexes runner event stream to subscribers
   v
Session Container
   |
   | runs agent-runner
   v
agent-runner
   |
   | owns ClaudeSDKClient lifecycle
   | owns the per-session pending queue and current-turn marker
   | tees SDK events to .agent/transcript.jsonl + broadcast
   v
Claude Agent SDK
   |
   | shells out to `claude` CLI subprocess
   | persists conversation JSONL to $CLAUDE_CONFIG_DIR/projects/...
   | calls Anthropic / Vertex / LiteLLM
   | reads/writes /workspace
   | executes tools inside container
```

For the local prototype, Docker is the execution backend. Later, this can map to Kubernetes, where the session volume becomes a PVC and the container becomes a pod.

---

## 4. Main Commands

The desired local CLI interface:

```bash
agentctl start
agentctl start --detach
agentctl attach <session_id>
agentctl send <session_id> "message"
agentctl interrupt <session_id>
agentctl diff <session_id>
agentctl stop <session_id>
agentctl restart <session_id>
agentctl destroy <session_id>
agentctl status <session_id>
agentctl list
agentctl logs <session_id> [--turn <turn_id>] [--source agentd|runner|cli|all] [-f]
agentctl set-model <session_id> <model>
agentctl set-mode <session_id> acceptEdits|bypassPermissions|...
agentctl mcp reconnect <session_id> <name>
agentctl mcp toggle <session_id> <name> --enable|--disable
```

### `agentctl start`

Creates a new session, starts a container, and enters an interactive chat loop.

```text
Created session: sess_abc123
Workspace: ~/.agentd/sessions/sess_abc123/workspace
Container: agent-session-sess_abc123

agent[sess_abc123]>
```

### `agentctl attach <session_id>`

Reconnects to an existing session. Streams live events plus a backfill from the runner's transcript so the user is not missing anything that happened while detached.

### `agentctl send <session_id> "message"`

Sends one message to a session and exits after the response is complete (or after the message is queued, if a turn is in progress; see §6).

### `agentctl interrupt <session_id>`

Interrupts the in-flight turn. Returns when the runner has cleanly drained the SDK stream and the next turn (queued or new) can begin.

### `agentctl diff <session_id>`

Shows diffs across repositories cloned inside the workspace.

### `agentctl start --detach`

Same as `start`, but returns immediately after the container is up and the SQLite row is written; the runner stays alive without an attached client. Output prints session id and an `attach` hint. Interactive `start` (no `--detach`) also keeps the runner alive on Ctrl+D / disconnect; the user must `stop` or `destroy` explicitly.

### `agentctl stop <session_id>`

Stops the runtime container but keeps the session volume. Default behavior is graceful: issues `interrupt`, drains, then SIGTERMs the runner with a 30s deadline. `--force` skips drain and SIGKILLs immediately. **Phase 1 note:** because crash recovery is deferred (§7), `--force` is equivalent to session loss; the SDK's JSONL may be left inconsistent and the session is unrecoverable.

### `agentctl restart <session_id>`

Stops and re-spawns the runner against the same volume with `resume=session_id`. Required for any config change the SDK can't apply live (system_prompt, hooks, allowed_tools, skills, the MCP server set itself — see §13). Live switches (model, permission_mode, MCP reconnect/toggle) do not need this.

### `agentctl destroy <session_id>`

Stops the container and deletes the session volume. If `pending.jsonl` is non-empty, prompts: `Warning: N queued messages will be discarded. Continue? [y/N]`. On confirmation, writes `{"type": "destroyed_with_pending", "count": N}` to `transcript.jsonl` before deletion.

### `agentctl logs <session_id>`

Merges agentd, runner, and `claude` CLI subprocess stderr for the session, sorted by timestamp and prefixed by source + turn_id. `--turn` filters to one turn; `--source` selects a stream; `-f` follows. `--json` emits raw JSONL for `jq`. See §O1.

### `agentctl set-model` / `set-mode` / `mcp reconnect` / `mcp toggle`

Live config switches that do not require a restart. Backed by SDK control requests (`set_model`, `set_permission_mode`, `reconnect_mcp_server`, `toggle_mcp_server`). All other config changes need `agentctl restart`. See §13 and §O6.

### `agentctl status <session_id>` / `list`

Returns metadata: status, mode, current turn id (if any), queue depth, container state, cumulative cost (USD) and token usage.

---

## 5. Hot Mode vs Cold Mode

There are two execution modes. The volume design supports both; the runner protocol is identical.

### Hot Mode (MVP default)

The container and the `ClaudeSDKClient` stay alive between messages.

```text
Message 1
  -> container running
  -> ClaudeSDKClient.query(msg1)
  -> stream completes

Message 2
  -> same container
  -> same ClaudeSDKClient.query(msg2)
  -> in-memory SDK state and OS-level caches preserved
```

Pros: best UX, fast follow-ups, preserves SDK in-memory state, can preserve background processes.
Cons: holds resources while idle; needs an idle-timeout policy.

### Cold Mode

A new container starts for each message; the same session volume is reattached.

```text
Message 1
  -> start container, mount volume
  -> ClaudeSDKClient.query(msg1, resume=session_id)
  -> stream completes
  -> stop container

Message 2
  -> start new container, mount same volume
  -> ClaudeSDKClient.query(msg2, resume=session_id)
  -> SDK loads prior JSONL from volume, replays context to API
  -> stream completes
  -> stop container
```

Pros: lower idle resource usage, simpler scale-out, cleaner blast radius per turn.
Cons: every turn pays container cold-start; no preserved background processes; relies entirely on volume persistence.

### Mode interaction with queueing

If a follow-up message arrives while a turn is running:

- **Hot mode**: queue it in `.agent/pending.jsonl`; on `ResultMessage`, pop and call `query()` again on the same client. **Container is not stopped or restarted.**
- **Cold mode**: still queue it on the volume; the queue is drained either by the in-flight container before it shuts down, or by the next container that boots up. The SDK's `resume=` mechanism makes either path correct.

The MVP runs hot. Cold mode is an operational toggle, not a redesign.

---

## 6. Interrupt, Queue, and Drain Semantics

This section is normative. Get these right and the rest of the runtime falls out cleanly. Get them wrong and you corrupt session state.

### 6.1 SDK primitives we depend on

Verified from `claude-agent-sdk-python` source:

- `await client.interrupt()` — sends a stop signal to the in-flight turn. Returns immediately. The SDK still emits any messages that were already queued plus a final `ResultMessage` with `subtype="error_during_execution"`. Session/transcript stays consistent.
- The response stream is a sequential async iterator (`client.receive_response()`). It is **not** multiplexed: you cannot read and write concurrently, and you cannot start a new `query()` until the previous turn has reached `ResultMessage` and any post-interrupt buffered messages have been consumed.
- `query()` is single-shot per turn; **the SDK does not expose a queue API**. Queueing is a runner-side concern.

### 6.2 Drain

"Draining" means consuming the SDK's response stream until the end-of-turn `ResultMessage` so the buffer is empty before the next `query()`.

It applies in two situations:

1. **Normal end-of-turn**: read events to `ResultMessage`, that's drained naturally.
2. **Post-interrupt**: after `await client.interrupt()`, the stream may still have buffered events plus an error `ResultMessage`. Read them all (and tee them to the transcript with `interrupted=true`) before issuing the next `query()`. Skipping this step → next turn reads stale events → looks like session corruption.

Wrap it once in the runner so callers can't forget:

```python
async def _drain_until_result():
    async for msg in client.receive_response():
        await _emit_event(msg, interrupted=True)
        if isinstance(msg, ResultMessage):
            return
```

### 6.3 Queue

The queue lives on the volume at `/workspace/.agent/pending.jsonl`. It is an append-only FIFO of pending user messages. On `agentctl send` while the runner is `RUNNING`, the message is appended to the queue and acknowledged immediately with `{queued: true, position: N}`.

Queue persistence on the volume is required (not just in-memory) because cold-mode and crash-recovery flows must be able to drain pending messages from a fresh container.

Backpressure: bound the queue at, say, 10 messages. When full, `send` returns `{error: "queue_full", retry_after_seconds: ...}`. Avoids unbounded growth and surfacing model-output backlog to the user.

### 6.4 Runner state machine

```text
        ┌────────┐  send / pop
        │  IDLE  │ ────────────► RUNNING
        └────────┘                  │
            ▲                       │ ResultMessage (success)
            │                       ▼
            └─────────────────── (pop next from queue if any)
                                    │
                                    │ interrupt
                                    ▼
                                DRAINING
                                    │ ResultMessage (error_during_execution)
                                    ▼
                                  IDLE → pop next from queue
```

States:

- **IDLE**: no in-flight turn, no current_turn.json on disk. New `send` → either start a turn directly, or pop the head of `pending.jsonl` if non-empty.
- **RUNNING**: `client.query()` issued, `current_turn.json` written, events streaming. New `send` → append to `pending.jsonl`, ack with queue position. `interrupt` → call `client.interrupt()`, transition to DRAINING.
- **DRAINING**: post-interrupt; `_drain_until_result()` running. Reject new `send` with `{retry: true}` (or queue them; both are valid — pick one; recommended: queue, since it preserves the user's flow).

### 6.5 Atomicity of queue and turn marker

`pending.jsonl` is append-only; popping = reading the first line and rewriting the file (or maintaining a separate "head pointer" file). Use `os.replace()` for atomic swap on rewrite.

`current_turn.json` is written atomically before `query()` and deleted after `ResultMessage`. `tempfile.NamedTemporaryFile(dir=".agent") + os.replace()`.

### 6.6 Mandatory drain helper, not optional

The drain helper is the single most important invariant in the runner. Add a unit test that calls `interrupt()` followed by `query()` *without* draining and asserts that the runner detects the violation. This is the bug class we cannot afford to ship.

---

## 7. Crash Recovery and Runtime Journaling

> **Phase 1 scope: not implemented.** Phase 1 treats container/runner/host termination mid-turn as session loss. If the container dies during a turn, the user's path forward is `agentctl destroy <session>` and `agentctl start` afresh; the SDK's own JSONL on the volume may be left in an inconsistent state and we do not attempt to reconcile it. The design below — write-ahead `current_turn.json`, sub-case reconciliation, synthetic `tool_result` injection — is the **Phase 2** plan. It is preserved here because (a) the runtime journal (`transcript.jsonl`, `events.jsonl`) is still useful in Phase 1 for UI/audit, and (b) the recovery flow needs to be designed up front so Phase 2 doesn't require rewriting the runner. Until Phase 2: `current_turn.json` is **not** written; A5 collapses to "graceful drain only — `agentctl stop --force` is equivalent to session loss"; A10/A11 are deferred.

Crashes happen mid-turn: the container OOMs, the host reboots, the runner segfaults, the user kills `agentd`. Recovery needs to be deterministic — never relies on guessing what state the SDK was in.

### 7.1 The runtime journal

Three files under `/workspace/.agent/` form the journal:

| File | Owner | Purpose |
|---|---|---|
| `current_turn.json` | runner | Write-ahead marker. Present iff a `query()` is in-flight. Contains `{turn_id, started_at, user_msg_id, mode}`. |
| `pending.jsonl` | runner | Queued messages not yet processed. |
| `transcript.jsonl` | runner | Append-only log of every event observed by the runner from the SDK stream, plus runtime-protocol events (`queued`, `interrupted`, `crashed`, `recovered`). |

These are independent of the SDK's own JSONL at `/workspace/.claude/projects/-workspace/<session>.jsonl`. The SDK file is for *replay to the model*; the runner files are for *runtime correctness and human-facing observability*.

### 7.2 Recovery on runner boot

When `agent-runner` starts (cold mode, hot-mode crash, or `agentd` restart triggers re-attach):

```text
1. Read .agent/session.json for session_id and mode.
2. Initialize ClaudeSDKClient with:
     options.cwd = "/workspace"
     options.resume = session_id
     options.env["CLAUDE_CONFIG_DIR"] = "/workspace/.claude"
3. Check for current_turn.json:
   - If absent: state = IDLE, proceed to step 5.
   - If present: state = RECOVERING, proceed to step 4.
4. Reconcile the dangling turn:
   a. Read the SDK's JSONL tail: /workspace/.claude/projects/-workspace/<session>.jsonl
   b. Determine sub-case (see §7.3).
   c. Append a {type: "crashed", recovered_as: "<sub-case>"} event to transcript.jsonl.
   d. Take corrective action per sub-case.
   e. Delete current_turn.json. State = IDLE.
5. If pending.jsonl is non-empty, pop head and start a new turn. Else wait for input.
```

### 7.3 Crash sub-cases (the part most designs get wrong)

When `current_turn.json` exists, the SDK's JSONL tail tells us where the crash occurred:

| SDK JSONL tail at crash | Sub-case label | Corrective action |
|---|---|---|
| Last entry is the user message we sent (no assistant turn yet) | `pre_assistant` | Safe. Re-issue the same `query()` with the original user message on next turn (or treat the queued user message as the next turn's input — pick a policy and stick to it). Document choice. |
| Partial assistant message, no `tool_use` | `mid_assistant_no_tool` | The model produced text that the API thinks completed. Resume normally; the next `query()` with a new user message picks up from there. Append `{type: "crashed_mid_assistant"}` to transcript so the UI can show it. |
| `tool_use` entry present without matching `tool_result` | `dangling_tool_use` | **Dangerous.** The model believes a tool ran and is awaiting its result. The SDK on resume will replay this state to the API and the model will be confused. Mitigation: inject a synthetic tool result as the next message *before* any new user message. Concrete approach: write a `{role: "user", content: [{type: "tool_result", tool_use_id: "...", content: "Tool execution interrupted by container restart.", is_error: true}]}` entry via the SDK's session-mutation API (`claude_agent_sdk._internal.session_mutations`) before next `query()`. |

The recovery routine MUST handle all three; the third is the tricky one and the only reason `current_turn.json` exists.

### 7.4 What we do not try to recover

- **In-memory SDK state in hot mode** (e.g., recent thinking blocks not yet flushed): lost on crash, cannot be recovered. Acceptable.
- **Background processes started by tools**: lost on container exit. Acceptable; tools should be idempotent or checkpoint to disk if they run long.
- **Tool side-effects already executed before the crash** (e.g., a `Bash` that mutated `repos/`): visible on the volume, no rollback. Documented in `transcript.jsonl` via the tool events.

---

## 8. Session Volume

Every session has a durable workspace volume.

For local development we use a host-folder bind-mount, not a Docker named volume — easier to inspect and debug.

Default host path:

```text
~/.agentd/sessions/<session_id>/workspace
```

Mounted inside the container at:

```text
/workspace
```

The container is also given:

```bash
-e CLAUDE_CONFIG_DIR=/workspace/.claude
```

so that the Claude Agent SDK and the underlying `claude` CLI subprocess write their state onto the volume. (Verified from SDK source: `claude_agent_sdk._internal.sessions._get_claude_config_home_dir` reads `CLAUDE_CONFIG_DIR` first, falling back to `~/.claude`. See §10 for details.)

The invariant:

```text
If we have the session volume, we have the session.
```

That means the session must be resumable from the volume alone, even if:

- `agentd` restarts.
- The container exits.
- The host reboots.
- The CLI detaches.
- A turn was in-flight and crashed (recovered via §7).

---

## 9. Volume Layout (definitive)

```
/workspace/
├── CLAUDE.md                            # agent guidance (see §16)
├── .mcp.json                            # MCP servers config (§20)
│
├── .claude/                             # SDK + skill tree; redirected here via CLAUDE_CONFIG_DIR
│   ├── skills/                          # preloaded skills
│   │   └── repo-discovery/
│   │       └── SKILL.md
│   ├── agents/                          # optional: subagent configs
│   ├── commands/                        # optional: slash commands
│   ├── projects/
│   │   └── -workspace/                  # sanitized cwd (`/workspace` → `-workspace`)
│   │       └── <session_id>.jsonl       # SDK auto-writes turn-by-turn
│   ├── .credentials.json                # optional, SDK-managed if used
│   └── .claude.json                     # optional, SDK-managed if used
│
├── .agent/                              # runner-owned runtime journal
│   ├── session.json                     # {id, mode, model, created_at,
│   │                                    #  schema_version, runner_image,
│   │                                    #  image_digest, sdk_version,
│   │                                    #  cli_version}                 (§A6)
│   ├── pending.jsonl                    # queued user messages (FIFO)
│   ├── current_turn.json                # write-ahead marker (Phase 2 only — §7)
│   ├── transcript.jsonl                 # parallel event log for UI/audit (NDJSON)
│   ├── events.jsonl                     # tool-call timeline with timestamps
│   ├── usage.db                         # SQLite per-turn usage log     (§O5)
│   └── runs/
│       └── <turn_id>/
│           └── <event_id>.txt           # large payload spillovers
│
├── repos/                               # cloned on demand (§21)
├── notes/                               # agent-owned working notes
└── artifacts/                           # generated outputs, test reports, etc.
```

Note: there is no `.claude-home/`. Earlier drafts of this doc proposed it; it was based on a misunderstanding. The SDK uses `$CLAUDE_CONFIG_DIR` (or `~/.claude` when unset). We point that at `/workspace/.claude`, and skills + SDK projects coexist there. Single source for everything Claude-related.

---

## 10. Persistence Strategy: Three-Layer Ownership Model

State on the volume splits into three categories, each with one writer:

| Layer | What | Owner (writer) | Source of truth | Readers |
|---|---|---|---|---|
| Conversation | user msgs, assistant msgs, tool calls, tool results | Claude Agent SDK / `claude` CLI subprocess | `/workspace/.claude/projects/-workspace/<session_id>.jsonl` | SDK only (do not write to it from outside; read via `claude_agent_sdk` `list_sessions`/`get_session_messages` if needed) |
| Workspace | repos, edits, notes, artifacts | the agent (via tool calls) | git working trees in `/workspace/repos/*`; flat files in `/workspace/notes`, `/workspace/artifacts` | UI, `agentctl diff`, runner |
| Runtime | pending queue, current-turn marker, transcript, events | `agent-runner` | `/workspace/.agent/*` | runner (writer), UI (reader), recovery (reader) |

Three writers. Three non-overlapping path prefixes. No cross-writes.

### 10.1 Invariants enforced in code

1. `agentd` never writes to a session volume. It only mounts. (Lint: any `open(...)` under `~/.agentd/sessions/*/workspace/` from `agentd` code is a CI failure.)
2. `agent-runner` only writes inside `/workspace/.agent/`. Wrap the journal helpers and forbid `open()` on other prefixes from runner code.
3. The SDK's JSONL is read-only to everyone except the SDK. Reads use the SDK's documented helpers; never `open(... "a")`.

If those three hold, persistence is correct by construction.

### 10.2 Why dual transcript (SDK file + our `transcript.jsonl`)

The SDK file is the source of truth for *replay to the stateless API*. The runner's `transcript.jsonl` is the source of truth for *display, audit, search, queue-aware UI rendering*. They look redundant; they serve different masters.

The runner's transcript captures events the SDK file does not:

- `queued` (user message added to queue with position)
- `interrupted` (with `by: user|system`, `at_turn`, `at_event_offset`)
- `crashed` / `recovered` (with sub-case label)
- `turn_started` / `turn_complete` (with timing)

These are essential for the web UI (§22) and `agentctl attach` backfill. Trying to render them out of the SDK file alone is reverse-engineering its private schema.

The runner writes both directions: every SDK event it observes via `client.receive_response()` is teed (a) to `transcript.jsonl` for our consumers and (b) forwarded to live subscribers. The SDK independently writes to its own JSONL for replay. Two writes to two files; no cross-coordination needed.

---

## 11. The Conversation History Problem & SDK Configuration

Claude APIs are stateless. The model does not remember prior turns. Conversation continuity comes entirely from re-sending prior messages on each request.

This concern is handled by the Claude Agent SDK and the underlying `claude` CLI subprocess. We do not implement message-history reconstruction. We:

1. Use a stable `session_id` per session.
2. Set `CLAUDE_CONFIG_DIR=/workspace/.claude` so the SDK's session JSONL lives on the volume.
3. Pass `resume=session_id` on every `query()` so the SDK loads the JSONL and replays context.

### 11.1 Source-verified mechanism (`claude-agent-sdk-python`)

- `_internal/sessions.py:122-140` — `_get_claude_config_home_dir()` reads `os.environ["CLAUDE_CONFIG_DIR"]` first, falls back to `Path.home() / ".claude"`. `_get_projects_dir(env_override)` checks `env_override["CLAUDE_CONFIG_DIR"]` first, then the env, then home.
- `_internal/transport/subprocess_cli.py:430-434` — the `claude` CLI subprocess inherits `os.environ` plus `options.env` (with `options.env` overriding inherited values). `CLAUDE_CONFIG_DIR` propagates either way.
- `_internal/session_resume.py` — the SDK's own session-store plugin system materializes a temp directory shaped like `~/.claude/` and points the subprocess at it via `CLAUDE_CONFIG_DIR`. This confirms `CLAUDE_CONFIG_DIR` is the official knob, not a workaround.
- `tests/test_session_resume.py:192` — explicit comment: *"options.env CLAUDE_CONFIG_DIR takes precedence over ~ lookup."*

### 11.2 Resume API

```python
options = ClaudeAgentOptions(
    cwd="/workspace",
    resume=session_id,                                       # the same id as last turn
    env={"CLAUDE_CONFIG_DIR": "/workspace/.claude"},         # optional if set as container env
    permission_mode="bypassPermissions",
    setting_sources=["project"],
    skills="all",
    mcp_servers="/workspace/.mcp.json",
    include_partial_messages=True,
    enable_file_checkpointing=True,
)
async with ClaudeSDKClient(options=options) as client:
    await client.query(user_message)
    async for msg in client.receive_response():
        ...
```

On resume, the SDK reads `/workspace/.claude/projects/-workspace/<session_id>.jsonl`, computes the relevant context window, and includes prior turns when calling the API. No additional work in `agentd` or the runner.

### 11.3 Forking

`ClaudeAgentOptions(fork_session=True)` creates a new session_id branching from the current state. Useful for what-if reruns. Original session JSONL is not mutated.

### 11.4 SessionStore plugin (deferred)

The SDK supports a pluggable `SessionStore` protocol with built-in examples (`examples/session_stores/{redis,postgres,s3}_session_store.py`). The subprocess still writes to local disk; the store is a mirror channel. **For the local prototype we do not use this.** It is the path forward for cloud deployments where session JSONL needs to live in a managed DB.

---

## 12. agentd Should Not Understand Conversations

We want a clean boundary:

```text
agentd:
  - knows session id
  - knows volume host path
  - knows container status, container id, runner port
  - injects credentials (ANTHROPIC_API_KEY, GITHUB_TOKEN, etc.)
  - injects CLAUDE_CONFIG_DIR=/workspace/.claude
  - starts/stops containers
  - multiplexes runner event streams to subscribers (CLI, web UI)
  - persists session metadata in SQLite

agentd does NOT:
  - parse conversation
  - summarize conversation
  - reconstruct prompts
  - inspect SDK transcript
  - understand tools or messages
  - store conversation content in SQLite
```

The conversation lives on the volume (SDK JSONL). The runtime journal lives on the volume (`.agent/`). `agentd` is a session lifecycle daemon plus an event broker.

The runner *does* understand a small amount of state, but only protocol-level (queue, current turn, drain status), not conversation-level.

---

## 13. Runner Responsibility & State Machine

`agent-runner` runs inside the container and is the sole owner of `.agent/`.

### 13.1 Responsibilities

- Initialize `ClaudeSDKClient` with stable session_id, `cwd=/workspace`, `resume=session_id`, and `CLAUDE_CONFIG_DIR=/workspace/.claude`.
- Use `permission_mode="bypassPermissions"` (safe because the container is the security boundary; see §19).
- Manage the runner state machine (§6.4): `IDLE → RUNNING → DRAINING → IDLE`.
- Maintain the runtime journal:
  - Write `current_turn.json` before each `query()`; delete on `ResultMessage`. **(Phase 2 only — see §7 callout.)**
  - Append-only writes to `transcript.jsonl` and `events.jsonl` (NDJSON, monotonic `offset` per event).
  - Manage `pending.jsonl` queue with atomic head-pop semantics.
  - Append per-turn usage (`total_cost_usd`, input/output/cache tokens) to `.agent/usage.db` on every `ResultMessage` (§O5).
- Tee every SDK event to `transcript.jsonl` AND broadcast to subscribers (the CLI/UI multiplexer in `agentd`). A single broadcaster task owns the SDK iterator: it durably writes the event before fanning out to per-subscriber bounded queues (§22.3).
- Recover on boot per §7.2. **(Phase 2 only.)**
- Honor `interrupt`, `send`, `attach`, `status`, `restart`, and live-switch opcodes (`set_model`, `set_permission_mode`, `mcp_reconnect`, `mcp_toggle`) from `agentd`. The four live switches map directly to SDK control requests (`client.set_model`, `client.set_permission_mode`, `client.reconnect_mcp_server`, `client.toggle_mcp_server`); everything else (`system_prompt`, `hooks`, `allowed_tools`, `skills`, the MCP server set) requires `agentctl restart`. See §O6.
- Stream events with monotonic offsets so resumable subscriptions work (§22.2).
- Capture the `claude` CLI subprocess stderr via `ClaudeAgentOptions(stderr=...)` and forward to `.agent/cli-stderr.log` with `turn_id` correlation (§O1).

### 13.2 Single-runner invariant per session (§A1)

At most one container/runner exists for a given `session_id`. `agentd` enforces this with two layers:

1. **SQLite leader-election token.** The `sessions` table has a partial UNIQUE index on `(session_id) WHERE status='running'`. `start` is an atomic `INSERT ... WHERE NOT EXISTS (SELECT 1 FROM sessions WHERE id=? AND status='running')`. Loser: returned a hint to `attach` instead.
2. **Filesystem lock as defense-in-depth.** `flock` on `~/.agentd/sessions/<id>/.lock` held by the running `agentd` for the duration of the container's life. Survives `agentd` crashes via `flock`'s automatic release on FD close.

This is the only mechanism guarding against `agentctl start sess_X` racing into two containers, and against two `send`s landing in different containers. Test by spawning two `start sess_X` simultaneously and asserting exactly one container exists.

### 13.3 What the runner does NOT do

- Does not parse conversation content.
- Does not modify the SDK's JSONL.
- Does not write outside `.agent/` (except via tools the agent runs, which write to `repos/`/`notes/`/`artifacts/` — but those are SDK/agent writes, not runner writes).
- Does not understand tools beyond their event envelope.

### 13.4 Runner ↔ agentd protocol

**Framing: NDJSON over Unix socket.** One JSON object per `\n`. Same shape on the wire, in `transcript.jsonl`, and on the WebSocket — chosen so `tail -f transcript.jsonl | jq` works and replay-from-disk is byte-identical to live stream. Length-prefixed framing was rejected (events are <4 KB and text-friendly); CBOR was rejected (loses `jq`).

Opcodes:

```text
client → runner:
  send {user_msg_id, content}
  interrupt
  attach {since_offset?}
  status
  set_model {model}
  set_permission_mode {mode}
  mcp_reconnect {name}
  mcp_toggle {name, enabled}

runner → client:
  ack {opcode_id, result}
  event {offset, type, turn_id, ts, payload}   # streamed, NDJSON
  error {code, message}
```

Events forwarded to the multiplexer (which fans out to CLI and web UI subscribers) carry offsets so subscribers can resume from a known point. Multiplexer fan-out semantics: per-subscriber `asyncio.Queue(maxsize=256)`; `put_nowait` only — never await a subscriber. Slow consumer → close the socket and force reconnect with `?since=<last_offset>` (backfilled from `transcript.jsonl`). This is the Kafka/NATS pattern: the durable log is truth, the live stream is best-effort. Blocking the SDK iterator on a slow UI is the failure mode this design exists to prevent.

---

## 14. Example Runner Options

```python
import os
from claude_agent_sdk import ClaudeAgentOptions, ClaudeSDKClient

session_id = os.environ["AGENT_SESSION_ID"]   # injected by agentd

options = ClaudeAgentOptions(
    cwd="/workspace",
    resume=session_id,
    permission_mode="bypassPermissions",
    setting_sources=["project"],
    skills="all",
    mcp_servers="/workspace/.mcp.json",
    include_partial_messages=True,
    enable_file_checkpointing=True,
    env={
        # CLAUDE_CONFIG_DIR is also set as a container env var for redundancy;
        # explicit options.env wins over inherited env per subprocess_cli.py:430-434.
        "CLAUDE_CONFIG_DIR": "/workspace/.claude",
        "ANTHROPIC_BASE_URL": os.environ.get("ANTHROPIC_BASE_URL", ""),
        "ANTHROPIC_AUTH_TOKEN": os.environ.get("ANTHROPIC_AUTH_TOKEN", ""),
        "ANTHROPIC_API_KEY": os.environ.get("ANTHROPIC_API_KEY", ""),
        "ANTHROPIC_MODEL": os.environ.get("ANTHROPIC_MODEL", ""),
        "GITHUB_TOKEN": os.environ.get("GITHUB_TOKEN", ""),
    },
)

async with ClaudeSDKClient(options=options) as client:
    # state machine loop driven by socket opcodes
    ...
```

The container env is set via `docker run -e CLAUDE_CONFIG_DIR=/workspace/.claude ...` so any auxiliary process (e.g., a future `agentctl exec` that runs `claude` directly inside) inherits it for free.

---

## 15. CLAUDE.md

Each session volume contains a `CLAUDE.md` file at the workspace root, loaded automatically by the SDK because `setting_sources=["project"]` is set.

```markdown
# Agent Workspace Instructions

You are running inside an isolated agent workspace.

The session state is stored in this workspace:

- `/workspace/.agent/transcript.jsonl`: parallel event log (do not edit)
- `/workspace/.agent/state.md`: current session state (you may update)
- `/workspace/.agent/summary.md`: concise progress summary (you may update)
- `/workspace/repos`: cloned repositories
- `/workspace/notes`: working notes
- `/workspace/artifacts`: generated artifacts

Rules:

1. Treat `/workspace` as the source of truth.
2. Clone repositories only under `/workspace/repos`.
3. Do not write outside `/workspace`.
4. Do not write to `/workspace/.claude/` or `/workspace/.agent/transcript.jsonl` or `/workspace/.agent/events.jsonl` (runtime-owned).
5. Before modifying code, identify the target repository.
6. After modifying code, run the smallest relevant test.
7. At the end of meaningful work, update `/workspace/.agent/state.md` and `/workspace/.agent/summary.md`.
```

This file is intentionally small. Conversation replay is handled by the SDK, not by the agent re-reading its own transcript.

---

## 16. SQLite for `agentd`

`agentd` uses SQLite for metadata only. No conversation content lives here.

During onboarding (`agentd init`):

```text
~/.agentd/
  config.toml
  agentd.db
  sessions/
```

Schema:

```sql
CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    status TEXT NOT NULL,                 -- starting|running|stopped|destroyed
    mode TEXT NOT NULL,                   -- hot|cold

    workspace_path TEXT NOT NULL,         -- ~/.agentd/sessions/<id>/workspace
    container_name TEXT,
    container_id TEXT,
    runner_port INTEGER,

    provider TEXT,                        -- anthropic|vertex|litellm
    model TEXT,

    last_turn_id TEXT,                    -- mirrors current_turn.json; null when IDLE
    queue_depth INTEGER NOT NULL DEFAULT 0,

    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    last_active_at TEXT
);

CREATE INDEX idx_sessions_status ON sessions(status);
```

`last_turn_id` and `queue_depth` are mirrors of state-on-volume so `agentctl status` can answer without inspecting the workspace. The volume remains source of truth on conflict.

---

## 17. `agentd` Onboarding

```bash
agentd init
```

Prompts:

```text
Where should agentd store data? [~/.agentd]
```

Derives:

```toml
[storage]
root_dir = "/Users/vipul/.agentd"
db_path = "/Users/vipul/.agentd/agentd.db"
sessions_dir = "/Users/vipul/.agentd/sessions"

[runtime]
container_runtime = "docker"
runner_image = "agent-runner:local"
default_session_mode = "hot"
idle_container_timeout_seconds = 1800

[llm]
provider = "litellm"
base_url = "http://host.docker.internal:4000"
default_model = "claude-sonnet-4-5"

[github]
token_source = "env"
token_env = "GITHUB_TOKEN"

[queue]
max_pending_per_session = 10
```

---

## 18. Credentials and Permissions

### 18.1 Local MVP

Inject into the container at session runtime; never bake into the image:

```bash
docker run \
  --user "$(id -u):$(id -g)" \
  -e HOME=/home/agent \
  -e ANTHROPIC_API_KEY="$ANTHROPIC_API_KEY" \
  -e ANTHROPIC_BASE_URL="$ANTHROPIC_BASE_URL" \
  -e ANTHROPIC_AUTH_TOKEN="$ANTHROPIC_AUTH_TOKEN" \
  -e GITHUB_TOKEN="$GITHUB_TOKEN" \
  -e CLAUDE_CONFIG_DIR="/workspace/.claude" \
  -e AGENT_SESSION_ID="$session_id" \
  -v "$workspace_host_path:/workspace" \
  agent-runner:v0.1.0@sha256:...
```

`--user $(id -u):$(id -g)` makes files written under `/workspace` host-readable on Linux (no sudo to inspect or `git` the workspace). On macOS Docker Desktop the gRPC-FUSE layer remaps ownership anyway so it's a no-op there, but harmless. The runner image must be built to support arbitrary runtime UIDs (OpenShift-style): pre-create `/workspace` and `/home/agent/.claude` world-writable; do not rely on `/etc/passwd` having an entry for the runtime UID. See §A4. The image reference uses an immutable digest pinned by `agentd` at session creation (§A6) so an upstream re-tag cannot silently change a running session's runtime.

`ANTHROPIC_API_KEY` alone is sufficient to bootstrap the bundled `claude` CLI inside the container; no `.claude.json` / `.credentials.json` seeding required. The OAuth + Keychain path used by interactive `claude /login` is orthogonal and intentionally not wired here (and is disallowed for SDK-based products by Anthropic's ToS). Do **not** bind-mount the host's `~/.claude` — mixing host OAuth state with API-key auth causes UID-mismatch failures on `.credentials.json`. For a gateway path, set `ANTHROPIC_BASE_URL` + `ANTHROPIC_AUTH_TOKEN`; for Bedrock/Vertex/Foundry, set `CLAUDE_CODE_USE_BEDROCK=1` / `CLAUDE_CODE_USE_VERTEX=1` / `CLAUDE_CODE_USE_FOUNDRY=1` plus the relevant cloud creds in place of `ANTHROPIC_API_KEY`.

### 18.2 Future production pattern

```text
Session container
  -> calls LiteLLM/internal gateway using a short-lived session token

agentd / gateway
  -> owns Anthropic/Vertex credentials
  -> enforces budgets, rate limits, model policy
  -> mints session-scoped tokens with TTL

GitHub
  -> GitHub App installation tokens, not developer PATs
```

### 18.3 Container-as-boundary

`permission_mode="bypassPermissions"` is safe *because the container is the security boundary*. The agent cannot escape `/workspace` because nothing else is mounted writable. This must hold:

- Only `/workspace` is bind-mounted writable.
- `/var/run/docker.sock` is **not** mounted (no Docker-in-Docker escape).
- The container runs without `--privileged`.
- Network egress goes through the LLM/internal gateway only (future work; MVP allows direct egress).

---

## 19. MCPs and Skills

### 19.1 Skills

Stored in `/workspace/.claude/skills/`. The SDK's `skills="all"` option discovers and loads them.

Example:

```text
/workspace/.claude/skills/repo-discovery/SKILL.md
```

The session volume is seeded with a default set at session creation by `agentctl start` (copied from a template directory, e.g. `~/.agentd/templates/skills/`).

### 19.2 MCP config

Stored in `/workspace/.mcp.json`. Empty config for first version:

```json
{ "mcpServers": {} }
```

Later: GitHub, repo catalog, Sentry, Grafana, etc. The web UI can offer per-session toggles. For local MVP, static per-session config seeded at `agentctl start`.

**Where stdio MCPs run.** The Python runner spawns the `claude` Node CLI as a subprocess, and the CLI in turn forks each stdio MCP. Process tree per session: `python runner` → `claude` (Node CLI) → `npx @modelcontextprotocol/server-github`, `sentry-mcp`, etc. SDK-type MCPs (`"type": "sdk"`) run in-process inside the Python runner via the SDK control protocol; we do not use those in MVP.

**Per-MCP credential injection.** `McpStdioServerConfig` accepts a literal `env` dict, and `.mcp.json` supports `${VAR}` expansion at CLI load time. Inject secrets as env vars on the runner container; reference them in `.mcp.json`. Never bake tokens into the file on the volume.

```jsonc
// /workspace/.mcp.json — references, never literals
{
  "mcpServers": {
    "github": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-github"],
      "env": { "GITHUB_TOKEN": "${GITHUB_TOKEN}" }
    },
    "sentry": {
      "command": "sentry-mcp",
      "env": { "SENTRY_TOKEN": "${SENTRY_TOKEN}" }
    }
  }
}
```

**Crash handling.** No automatic retry. The SDK exposes `McpServerConnectionStatus` (`connected | failed | needs-auth | pending | disabled`) inside the `init` SystemMessage and via `client.get_mcp_status()`. A failed MCP does not abort the turn — its tools become unavailable. Recovery is explicit: `agentctl mcp reconnect <session> <name>` (calls `client.reconnect_mcp_server`) or `agentctl mcp toggle <session> <name>` (calls `client.toggle_mcp_server`). The runner surfaces all status changes as transcript events for the UI.

**Sidecar vs in-container.** MVP runs all stdio MCPs in the same container as the runner. Move an MCP to a sidecar only when it needs (a) different network egress rules from the agent, (b) GPU or heavy deps you don't want in the runner image, or (c) shared state across sessions. None apply for the MVP MCP set.

See §A7.

---

## 20. Repositories Pulled on Demand

Sessions are workspace-scoped, not repo-scoped:

```text
Workspace session
  may contain zero, one, or many repositories
```

Repos go under `/workspace/repos/`. The agent clones what it needs via `Bash` tool calls; we don't preload.

This supports cross-repo tasks like investigating a checkout failure that spans `ads-service`, `payment-service`, `checkout-service`, `frontend-web`.

`GITHUB_TOKEN` is injected so `git clone` against private repos works without interactive auth.

---

## 21. Diff Handling

Source of truth for code changes is the git working tree in each cloned repo.

`agentctl diff <session_id>` (and the equivalent web UI endpoint) walks `/workspace/repos/*` and runs:

```bash
git -C /workspace/repos/<repo> status --short
git -C /workspace/repos/<repo> diff
```

Output is grouped by repo. The database stores no source files or diffs; SQLite is metadata only.

---

## 22. Web UI Architecture

The web UI is a thin client of `agentd`. The volume is never accessed directly by browsers.

### 22.1 `agentd` HTTP+WS surface

```text
GET    /sessions                          list sessions (from SQLite)
POST   /sessions                          create new session (= agentctl start)
GET    /sessions/:id                      session metadata
GET    /sessions/:id/messages?since=N     paginated history (reads transcript.jsonl)
GET    /sessions/:id/diff                 grouped repo diffs (= agentctl diff)
WS     /sessions/:id/stream?since=N       live event stream
POST   /sessions/:id/messages             send a user message
POST   /sessions/:id/interrupt            interrupt current turn
DELETE /sessions/:id                      destroy
```

Each endpoint is a thin re-skin of the same runner protocol that powers `agentctl`. CLI and web UI become independent clients of one daemon.

### 22.2 Event offsets and seamless backfill

Every line in `transcript.jsonl` carries a monotonically increasing integer offset. The web UI flow:

1. `GET /sessions/:id/messages?since=0` — load full history (paginated).
2. Open `WS /sessions/:id/stream?since=<last_seen_offset>`.
3. `agentd` backfills any events between `last_seen_offset` and the current live position, then streams onward.

No gaps, no duplicates, even with reconnects.

### 22.3 Multiplexer

`agentd` multiplexes the runner's single event stream to multiple subscribers (CLI attach, web UI tabs, future services).

- A single broadcaster task in the runner owns the SDK iterator. For each event: (1) durably append to `transcript.jsonl`, (2) `put_nowait` onto each per-subscriber queue. Never await a subscriber.
- Per-subscriber queue: `asyncio.Queue(maxsize=256)` (~30s of events at typical rates, ~2.5 MB worst-case RAM at 10 KB/event). Raise to 1024 only if legitimate spikes are observed.
- Overflow policy: **drop and force reconnect.** The runner closes the offending subscriber's socket; the client reconnects with `?since=<last_offset>` and backfills from `transcript.jsonl`. Block-with-timeout was rejected (risks stalling the SDK iterator and the entire turn); spill-to-disk was rejected (`transcript.jsonl` already exists for that purpose).
- Live events are appended to `transcript.jsonl` *and* broadcast in the same step (write first, then broadcast — durability before fan-out).
- Heartbeat every 10s on the WS surface so dead clients are reaped quickly.

Precedents: NATS slow-consumer detection disconnects past `pending_limit`; Kafka decouples producers from consumers via the durable log + offsets; Jupyter's iopub channel drops to slow frontends. LSP's synchronous JSON-RPC is the explicit anti-pattern here. See §O2.

### 22.4 Event schema (consumer-facing)

The runner emits typed events to `transcript.jsonl`. Consumers (web UI, CLI) render based on `type`:

```jsonl
{"offset":1,"type":"user_message","turn_id":"t1","ts":"...","content":"..."}
{"offset":2,"type":"assistant_text","turn_id":"t1","ts":"...","content":"..."}
{"offset":3,"type":"tool_use","turn_id":"t1","ts":"...","tool":"Read","input":{...},"tool_use_id":"tu_1"}
{"offset":4,"type":"tool_result","turn_id":"t1","ts":"...","tool_use_id":"tu_1","content_ref":"runs/t1/e4.txt","size":48201,"is_error":false}
{"offset":5,"type":"thinking","turn_id":"t1","ts":"...","content":"..."}
{"offset":6,"type":"turn_complete","turn_id":"t1","ts":"...","stop_reason":"end_turn","duration_ms":12345}
{"offset":7,"type":"queued","ts":"...","queue_position":1,"user_msg_preview":"..."}
{"offset":8,"type":"interrupted","turn_id":"t2","ts":"...","by":"user"}
{"offset":9,"type":"crashed","ts":"...","recovered_as":"dangling_tool_use"}
```

This schema is owned by the runner. Versioning: include `"schema_version": 1` in `session.json`; bump when the event schema changes incompatibly.

### 22.5 Large payloads

Tool results can be large (file reads, command outputs). To keep `transcript.jsonl` scannable:

- Inline payloads under 8 KB directly in the event.
- Larger payloads → write to `.agent/runs/<turn_id>/<event_id>.{txt,json}` and put `{"content_ref": "runs/t1/e4.txt", "size": <bytes>}` in the event.
- UI fetches blob on expand via `GET /sessions/:id/blob/<turn_id>/<event_id>`.

This keeps history loads fast even for sessions with hundreds of file reads.

---

## 23. Session Restart Flow (cold mode reference)

When a new message arrives for a stopped session:

```text
 1. agentd receives `send` for sess_123.
 2. agentd looks up sess_123 in SQLite → status=stopped, workspace_path, mode=cold.
 3. agentd buffers the new user message in memory (NOT in pending.jsonl;
    only the runner writes .agent/ — see §10 ownership invariants).
 4. agentd starts a new container with:
       --user $(id -u):$(id -g)
       -v <workspace>:/workspace
       -e CLAUDE_CONFIG_DIR=/workspace/.claude
       -e AGENT_SESSION_ID=sess_123
       -e ANTHROPIC_API_KEY=... etc.
 5. Runner boots:
    a. Reads .agent/session.json (incl. sdk_version / cli_version pin check).
    b. Initializes ClaudeSDKClient with cwd=/workspace, resume="sess_123".
    c. (Phase 2) Checks for current_turn.json:
        - if present, runs §7.2 recovery before doing anything else.
    d. Opens the runner socket; agentd forwards buffered `send`s.
       Runner appends each to pending.jsonl, then drains: pops head,
       calls client.query(...).
 6. SDK (via the `claude` CLI subprocess) reads
    /workspace/.claude/projects/-workspace/sess_123.jsonl, replays prior
    context, sends new message + history to the API.
 7. Streamed events fan out: SDK appends to its JSONL; runner tees to
    .agent/transcript.jsonl + broadcasts to subscribers.
 8. On ResultMessage: delete current_turn.json. If queue still has items, pop
    next; else go IDLE.
 9. Container stops (cold mode) or stays alive (hot mode) per policy.
```

The key invariant:

```text
Claude API is stateless.
Claude Agent SDK / Claude Code runtime persists and reloads the session transcript.
agent-runner persists and reloads queue + recovery state.
agentd remembers nothing about conversation content.
```

---

## 24. What Should Be Built First

Smallest viable local prototype, in order:

### Step 1 — `agent-runner` Docker image

- Python 3.11+
- `claude-agent-sdk` (pinned to a known version)
- `claude` CLI (npm install `@anthropic-ai/claude-code`)
- Git, ripgrep, jq, curl
- Node/npm

### Step 2 — `agentd init`

- Prompt for root path.
- Create `config.toml`, `agentd.db`, `sessions/`.
- Create `~/.agentd/templates/{skills,mcp}` for seeding new sessions.

### Step 3 — `agentctl start` (no queue, no interrupt yet)

- Allocate session_id; create workspace folder.
- Seed: `CLAUDE.md`, `.mcp.json`, copy skills into `.claude/skills/`, write `.agent/session.json`.
- Insert SQLite row.
- `docker run` with proper env (incl. `CLAUDE_CONFIG_DIR=/workspace/.claude`).
- Open interactive REPL.

### Step 4 — Single-turn `send` + `attach`

- Wire runner ↔ agentd socket protocol.
- Implement `IDLE → RUNNING → IDLE` happy path.
- Stream events to `transcript.jsonl` and to the CLI.
- **Verify resume works**: stop container, send another message in a new container, confirm prior turn is in context. Test with the smoke test in §25.

### Step 5 — Queue + interrupt + drain

- Add `pending.jsonl` and `current_turn.json`.
- Implement the full state machine (§6.4).
- Implement `_drain_until_result()` helper with a unit test that fails if drain is skipped.

### Step 6 — Crash recovery

- Implement `recover()` on runner boot with all three sub-cases (§7.3).
- Test by killing the container mid-turn and restarting.

### Step 7 — `agentctl diff`

- Walk `/workspace/repos/*`, run `git status --short` and `git diff` per repo, group output.

### Step 8 — `agentd` HTTP+WS surface

- Implement endpoints from §22.1.
- Implement multiplexer with bounded per-subscriber queues.

### Step 9 — Web UI

- Minimal React client: list sessions, open one, render `transcript.jsonl`, send messages, interrupt button, diff view.

### Step 10 — Production hardening (out of MVP scope)

- Token broker, GitHub App tokens, network egress restrictions, idle timeouts, resource limits, observability.

---

## 25. Open Questions (and verification plan)

Open questions are grouped by impact: architectural questions affect correctness or break the §10 ownership boundaries; operational questions affect day-to-day usability; future questions are deferred to cloud/production phases; minor questions can be settled in implementation.

### 25.1 Resolved in this iteration

- ~~Q1 Exact Python SDK options for specifying Claude config/session directory~~ → `ClaudeAgentOptions(cwd="/workspace", resume=session_id, env={"CLAUDE_CONFIG_DIR": "/workspace/.claude"})`.
- ~~Q2 Resume behavior with stable session_id~~ → `resume=session_id` on options; SDK loads JSONL automatically.
- ~~Q3 Whether `CLAUDE_CONFIG_DIR` is the correct env var~~ → **yes**, source-verified at `claude_agent_sdk/_internal/sessions.py:122-140`.
- ~~Q4 How much session state the SDK persists automatically~~ → full transcript: user/assistant/tool_use/tool_result/thinking. JSONL at `<CLAUDE_CONFIG_DIR>/projects/<sanitized-cwd>/<session_id>.jsonl`.

### 25.2 Architectural — affect correctness or boundaries

These must be resolved before or during Step 5 of §24 ("Queue + interrupt + drain") because each can break the runner state machine, the ownership invariants in §10, or the recovery flow in §7.

| # | Question | Default lean | Verification plan |
|---|---|---|---|
| A1 | Single-runner invariant per session — what prevents two `agentctl start`s with the same session_id, or two `send`s racing into different containers? | **Resolved.** Two-layer enforcement (§13.2): (1) SQLite partial UNIQUE index on `(session_id) WHERE status='running'` for atomic leader election; (2) `flock` on `~/.agentd/sessions/<id>/.lock` as defense-in-depth (auto-released on agentd crash via FD close). New `start` for an active session is rejected with an attach hint. | Concurrency test: spawn two `agentctl start sess_X` simultaneously; assert exactly one container exists and the loser is told to attach. |
| A2 | `pending.jsonl` writer ownership — §23 step 3 has `agentd` writing the queue file directly, but §10 says only the runner writes to `.agent/`. The doc currently contradicts itself. | **Resolved.** Only the runner writes `pending.jsonl`. When no container exists, `agentd` buffers `send`s in memory and forwards them on runner connect (§23 step 3 updated). Preserves §10 invariants. | Lint at Step 5: forbid any `open(...)` under `<workspace>/.agent/` from agentd code as a CI failure. |
| A3 | `claude` CLI auth bootstrap inside Linux containers — the CLI normally uses OAuth + macOS Keychain via `claude /login`. Inside Linux, no Keychain. Is `ANTHROPIC_API_KEY` alone sufficient end-to-end, or must `.claude.json` / `.credentials.json` be pre-seeded? | **Resolved: API key only.** Confirmed by the Agent SDK docs (`code.claude.com/docs/en/agent-sdk/overview`): setting `ANTHROPIC_API_KEY` is the entire bootstrap; no `.claude.json` / `.credentials.json` pre-seed, no host `~/.claude` mount. Use `ANTHROPIC_BASE_URL` + `ANTHROPIC_AUTH_TOKEN` for the LiteLLM/gateway path (§18.2). For third-party providers, set `CLAUDE_CODE_USE_BEDROCK=1` / `CLAUDE_CODE_USE_VERTEX=1` / `CLAUDE_CODE_USE_FOUNDRY=1` plus the relevant cloud creds. Do **not** mount the host's `~/.claude` — it mixes OAuth state with API-key auth and triggers UID-mismatch failures on `.credentials.json` (see claude-code GH issue #22066). Note: Anthropic ToS forbids using `claude.ai` login auth in SDK-based products, so API-key is the only sanctioned path anyway. | Smoke test still wise: `docker run` with only `ANTHROPIC_API_KEY` set, exercise a one-shot `query()`. Confirms the bundled CLI doesn't unexpectedly require a writable `~/.claude` for API-key auth. Expected to pass on first try. |
| A4 | Container user / file ownership on bind-mount — root inside the container → root-owned files on the host volume → host user can't read workspace without sudo. | **Resolved.** OpenShift-style arbitrary-UID image: pre-create `/workspace` and `/home/agent/.claude` world-writable; build with a fixed `agent` user (UID 1000) but run with `--user $(id -u):$(id -g)`. macOS Docker Desktop's gRPC-FUSE remaps ownership anyway (no-op there); Linux native bind needs the flag. Rejected: per-developer image rebuilds (defeats shared artifact); rootless Docker (no parity on macOS). See §18.1. | Test on a fresh macOS + Linux dev box: host user can `cat`, `grep`, `git -C /workspace/repos/...` without sudo. Cite: [VS Code: Add a non-root user](https://code.visualstudio.com/remote/advancedcontainers/add-nonroot-user). |
| A5 | `agentctl stop` mid-turn semantics — graceful (interrupt + drain + SIGTERM) or hard (SIGKILL, recover via §7 next boot)? | **Phase 1: graceful only**, 30s deadline, SIGKILL on deadline. `agentctl stop --force` is supported but **equivalent to session loss in Phase 1** since crash recovery is deferred (§7 callout). Phase 2 lifts this: hard kill becomes recoverable. | Implement and test graceful path in Step 5. Test `--force` path in Phase 2 alongside §7 recovery. |
| A6 | SDK / `claude` CLI version pinning + JSONL forward-compat — can runner image v2 (newer SDK) read a JSONL written by v1? | **Resolved.** Pin exact SDK + CLI versions in the Dockerfile (no caret). SDK changelog confirms changes are additive with preserved fields for forward compat (v0.1.40, v0.1.51); newer reading older is supported, the reverse is not. Stamp `runner_image`, `image_digest`, `sdk_version`, `cli_version` into `.agent/session.json` at creation. On resume, if the running container's labels differ at SDK minor version, **refuse resume** and offer `--fork` (uses SDK `fork_session` to branch from the transcript without mutating the original). Watch out for [#555](https://github.com/anthropics/claude-agent-sdk-python/issues/555): a silent `session_id` rewrite on resume — `agentd` must hard-fail on requested-vs-returned mismatch. | Smoke test resume across pinned versions at Step 4. Resume across a deliberate SDK minor bump and assert the fork prompt fires. |
| A7 | MCP server lifecycle inside the container — do MCPs run inside the runner container or as sidecars? Per-MCP credential injection? Restart policy if an MCP crashes mid-turn? | **Resolved.** Process tree: `python runner` → `claude` CLI (Node) → stdio MCP child. The Python runner does **not** spawn stdio MCPs directly; the Node CLI does. SDK-type (`"type": "sdk"`) MCPs would run in-process but we don't use those in MVP. No auto-retry; failed MCPs surface as `McpServerConnectionStatus="failed"` in the `init` SystemMessage and via `client.get_mcp_status()`. Recovery: explicit `agentctl mcp reconnect|toggle`. Credential injection via `${VAR}` expansion in `.mcp.json` + env vars on the container — never literal tokens on disk. Sidecars only when an MCP needs different egress, GPU/heavy deps, or cross-session shared state. See §19.2. | Spike at Step 8 with a real MCP (`@modelcontextprotocol/server-github`). Verify init-message status surfaces correctly when the MCP fails to start. |
| A8 | Hot-mode idle timeout — how long to keep a container alive between turns, and what counts as "active"? | **Resolved.** Default 30 min (`idle_container_timeout_seconds = 1800`). "Active" = last `query()` start time, NOT last subscriber attach. Reading history / attaching the CLI doesn't count. | `agentd` periodic sweep over SQLite `last_active_at`; container `stop` on expiry. Surface remaining TTL in `agentctl status`. |
| A9 | `agentctl start --detach` semantics — what's the contract? | **Resolved (§4).** `--detach` returns immediately after the container is up and the session row is written; runner stays alive without an attached client. Output prints session id + attach hint. Interactive `start` keeps the runner alive on Ctrl+D; user must `stop` or `destroy` explicitly. | Covered by §4 spec + happy-path test in Step 4. |
| A10 | Behavior of SDK `resume` after a `dangling_tool_use` crash sub-case (§7.3) — does the SDK error, succeed, or pass through? Synthetic `tool_result` injection must be done correctly via `claude_agent_sdk._internal.session_mutations`. | **Phase 2.** Crash recovery is deferred per the §7 callout. The injection design (synthetic `tool_result` with `is_error=true` and content `"Tool execution interrupted by container restart."`) is preserved here as the Phase 2 plan; not implemented in Phase 1. | Phase 2 pytest: kill subprocess after `tool_use` is appended but before `tool_result`; on resume, run injection; verify SDK accepts. |
| A11 | Whether `enable_file_checkpointing=True` interacts oddly with our recovery logic — does it write its own state outside `.claude/projects/`? | **Phase 2.** Tied to crash recovery; deferred. For Phase 1 we still need to confirm it doesn't write outside `/workspace` (§10 invariant), but no recovery interaction to validate. | Phase 1: filesystem audit after a turn with checkpointing on; assert no writes outside `/workspace`. Phase 2: full A10 alongside. |
| A12 | Behavior when `pending.jsonl` is non-empty at session destroy — silently drop, refuse destroy, or warn? | **Resolved (§4).** Drop with a warning event in `transcript.jsonl` (`{"type": "destroyed_with_pending", "count": N}`) before deletion. CLI prompts `Warning: N queued messages will be discarded. Continue? [y/N]`. Web UI surfaces the same prompt. | Implement at Step 7 alongside `agentctl destroy`. |

### 25.3 Operational — affect day-to-day usability

These won't break the design, but ignoring them makes the prototype painful to live with.

| # | Question | Default lean | Verification plan |
|---|---|---|---|
| O1 | Logging architecture — where do `agentd`, runner, and CLI subprocess logs go? | **Resolved.** Three streams, three files, one correlator (a ULID `turn_id`). `agentd` → `~/.agentd/agentd.log` (Python `RotatingFileHandler`, 50 MB × 5). Runner structured logs → `~/.agentd/sessions/<id>/runner.log` (per-session, GC'd on `destroy`). `claude` CLI stderr → captured via `ClaudeAgentOptions(stderr=callback)` (confirmed in `claude_agent_sdk/types.py:stderr`, wired in `_internal/transport/subprocess_cli.py:_handle_stderr`) → `~/.agentd/sessions/<id>/cli-stderr.log`. Docker stderr stays as a fallback only — no turn correlation, lost on `docker rm`. `agentctl logs` merges all three filtered by session_id and sorted by timestamp; `--turn` / `--source` / `-f` / `--json` modifiers per §4. **Do not** use the deprecated `debug_stderr` option. | Wire at Step 4 alongside event teeing. Verify `turn_id` propagates from `turn_started` through every record. |
| O2 | Streaming protocol framing and backpressure — how does runner ↔ agentd ↔ subscribers handle slow consumers without blocking the SDK? | **Resolved (§13.4, §22.3).** NDJSON over Unix socket — same shape on the wire, in `transcript.jsonl`, and on the WS frame, so `tail -f \| jq` and replay-from-disk are byte-identical to live stream. Single broadcaster task in the runner owns the SDK iterator; durably writes the event before fanning out. Per-subscriber `asyncio.Queue(maxsize=256)` (~30s buffer, ~2.5 MB worst case). `put_nowait` only — never await a subscriber. Overflow → close socket, force reconnect with `?since=<last_offset>`, backfill from `transcript.jsonl`. Block-with-timeout and spill-to-disk both rejected (one stalls the SDK iterator, the other duplicates the durable log). | Prototype at Step 4; load-test by attaching a sleeping subscriber and verifying the runner is not blocked. Cite: NATS slow-consumer, Kafka offset/replay, Jupyter iopub. |
| O3 | Image build, distribution, updates — where does `agent-runner:local` come from? | **Resolved.** Dockerfile in repo at `runtime/Dockerfile`. `docker buildx` with `docker-container` driver. Local dev: `make dev` builds single-arch via `--load`; fast. CI: `make release` builds multi-arch (`linux/amd64,linux/arm64`) on native runners (`ubuntu-latest` + `ubuntu-24.04-arm`) and pushes to **GHCR** with both `vX.Y.Z` and `@sha256:` tags. Docker Hub rejected (anonymous-pull rate limits). agentd resolves the configured tag to a digest at startup and stores the digest in `.agent/session.json` so an upstream re-tag cannot mutate a running session's runtime. `agentctl upgrade-runner` does `docker pull` + bumps the configured tag; new sessions pick it up, in-flight sessions keep their pinned digest. | Set up in Step 1 alongside the image. Cite: [Docker multi-platform builds](https://docs.docker.com/build/building/multi-platform/). |
| O4 | macOS Docker Desktop bind-mount perf — git operations on bind-mounted volumes go through gRPC FS; slow on large repos. | Document as a known limitation. For very large repos, consider future named-volume-with-rsync hybrid; out of MVP scope. | Benchmark `git status` and `git clone` of a representative repo size; surface latency in `agentctl diff` if >2s. |
| O5 | Cost / budget tracking in MVP — anything beyond raw API calls? | **Resolved.** Persist per-turn usage to `.agent/usage.db` (SQLite) on every `ResultMessage`. One row per turn: `turn_num, ts, cost_usd, input_tokens, output_tokens, cache_read_input_tokens, cache_creation_input_tokens, model`. Source fields: `ResultMessage.total_cost_usd` (cumulative session) and `ResultMessage.usage` (raw Anthropic usage dict, includes cache tokens). On resume the CLI rehydrates cumulatives from the JSONL — treat the JSONL as authoritative; agentctl owns its own per-turn log for billing. Surface in `agentctl status` and web UI. No hard caps in MVP; the SDK's `ClaudeAgentOptions.max_budget_usd` is available later (yields `subtype="error_max_budget_usd"`). | Implement at Step 4 alongside event teeing. Verify cumulatives across hot↔cold restart match. |
| O6 | Live model and config changes mid-session — can the user change model, `permission_mode`, or MCP set without a fresh session? | **Resolved (source-verified).** The SDK exposes exactly **four** runtime-mutable knobs via control requests: `client.set_model`, `client.set_permission_mode`, `client.reconnect_mcp_server`, `client.toggle_mcp_server` (`_internal/query.py:735-794`). Everything else (`system_prompt`, `append_system_prompt`, `tools` / `allowed_tools` / `disallowed_tools`, `skills`, `hooks`, `mcp_servers` set, `setting_sources`, `cwd`, `env`, `add_dirs`, `betas`, `max_turns`, `max_budget_usd`) is consumed at construction and requires a fresh `ClaudeSDKClient` = `agentctl restart`. Don't try to fake hot-reload by reconnecting the transport — the CLI subprocess holds initialize state. | Document the live/restart matrix in §13; surface live switches as `agentctl set-model` / `set-mode` / `mcp reconnect|toggle` (§4); everything else is `agentctl restart`. |
| O7 | Network egress policy in containers — direct vs. via gateway | MVP allows direct (simpler dev experience). Production: egress only to LiteLLM/internal gateway via `--network` policy. | Defer to production hardening (Step 10). |

### 25.4 Future / Cloud-Phase

Deferred until we move beyond local. Tracked here so they're not forgotten.

| # | Question | Notes |
|---|---|---|
| F1 | When to adopt the `SessionStore` plugin (Postgres/Redis/S3) for production cloud deployments | Available in SDK (`examples/session_stores/`). Subprocess still writes local disk; store is a mirror channel. Adopt when sessions need to survive node loss or be readable from multiple nodes. |
| F2 | UI auth model — multi-user web UI with per-user session ownership and ACLs | Out of scope for local MVP. Likely needs SSO + a session-ownership column in SQLite. Web UI endpoints (§22.1) gain auth middleware. |
| F3 | Token broker — short-lived session-scoped tokens minted by `agentd`/gateway, replacing long-lived dev creds | §18.2 sketches the pattern. Required before any multi-tenant deployment. |
| F4 | GitHub App installation tokens replacing developer PATs | Same trigger as F3. Per-session installation token. |
| F5 | Kubernetes mapping — session volume → PVC, container → pod, `agentd` → operator | §3 anticipates this. PVC reclaim policy and pod restartPolicy choices map onto §5 hot/cold. |

### 25.5 Minor / Nice-to-Have

Won't change the design; settle in implementation review.

| # | Question | Default |
|---|---|---|
| M1 | Timestamp format in `transcript.jsonl` and `events.jsonl` | UTC ISO-8601 with millisecond precision (`2026-05-09T14:23:45.123Z`). Container clock assumed correct (Docker syncs from host). |
| M2 | Mock/replay for tests — how to test runner without real API calls | Use `claude-agent-sdk`'s `InMemorySessionStore` + a fake transport stub for unit tests; record/replay for integration tests. |
| M3 | `agentd` SQLite schema migrations on upgrade | Embed migration scripts (`migrations/0001_init.sql`, `0002_add_queue_depth.sql`) and apply on boot if `PRAGMA user_version` is stale. |
| M4 | Subagent (`/.claude/agents/`) and slash-command (`/.claude/commands/`) preloading | Same template-copy pattern as skills. Seeded from `~/.agentd/templates/{agents,commands}/` at `agentctl start`. |
| M5 | Default skill set for new sessions | `repo-discovery`, `code-review`, plus whatever the team standardizes on. List in `~/.agentd/templates/skills/README.md`. |
| M6 | What happens to `transcript.jsonl` on long-running sessions (size, rotation) | Soft cap at 100 MB per file; rotate to `transcript.<offset>.jsonl` and keep an index. UI loads only the latest segment by default. |
| M7 | Whether to surface `interrupt` from inside the agent's tool calls (e.g., long bash command) | MVP: only via explicit `agentctl interrupt`. Tool-level cancellation (e.g., SIGINT to a long bash) is a follow-up. |

### 25.6 Mandatory smoke test before locking persistence (Step 4)

```bash
# 1. Start, send one message, observe success.
agentctl start
> Hello, my name is Vipul.
[wait for response]

# 2. Stop the container (simulating cold restart).
agentctl stop $SESSION_ID

# 3. Verify SDK JSONL exists on the volume.
ls -la ~/.agentd/sessions/$SESSION_ID/workspace/.claude/projects/-workspace/
# expect: <session_id>.jsonl with content

# 4. Send a follow-up that requires prior context.
agentctl send $SESSION_ID "What is my name?"
# expect: response includes "Vipul"
```

If step 4 fails: the resume mechanism is broken. Do not proceed past Step 4 until it works.

### 25.7 Triage summary

- **Resolved with concrete designs** (folded into the body sections; tests required at the noted steps): A1 (§13.2), A2 (§23 step 3), A3 (§18.1), A4 (§18.1), A6 (§9 + §A6 row), A7 (§19.2), A8 (§17), A9 (§4), A12 (§4), O1 (§A4 + §A6 + §O1 row), O2 (§13.4 + §22.3), O3 (§A6 + §O3 row), O5 (§9 + §16 + §O5 row), O6 (§4 + §13 + §O6 row).
- **Deferred to Phase 2** per the §7 callout: A5 hard-kill recoverability, A10 (dangling-tool-use injection), A11 (file-checkpointing + recovery interaction). Phase 1 keeps A5 graceful only and A11 reduced to "confirm no writes outside `/workspace`".
- **Defer**: F1–F5 (cloud phase), M1–M7 (implementation polish), O4 (macOS perf), O7 (network egress policy).
- **Smoke tests still required before locking each step**: §25.6 resume smoke test (Step 4); A1 concurrency test, A4 host-readability test (Step 5); A6 cross-version resume test, A7 MCP failure surface test (Step 8).

---

## 26. Current Design Decision Summary

| Topic | Decision |
|---|---|
| Local runtime | Docker |
| Main CLI | `agentctl` |
| Local daemon | `agentd` |
| Agent runtime | Python `claude-agent-sdk` (shells out to `claude` CLI) inside container |
| Session persistence | One bind-mounted host folder per session |
| Volume backend (local) | Host folder mount at `~/.agentd/sessions/<id>/workspace` → `/workspace` |
| Default data root | `~/.agentd` |
| Metadata DB | SQLite (`~/.agentd/agentd.db`); metadata only, no conversation |
| Conversation understanding | Not in `agentd`. Runner only owns protocol-level state. |
| Conversation replay | Claude Agent SDK with `resume=session_id` |
| SDK session-store path | `/workspace/.claude/projects/-workspace/<session_id>.jsonl` |
| How SDK is redirected to volume | `CLAUDE_CONFIG_DIR=/workspace/.claude` (container env + `options.env`) |
| Workspace path in container | `/workspace` |
| Skills | Preloaded under `/workspace/.claude/skills/` |
| MCP config | `/workspace/.mcp.json` |
| Permission mode | `bypassPermissions` inside container only; container is the boundary |
| First execution mode | Hot mode |
| Cold mode | Supported via the same volume model and `pending.jsonl` |
| Mid-turn message queueing | `agent-runner` owns `.agent/pending.jsonl`; container is not stopped/restarted to process queued messages |
| Mid-turn interrupt | `client.interrupt()` + mandatory drain helper before next `query()` |
| Crash recovery | `.agent/current_turn.json` write-ahead marker; runner reconciles dangling tool calls on boot |
| Dual transcript | SDK JSONL (replay) + `.agent/transcript.jsonl` (UI/audit) |
| Web UI access | HTTP + WebSocket endpoints on `agentd`; reads `transcript.jsonl`, multiplexes runner stream |
| Event offsets | Monotonic integer per session, written into `transcript.jsonl` for resumable subscriptions |
| Large tool payloads | Spilled to `.agent/runs/<turn_id>/<event_id>.txt`, referenced by `content_ref` in transcript |
| Pluggable `SessionStore` | Available in SDK (Postgres/Redis/S3 examples); deferred to cloud phase |

---

## 27. One-Line Architecture

```text
agentctl talks to agentd; agentd starts a per-session container with a per-session bind-mounted volume and CLAUDE_CONFIG_DIR=/workspace/.claude; the runner inside the container owns a state machine (IDLE/RUNNING/DRAINING) plus a journal at /workspace/.agent (pending queue, current-turn marker, transcript), wraps a single ClaudeSDKClient with resume=session_id, and tees SDK events to its transcript while the SDK persists conversation JSONL on the same volume — so interruption, queueing, crash recovery, hot↔cold restarts, and web UI subscription all reduce to operations on the volume's three ownership-isolated layers (SDK conversation, runner runtime journal, agent workspace).
```
