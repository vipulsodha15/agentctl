# Observability

This is the resolution of §15.9 plus the broader logging / metrics /
debugging story.

## 1. Three logging tiers

| Tier | Path | Format | Rotation | Read by |
|---|---|---|---|---|
| Daemon log | journald (`agentd.service`, Linux) / `~/Library/Logs/agentctl/agentd.log` (macOS) | NDJSON | journald: per system; macOS file: agentd in-process rotator (50 MB / 7 gen) | `agentctl logs --daemon`, `journalctl --user -u agentd` |
| Per-session log | `~/.local/share/agentctl/sessions/<id>/agentd.log` | NDJSON | agentd in-process: 50 MB or daily, 7 gen, gzipped | `agentctl logs <session>` |
| Container stdout/err | Docker's log store | text | Docker default | `agentctl logs <session> --container` (proxies `docker logs`) |

The first two are what we own; the third is whatever the runtime / shim
prints, which is mostly the shim's own diagnostics (the actual model
stream goes through the control sock, not stdout, so it's not in
container logs).

## 2. Daemon log

### 2.1 Linux: journald

`agentd.service` (systemd `--user`) sets `StandardOutput=journal` and
`StandardError=journal`. The daemon writes structured logs to stderr;
systemd journals them with `_SYSTEMD_USER_UNIT=agentd.service`.

Each line is one NDJSON object:

```json
{"ts":"2026-05-09T12:00:00.123Z","level":"info","component":"sessions",
 "msg":"session.started","session_id":"sess_01JFZ…",
 "image_id":"sha256:abcd…","mcps":["github","internal-jira"]}
```

`journalctl --user -u agentd` reads it raw; `journalctl --user -u
agentd -o json` reads with metadata. We do **not** rely on systemd's
free-text matching; structured queries are the path for operators.

### 2.2 macOS: file + unified log

launchd doesn't redirect stderr to a structured store; we route stderr
to `~/Library/Logs/agentctl/agentd.log` (NDJSON, same shape as Linux),
plus emit one `os_log` line per high-level event for "Console.app"
visibility:

- `os_log_create("com.agentctl.agentd", "default")` for lifecycle.
- Other events go to the file only.

The file is rotated by agentd's in-process rotator (size 50 MB or daily,
keep 7 generations, gzip). launchd has no built-in rotation; we don't
ask it.

### 2.3 Format spec

| Field | Required | Notes |
|---|---|---|
| `ts` | yes | RFC3339Nano UTC. |
| `level` | yes | `debug`, `info`, `warn`, `error`. |
| `component` | yes | `sessions`, `containers`, `mcp`, `web`, `cli`, `sweep`, `recovery`, `usage`, `migration`, `boot`. |
| `msg` | yes | Short event name (e.g., `session.started`, `mcp.unreachable`). Lowercase, dotted; stable. |
| `session_id` | when relevant | Always set when the event scopes to a session. |
| `error` | when level=error | String. |
| `dur_ms` | optional | Operation duration. |
| ad-hoc fields | optional | Snake-case. Avoid free-form prose; use additional fields. |

### 2.4 Levels

- `debug`: high-volume, off by default. Toggleable via
  `agentctl config set agentd.log_level debug` + SIGHUP.
- `info`: lifecycle and notable transitions. Default level.
- `warn`: degraded but proceeding. E.g., MCP unreachable, image-pull
  fallback to cache.
- `error`: action failed. E.g., container failed to start, Docker
  unreachable.

### 2.5 What we do **not** log

- Conversation contents — neither user messages nor assistant responses
  appear in logs. The session log has *metadata* about turns (turn ids,
  durations, tool names, token counts) but not bodies.
- Secrets — `ANTHROPIC_API_KEY`, `GITHUB_PAT`, the web bearer token,
  `session_token`. A log redactor wraps the logger and runs a regex
  pass over every emitted line as a defense-in-depth before flush.

## 3. Per-session log

Lives at `~/.local/share/agentctl/sessions/<session_id>/agentd.log`.

### 3.1 What it contains

Everything the daemon log contains that scopes to this session, plus
slightly more chatty per-session bookkeeping:

- Lifecycle: `session.created`, `session.starting`, `session.running`,
  `session.stopped`, `session.resumed`, `session.terminated`,
  `session.error`, `session.queue_depth_changed`,
  `session.in_flight_changed`, `turn.start`, `turn.end`,
  `turn.cancelled`.
- MCP probes: `mcp.probe.ok`, `mcp.probe.unreachable`, `mcp.skipped` (unknown transport or kind).
- Control-channel I/O **metadata** (kind, seq, byte count) — never
  bodies. Disabled at level `info`; enabled at `debug` only.
- Repo events: `repo.cloned`, `repo.changed`, `repo.export.pushed`,
  `repo.export.failed`.
- Sweeper actions affecting this session: `sweep.idle_stop`,
  `sweep.hard_cutoff`.

### 3.2 Rotation and retention

- Triggered when the file reaches 50 MB **or** at 00:00 local time,
  whichever comes first.
- Names: `agentd.log.1.gz`, `agentd.log.2.gz`, …, `agentd.log.7.gz`
  (oldest dropped at 8).
- Total disk per session: ~ (50 MB current) + 7 × (gzipped 50 MB) ≈
  50 MB + 7 × ~10 MB = ~120 MB worst case. We surface this in
  `agentctl ls --verbose` ("logs: NN MB").

### 3.3 Termination

`agentctl stop <id>` deletes the session directory including all logs.
If a developer needs the logs after termination, they can either copy
the file out before stopping, or run `agentctl export <id> --logs
<path>` (post-v1; not in `api.md` v1 surface).

## 4. `agentctl logs <session>`

Default: pretty-print the per-session NDJSON with colorized levels and
elided long fields. `-f` follows. `--raw` emits NDJSON unchanged.
`--since 5m` filters by `ts`.

Internally implemented by the `GetLogs` op (api.md §3.2): the server
opens the file, streams NDJSON lines as `log.line` events, and watches
for new writes (epoll/kqueue on the file). Rotation transparently
re-opens the new current file.

### 4.1 Variants

- `agentctl logs <session> -f` — follow.
- `agentctl logs <session> --raw` — pure NDJSON.
- `agentctl logs <session> --container` — proxy to `docker logs <id>`
  via agentd (so the developer doesn't need direct Docker access).
- `agentctl logs --daemon` — Linux: shells out to `journalctl --user
  -u agentd -f`; macOS: tails `~/Library/Logs/agentctl/agentd.log`.
- `agentctl logs --daemon --json` — same with `-o json`.

Per O2 in `overview.md` §11, the default `agentctl logs <session>` does
**not** include control-channel frame metadata; pass `--verbose` for it.

## 5. Metrics

Local-only. No outbound. Used for `agentctl doctor`, `agentctl ls
--verbose`, and ad-hoc debugging.

### 5.1 What we count

| Metric | Type | Where it comes from |
|---|---|---|
| `agentd_uptime_seconds` | gauge | Boot. |
| `agentd_active_sessions` | gauge | Session manager. |
| `agentd_total_sessions_created` | counter | Session manager. |
| `agentd_session_starts_total{result}` | counter | result ∈ `ok|error`. |
| `agentd_session_resumes_total` | counter | |
| `agentd_idle_stops_total{reason}` | counter | reason ∈ `idle|hard_cutoff`. |
| `agentd_messages_enqueued_total` | counter | |
| `agentd_turns_total{model,result}` | counter | |
| `agentd_turn_duration_seconds` | histogram (in-memory simple buckets) | |
| `agentd_runtime_throttled_seconds_total{session}` | counter | |
| `agentd_docker_calls_total{op,result}` | counter | |
| `agentd_db_writes_total{table}` | counter | |
| `agentd_snapshot_failed_total` | counter | When a client attach fails because the shim couldn't return the conversation snapshot. |
| `agentd_mcp_probe_results_total{name,result}` | counter | |
| `agentd_recovery_orphans_total{kind}` | counter | container/network/dir orphans found at boot. |

### 5.2 How they're exposed

Internal API endpoint `GET /v1/metrics` (auth'd) returns them as JSON:

```json
{
  "agentd_uptime_seconds": 12345,
  "agentd_active_sessions": 4,
  "counters": { "agentd_session_starts_total{result=ok}": 19, ... }
}
```

`agentctl doctor` reads this; nothing scrapes it. We deliberately do
**not** expose Prometheus exposition format in v1 — it's "send metrics
out" by convention and we want to keep the no-telemetry promise crisp.

A future v2 could opt-in expose `/metrics` for self-hosted scraping,
gated by a config flag.

### 5.3 Doctor's use

`agentctl doctor` reads the metrics endpoint and surfaces:

- "agentd has been up for 4 days; 12 sessions started, 0 errors" — looks
  healthy.
- "agentd has restarted 5 times in the last hour" — looks bad.
- "12 idle stops in the last day, 0 hard cutoffs" — informational.
- "3 snapshot failures in last hour" — investigate shim health or
  missing JSONL files on volumes.

## 6. Tracing

We do **not** ship distributed tracing in v1. The single-process,
single-host architecture doesn't need it; structured logs with consistent
`session_id` and `turn_id` fields produce the same insight via
journalctl/grep.

For developer debugging, the daemon log and the per-session log are
linked via shared `session_id` so an issue scoped to one session can be
investigated by joining both views.

## 7. Crash artifacts

If `agentd` panics or crashes:

- A goroutine/thread stack dump is written to
  `~/.local/state/agentctl/crash-<ts>.log` before exit (best-effort).
- The next boot's reconciler logs an `agentd.unclean_shutdown` event.
- `agentctl doctor` lists recent crashes (last 7 days).

The crash file is rotated to keep at most 10.

## 8. What `agentctl ls` shows

The default columns (developer-facing observability surface):

```text
ID            NAME              STATUS    LAST ACTIVITY  IMAGE_ID    COST
sess_01JFZ…   auth-refactor     running       2m ago     abcd1234    $0.42
sess_01JG0…   lint-cleanup      stopped      1h ago      abcd1234    $0.08
sess_01JG2…   old-experiment    terminated  yesterday    abcd1234    $1.20
```

`agentctl ls --verbose` adds: `IN_FLIGHT QUEUE_DEPTH MEM_LIMIT
CPU_LIMIT VOL_SIZE LOG_SIZE MCP_OK/TOTAL`.

`agentctl ls --json` emits all session columns plus computed cost. This
is the answer to "I want to see everything programmatically without
parsing pretty output."

## 9. Doctor output reference

A successful `agentctl doctor` run looks like:

```text
agentctl 0.1.0 — environment OK

  bin.versions          ok    agentctl=0.1.0  agentd=0.1.0  image=v1.2026-05-01
  fs.perms              ok    secrets.json=0600 …
  db.integrity          ok    schema=1 size=1.2 MB
  service.active        ok    systemd-user.agentd.service active
  agentd.health         ok    uptime=4d sessions=4 reconciling=false
  docker.reachable      ok    Docker 27.0.0
  docker.api            ok
  image.present         ok    agentctl/session-base:local id=sha256:abcd…
  image.built           ok    matches build context hash sha256:1234…
  image.build_context   ok    ~/.local/share/agentctl/image present
  skills.builtin        ok    3 skills (refactor, tests, docs)
  skills.custom         ok    1 skill (postmortem); 0 overrides
  mcp.registry          ok    2 entries
  secrets.fresh         ok    Anthropic + GitHub valid
  network.peer_isolation ok   probe A → probe B: connect timeout (expected)
  volumes.disk          ok    14% used, 4 sessions
```

Failures look like:

```text
  network.peer_isolation FAIL  probe A reached probe B (expected timeout)
                         → check Docker bridge config; ensure
                           `com.docker.network.bridge.enable_icc=false`
                           on per-session networks.
```

Exit code is non-zero with the failed-check name in
`AGENTCTL_DOCTOR_FAILED` env var if the developer is invoking from a
script.
