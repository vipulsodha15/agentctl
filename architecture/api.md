# API surfaces

This doc specifies every wire surface in v1. Three channels:

1. **CLI ↔ agentd** — Unix domain socket, length-prefixed NDJSON RPC.
2. **Browser ↔ agentd** — HTTP + WebSocket over `127.0.0.1` (default 7777).
3. **Container ↔ agentd** — bind-mounted Unix domain socket inside each
   session container, line-delimited JSON.

All three surfaces share **one logical API**. (1) and (2) are different
transports for the same set of operations; (3) is a smaller, internal
protocol between `agentd` and the runtime shim.

## 1. Logical operations (transport-agnostic)

These names are used by both the CLI socket RPC and the HTTP+WS surface.

| Op | Purpose | Direction |
|---|---|---|
| `Health` | Liveness probe. | client → server |
| `ListSessions` | Enumerate sessions (any state). | client → server |
| `GetSession` | Full session detail (incl. MCP set, repos, base SHAs). | client → server |
| `CreateSession` | Start a new session (name, mcps, repos, model, caps). | client → server |
| `SendMessage` | Append a user message to a session's queue. | client → server |
| `Interrupt` | Cancel the in-flight turn. | client → server |
| `Detach` | Server-noted, client-driven (close stream). | client → server |
| `TerminateSession` | End a session permanently. | client → server |
| `RestartSession` | Stop+recreate the container; preserve volume. | client → server |
| `AttachStream` | Subscribe to a session's event stream. | client → server (long-lived) |
| `SnapshotSession` | Fetch the runtime's current conversation history (for replay-on-attach beyond the event buffer). | client → server |
| `Diff` | Get diff against base SHA per repo. | client → server |
| `ExportPatch` | Write a `.patch` for the session's working tree. | client → server |
| `ExportPush` | `git push` the session's working tree to a branch. | client → server |
| `ListMCPs` / `AddMCP` / `UpdateMCP` / `RemoveMCP` / `SetDefaultMCP` | Registry CRUD. | client → server |
| `ListSkills` | Fetch the skill manifest from the running container. | client → server |
| `ListInstalledSkills` | Enumerate built-in + custom skills as known to agentd (independent of any session). | client → server |
| `AddSkill` / `RemoveSkill` / `ImportSkill` / `ExportSkill` / `ValidateSkill` | Manage the custom-skills directory under `~/.local/share/agentctl/custom-skills/`. `ImportSkill` is used by `agentctl init`'s Claude Code import phase and by `agentctl skill import`. | client → server |
| `GetCost` / `GetUsage` | Cost data. | client → server |
| `GetLogs` | Stream per-session log file. | client → server (long-lived) |
| `Doctor` | Run install + connectivity checks. | client → server |
| `Update` | Pull a new base image, repin. | client → server |

Server-pushed event kinds (over `AttachStream` / `GetLogs`) are listed in §5.

## 2. CLI ↔ agentd: Unix-socket RPC

### 2.1 Transport

- **Path:** `~/.local/share/agentctl/agentd.sock`. Mode `0600`. Parent dir
  `0700`.
- **Framing:** length-prefixed JSON. Each message is `<u32 big-endian
  length><utf8 JSON object>`. Length is bytes of the JSON payload, max
  16 MiB (configurable; protects against runaway peers). The 4-byte length
  is **not** included in the count.
- **Concurrency:** a single connection multiplexes many in-flight requests
  via the `id` field. Streams use the same `id` for all subsequent frames
  until terminated.
- **Why not gRPC / HTTP-over-Unix?** Length-prefixed JSON is trivial to
  implement in any language a CLI might be written in (Go, Rust, Python),
  has zero codegen, and matches how the WS surface frames events. We pay a
  small CPU cost vs. gRPC; not the bottleneck.

### 2.2 Frame schema

Every frame is a JSON object with these top-level fields. Extra fields are
ignored on read (forward compat).

```jsonc
{
  "v": 1,                     // protocol version (integer)
  "id": "01JFZ…",             // ULID or UUIDv7. Unique within the connection.
  "kind": "request"           // request | response | event | stream_chunk | stream_end | error
            | "response"
            | "event"
            | "stream_chunk"
            | "stream_end"
            | "error",
  "op": "CreateSession",      // for kind=request, op=event_kind for kind=event
  "data": { /* op-specific */ }
}
```

- A non-streaming RPC: client sends `kind=request, id=X`, server replies
  `kind=response, id=X` or `kind=error, id=X`.
- A streaming RPC: client sends `kind=request, id=X`. Server replies with
  zero or more `kind=stream_chunk, id=X` frames, terminated by exactly one
  `kind=stream_end, id=X` (clean) or `kind=error, id=X` (failure).
- Server-initiated events without a paired request use `kind=event` with an
  `id` the server generates and a `session_id` field in `data`. Currently
  v1 does not push events without a subscription, so this is reserved.

### 2.3 Error frame

```json
{
  "v": 1, "id": "…", "kind": "error",
  "data": {
    "code": "session_not_found",
    "message": "No session with id sess_01JFZ… exists.",
    "retryable": false,
    "details": { "session_id": "sess_01JFZ…" }
  }
}
```

Error codes (stable; CLI exit-code map is in `agentd.md` §7):

| Code | When |
|---|---|
| `bad_request` | Malformed input. |
| `not_found` | Session or registry entry missing. |
| `conflict` | E.g., MCP `add` with duplicate name. |
| `precondition_failed` | E.g., `interrupt` with no in-flight turn. |
| `unauthenticated` | Web bearer token missing/wrong. |
| `forbidden` | Origin check failure. |
| `unavailable` | Reconciling, Docker down. |
| `rate_limited` | Container event throttling kicked in. |
| `internal` | Unexpected. |
| `runtime_error` | Container reported failure. |
| `version_mismatch` | Client `v` newer than server's max known. |

### 2.4 Per-op shapes

Only `data` is shown — frame envelope is per §2.2.

#### `Health` (request → response)

Request `data`: `{}`.

Response `data`:

```json
{
  "ok": true,
  "version": "0.1.0",
  "build": "git-sha-or-release-tag",
  "reconciling": false,
  "docker": { "ok": true, "version": "27.0.0" },
  "uptime_s": 12345
}
```

`ok=false` is returned when reconcile is in progress or Docker is
unreachable; doctor uses this.

#### `CreateSession`

Request `data`:

```json
{
  "name": "my-session",
  "mcps": ["github", "internal-jira"],   // null = use registry defaults
  "exclude_mcps": [],                    // mutually exclusive with mcps
  "repos": ["https://github.com/me/foo.git"],
  "model": "claude-sonnet-4-6",          // null = config default
  "mem_limit_bytes": 4294967296,         // null = config default (4 GB)
  "cpu_limit_cores": 2.0
}
```

Response `data`:

```json
{
  "session_id": "sess_01JFZ…",
  "status": "starting",
  "web_url": "http://127.0.0.1:7777/sessions/sess_01JFZ…",
  "attach": {
    "stream_op": "AttachStream",
    "since_event": null
  }
}
```

The CLI typically follows up with `AttachStream` immediately.

#### `SendMessage`

Request `data`:

```json
{
  "session_id": "sess_01JFZ…",
  "content": "please refactor foo.py",
  "client_id": "cli-pid-12345",       // for fan-out attribution
  "idempotency_key": "01JG…"          // optional; client retry safety
}
```

Response `data`:

```json
{
  "message_id": "msg_01JG…",
  "queued": false,                    // true if a turn was already in flight
  "queue_depth": 0
}
```

Idempotency: if `idempotency_key` matches a message accepted in the last
5 minutes, the same `message_id` is returned without re-enqueuing.

#### `Interrupt`

Request `data`:

```json
{
  "session_id": "sess_01JFZ…",
  "clear_queue": false
}
```

Response `data`:

```json
{
  "interrupted": true,                 // false if no in-flight turn
  "cleared_queue_depth": 0
}
```

#### `AttachStream` (streaming)

Request `data`:

```json
{
  "session_id": "sess_01JFZ…",
  "since_event": "evt_01JG…",          // optional resume cursor
  "include_history": false             // if true, server pre-streams snapshot
}
```

Response: a series of `stream_chunk` frames whose `data` is one event from
§5; ends with `stream_end{reason: "client_disconnected" | "session_terminated"}`
or an `error`.

If `since_event` is older than the buffer, the server replies with a single
`error{code: "buffer_overflow"}` and the client falls back to
`SnapshotSession` + `AttachStream{since_event: <last-snapshot-event>}`.

#### `SnapshotSession`

Request `data`: `{ "session_id": "…" }`.

Response `data`:

```json
{
  "messages": [ /* runtime-formatted conversation, opaque to agentd */ ],
  "tail_event_id": "evt_01JG…",
  "fetched_at": "2026-05-09T12:00:00.000Z"
}
```

`agentd` proxies this to the runtime via the control sock (§4) and caches
for 30 s. Subsequent `AttachStream{since_event: tail_event_id}` resumes the
live tail.

#### `Diff`, `ExportPatch`, `ExportPush`

Each is a streaming response that yields binary chunks (the patch contents
or the git-push output). `data.repo` (optional) scopes to one repo. See
`agentd.md` §5 for shim implementation.

#### Registry CRUD

```json
// AddMCP — `transport` is freeform; v1 known values: "http", "sse".
//          `kind` is freeform; v1 known values: "none", "github_pat".
//          `auth_config` is kind-specific JSON (omit/null for v1 kinds).
{ "name": "team-x", "url": "https://…", "transport": "http",
  "kind": "none", "auth_config": null,
  "default_enabled": true, "description": "…" }
// UpdateMCP — same shape, all fields optional except name
// RemoveMCP — { "name": "team-x", "force": false }
// ListMCPs — {}
// SetDefaultMCP — { "name": "team-x", "default_enabled": true }

// ImportSkill — copy a skill subdirectory from `source_path` on the host
//               into ~/.local/share/agentctl/custom-skills/<name>/.
//               agentd validates the manifest, refuses on collision
//               with a built-in unless `force` is true, and records the
//               final path. Used by `agentctl init`'s Claude import
//               phase and `agentctl skill import`.
{ "source_path": "/Users/me/.claude/skills/postmortem",
  "name": "postmortem",                  // optional; defaults to basename(source_path)
  "force": false,                        // overwrite existing custom skill / shadow built-in
  "dry_run": false }
// Response: { "imported": true, "name": "postmortem",
//             "path": "~/.local/share/agentctl/custom-skills/postmortem",
//             "shadowed_builtin": false,
//             "skipped_reason": null }
```

### 2.5 Versioning

- `v: 1` is the only version v1 of agentctl speaks.
- Forward compat: clients ignore unknown response fields and unknown event
  kinds. Servers ignore unknown request fields.
- A client that needs a newer `v` than the server speaks gets `error{code:
  "version_mismatch", details: {server_max_v: 1}}`. The CLI prints "your
  agentd is older than your agentctl; run `agentctl init --repair`."
- Breaking schema changes between v1 and a future v2 happen by bumping `v`
  and serving both versions in parallel for at least one minor release.

## 3. Browser ↔ agentd: HTTP + WebSocket

### 3.1 Transport

- **Bind:** `127.0.0.1:7777` (default; configurable).
- **TLS:** none. Localhost only; introducing self-signed cert
  ceremony for a loopback-only port has more downsides (cert pinning,
  warning UX) than upsides (§security.md §3).
- **Static UI:** served from the `agentd` binary at `GET /`. Single-page
  app; routes are client-side.
- **API base:** `/v1/…`. All endpoints under `/v1/` require the bearer
  token (§3.3). All state-changing endpoints additionally require a
  matching `Origin` header (§3.4).

### 3.2 Endpoint map

| Method | Path | Logical op |
|---|---|---|
| `GET` | `/healthz` | `Health` (no auth required) |
| `GET` | `/v1/sessions` | `ListSessions` |
| `POST` | `/v1/sessions` | `CreateSession` |
| `GET` | `/v1/sessions/{id}` | `GetSession` |
| `DELETE` | `/v1/sessions/{id}` | `TerminateSession` |
| `POST` | `/v1/sessions/{id}/messages` | `SendMessage` |
| `POST` | `/v1/sessions/{id}/interrupt` | `Interrupt` |
| `POST` | `/v1/sessions/{id}/restart` | `RestartSession` |
| `GET` | `/v1/sessions/{id}/snapshot` | `SnapshotSession` |
| `GET` | `/v1/sessions/{id}/diff` | `Diff` (octet-stream) |
| `POST` | `/v1/sessions/{id}/export/patch` | `ExportPatch` (octet-stream) |
| `POST` | `/v1/sessions/{id}/export/push` | `ExportPush` (json) |
| `GET` | `/v1/sessions/{id}/logs` | `GetLogs` (text/event-stream) |
| `GET` | `/v1/mcps` | `ListMCPs` |
| `POST` | `/v1/mcps` | `AddMCP` |
| `PATCH` | `/v1/mcps/{name}` | `UpdateMCP` |
| `DELETE` | `/v1/mcps/{name}` | `RemoveMCP` |
| `GET` | `/v1/sessions/{id}/skills` | `ListSkills` |
| `GET` | `/v1/skills` | `ListInstalledSkills` |
| `POST` | `/v1/skills` | `AddSkill` (multipart upload of a tarball or a path on the host) |
| `POST` | `/v1/skills/import` | `ImportSkill` |
| `POST` | `/v1/skills/{name}/validate` | `ValidateSkill` |
| `GET` | `/v1/skills/{name}/export` | `ExportSkill` (octet-stream tarball) |
| `DELETE` | `/v1/skills/{name}` | `RemoveSkill` |
| `GET` | `/v1/usage?since=…&session_id=…` | `GetUsage` |
| `POST` | `/v1/doctor` | `Doctor` |
| `POST` | `/v1/update` | `Update` |
| `GET` (Upgrade) | `/v1/sessions/{id}/stream` | `AttachStream` (WebSocket) |

Request and response bodies are JSON (Content-Type `application/json`)
mirroring §2.4. HTTP status codes: 2xx ok, 4xx client error (mapped from
the error codes in §2.3), 5xx unavailable/internal.

### 3.3 Authentication

- The bearer token from `~/.config/agentctl/web_token` must be presented on
  every `/v1/*` and `/v1/sessions/{id}/stream` request.
- Acceptable carriers, in priority order:
  1. `Authorization: Bearer <token>` header.
  2. `agentctl_token` cookie (set by the loader page).
- WebSocket `Upgrade` request: cookie or header. Subprotocol header is
  `agentctl.v1`.
- `/healthz` is the **one** exempt path so doctor checks work cleanly. It
  reveals only the `Health` shape from §2.4.

### 3.4 CSRF / origin protection

- All non-`GET` requests must carry `Origin:
  http://127.0.0.1:<bind-port>`. Missing or mismatched Origin → `403`.
- WebSocket upgrade requests must carry `Origin: http://127.0.0.1:<bind-port>`.
- `Sec-Fetch-Site: same-origin` is checked when present (modern browsers
  always send it). `same-origin` required for state-changing requests;
  `none` accepted on top-level GETs.

### 3.5 WebSocket framing

- Subprotocol: `agentctl.v1`.
- Each text message is one JSON object whose shape exactly matches §2.2's
  `event` or `stream_chunk`/`stream_end`.
- Server pings every 20s; client pongs. Client may also ping to keep the
  connection alive across sleep/wake.
- Client-to-server frames are **not used for control**: if the user wants
  to send a message, they call `POST /v1/sessions/{id}/messages` over
  HTTP, not over the WS. This keeps the WS receive-only on the server side
  (a useful invariant for the fan-out implementation in `agentd.md` §3).

### 3.6 Loader and token handoff

`GET /` returns the SPA. Initial load sequence:

1. CLI runs `agentctl ui` (or the final lines of `agentctl init`). It
   computes `http://127.0.0.1:7777/#t=<bearer_token>` and shells out to
   `xdg-open` / `open` / `cmd /c start`.
2. The browser loads `/`. The fragment is **not** sent to the server. The
   loader script reads `location.hash`, extracts `t=…`, sets a
   `agentctl_token=<token>` cookie scoped to `/v1/`, `Path=/`, `SameSite=Strict`,
   `HttpOnly=false` (the SPA needs to read it to stick on subsequent
   fetches), `Secure=false` (loopback HTTP), then `history.replaceState`s
   the URL to remove the fragment.
3. Subsequent fetches read the cookie and use the `Authorization` header
   (or rely on the cookie being sent automatically on same-origin
   requests).
4. If the cookie is absent and the URL has no fragment, the SPA renders
   "open this UI by running `agentctl ui` from a terminal." It does **not**
   prompt for a token (defense against shoulder-surf / phishing pages
   instructing users to paste a token).

### 3.7 Versioning

- The HTTP path `/v1/` is the version axis. Future v2 endpoints would live
  under `/v2/`.
- The WebSocket subprotocol carries the version: `agentctl.v1`.
- The SPA bundles its own client; it is recompiled with the daemon. There
  is no third-party SPA talking to multiple agentd versions.

## 4. Container ↔ agentd: control socket

### 4.1 Transport

- A Unix domain socket on the host at
  `~/.local/share/agentctl/sessions/<id>/control/agentd.sock`, owned by
  the running user, mode `0660`.
- The `control/` directory is bind-mounted into the container at
  `/run/agentctl/control/` **read-write** (the runtime needs to connect to
  the socket). No other host paths are mounted.
- Inside the container, the runtime shim connects to
  `/run/agentctl/control/agentd.sock`.
- This is the **only** host-loopback-equivalent the container ever has.
  R7's "no host loopback" rule applies to network ports; this is a
  bind-mounted socket with explicit fs perms.

### 4.2 Frame schema

Line-delimited JSON (NDJSON). One frame = one line of UTF-8 JSON ending in
`\n`. Max line length 1 MiB. Both directions speak the same envelope:

```jsonc
{
  "v": 1,
  "seq": 42,                 // monotonic per direction; seq=0 reserved for greeting
  "kind": "runtime.event",   // see §4.3
  "ts": "2026-05-09T12:00:00.000Z",
  "data": { /* kind-specific */ }
}
```

### 4.3 Direction-specific kinds

**Container → agentd:**

| Kind | When | `data` |
|---|---|---|
| `runtime.hello` | Sent first by the shim. | `{ shim_version, sdk_version, sdk: "claude-agent-sdk-python", pid, capabilities[] }` |
| `runtime.ready` | Repos cloned, runtime initialized, ready for first message. | `{ repos: [{ name, url, base_sha, branch }], skills: [{ name, description }], started_at }` |
| `runtime.event` | Stream events from the runtime: `assistant.delta`, `assistant.message`, `tool.call`, `tool.result`, `usage`, `turn.start`, `turn.end`, `turn.cancelled`. Each is opaque to agentd except `usage` (R10) and `turn.*` (queue / interrupt logic). | varies; documented in §5 |
| `runtime.error` | Terminal error. agentd marks the session `error`. | `{ code, message, fatal }` |
| `runtime.heartbeat` | Every 5 s. Used for liveness; absence for 30 s ⇒ session marked stopped. | `{}` |
| `repo.changed` | Working tree changed (fs-watcher in shim). Fires throttled (max 2/s/repo). | `{ repo, files_changed, deletions, additions }` |
| `runtime.snapshot` | Reply to an `agentd.snapshot_request`. | `{ messages: [...], tail_event_id }` |

**agentd → container:**

| Kind | When | `data` |
|---|---|---|
| `agentd.greet` | First reply to `runtime.hello`. | `{ session_id, env: {…}, model, mcps: [{ name, url, transport, kind, auth_config?, headers? }], repos: [...], limits, log_level }` |
| `agentd.message` | Deliver a queued user message. | `{ message_id, content, idempotency_key }` |
| `agentd.interrupt` | Cancel current turn. | `{ reason: "user" \| "hard_cutoff" \| "shutdown" }` |
| `agentd.snapshot_request` | Ask the runtime to dump current history. | `{ request_id }` |
| `agentd.shutdown` | Graceful stop. Shim flushes and exits. | `{ grace_seconds: 30 }` |
| `agentd.config_reload` | Reserved (v2): live config update. Not used in v1. | `{}` |

### 4.4 Authentication / authorization

- The control sock is in a directory mode `0700` owned by the user; only
  processes running as that user (and the bind-mounted container running
  as the same uid via Docker) can open it. We rely on this fs-perms
  boundary; no in-band auth.
- The shim's `runtime.hello` includes a `session_token` (a 256-bit random
  written to `session.json` at create time). agentd verifies it before
  sending `agentd.greet`. This binds "the process that connected to this
  socket" to "the session that owns this socket dir" even if a malicious
  process inside the container tries to be the shim.
- agentd's accept loop binds **one** active control connection per
  session. A second connect attempt while one is alive returns
  `agentd.error{code: "already_connected"}` and is closed.

### 4.5 Backpressure & rate limits

- agentd reads from the control sock with a 100-frame buffer per session.
- If the shim sends >100 frames/s sustained for >10 s, agentd starts
  dropping `runtime.event` frames of kind `assistant.delta` only and emits
  `runtime.throttled` to clients. Other kinds (`tool.*`, `turn.*`,
  `usage`, `runtime.error`) are never dropped.
- Sustained throttling for >60 s ⇒ session marked `error`.

### 4.6 Versioning

- Shim and agentd ship together (built from the same release). `v: 1` is
  the only version v1 speaks.
- The shim refuses to start if the `agentd.greet` it receives has a `v` it
  doesn't understand (forward-compat in case a user starts an old
  container against a newer agentd). agentd accepts older `v: 1` shims
  forever.

## 5. Event vocabulary (server-pushed over `AttachStream`)

These are the events any attached client sees. Every event is wrapped in
the §2.2 `stream_chunk` frame (CLI socket) or a WebSocket text frame
(browser). Each event has a stable `event_id` (ULID) and `seq` per session.

| Kind | When | `data` |
|---|---|---|
| `session.snapshot` | First frame after `AttachStream`. | `{ session, queue_depth, in_flight, mcps_status, repos, last_seq }` |
| `session.starting` | During create. | `{ phase: "image_pull"\|"network"\|"container"\|"shim_init"\|"repo_clone" }` |
| `session.running` | Container ready. | `{}` |
| `session.stopping` | Idle/manual stop. | `{ reason }` |
| `session.stopped` | Container exited. | `{ reason, exit_code? }` |
| `session.resumed` | After idle-stop. | `{}` |
| `session.terminated` | After explicit stop. | `{}` |
| `session.error` | Terminal error. | `{ code, message }` |
| `mcp.unreachable` | Probe failed. | `{ name, transport, error }` |
| `mcp.skipped` | MCP omitted from runtime config because its `transport` or `kind` is unknown to this image. | `{ name, transport, kind, reason }` |
| `turn.start` | Runtime began a turn. | `{ turn_id, message_id, model }` |
| `turn.end` | Runtime finished. | `{ turn_id, status: "ok"\|"cancelled" }` |
| `turn.cancelled` | Interrupt acknowledged. | `{ turn_id, reason }` |
| `assistant.delta` | Incremental tokens. | `{ turn_id, delta }` |
| `assistant.message` | Final text. | `{ turn_id, content }` |
| `tool.call` | Tool invocation. | `{ turn_id, tool, input }` |
| `tool.result` | Tool result. | `{ turn_id, tool, output, is_error }` |
| `user.message` | A user message was accepted (echoed for fan-out). | `{ message_id, content, client_id }` |
| `usage` | Per-turn token / cost record (also written to DB). | `{ turn_id, model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost_usd }` |
| `queue.depth` | Queue size changed. | `{ depth }` |
| `repo.changed` | Working tree edited. | `{ repo, files_changed, additions, deletions }` |
| `skills.changed` | Manifest refreshed (e.g., container restart). | `{ skills: [...] }` |
| `runtime.throttled` | Rate-limit kicked in. | `{ active }` |
| `log.line` | Only on `GetLogs`, not `AttachStream`. | `{ ts, level, msg, fields }` |

Clients **must** ignore unknown event kinds and unknown fields.

## 6. Idempotency, retries, and ordering

- **`SendMessage`** accepts `idempotency_key`. agentd dedupes within a 5 m
  window per session.
- **`Interrupt`** is naturally idempotent (no-op when no in-flight turn).
- **`CreateSession`** is **not** idempotent in v1; the CLI reports the
  newly-created session id. Retrying after a network timeout can create
  two sessions; the CLI prints both ids and asks the user.
- **Event ordering** is global per session: `seq` is monotonic across
  every fan-out emission. Clients use `seq` (not timestamps) for ordering
  on reconnect.
- **At-least-once vs exactly-once delivery on the WS:** at-least-once.
  Clients must dedupe by `event_id` if they care; the SPA does.

## 7. Pagination, cursors, and limits

- `ListSessions` returns up to 200 rows; if there are more, the response
  includes `next_cursor`. (v1 default machines won't hit this.)
- `GetUsage` requires either `since` (a duration like `7d` or absolute
  range) or `session_id`. Returns up to 5,000 rows; uses `next_cursor`
  when needed.
- `GetLogs` is a long-lived stream; closes at EOF or on rotation (clients
  reconnect transparently).

## 8. CLI exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 2 | Environment problem (Docker, perms, network). Maps to `unavailable`/`bad_request` from agentd. |
| 3 | Auth problem (Anthropic / GitHub credentials). |
| 4 | Session-state problem (`not_found`, `conflict`, `precondition_failed`). |
| 5 | Runtime/container error. |
| 64 | Usage error (bad CLI flags). |
| 1 | Anything else. |
