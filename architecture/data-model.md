# Data model

## 1. Storage at a glance

| Store | Path | Owner | Purpose |
|---|---|---|---|
| sqlite DB | `~/.local/share/agentctl/agentd.db` | `agentd` | Sessions, MCP registry, usage, schema version. |
| Secrets | `~/.config/agentctl/secrets.json` | `agentctl init` | `ANTHROPIC_API_KEY`, `GITHUB_PAT`. Mode `0600`. |
| Web token | `~/.config/agentctl/web_token` | `agentctl init` | Bearer token for the browser. Mode `0600`. |
| Config | `~/.config/agentctl/config.toml` | `agentctl config` | All tunables (idle timeout, caps, model default, prices, web addr, image pin). Mode `0600`. |
| Registry seed | embedded in binary; optional `/etc/agentctl/registry.seed.toml`, `~/.config/agentctl/registry.seed.toml` | shipped | Initial MCP rows. |
| Image build context | `~/.local/share/agentctl/image/` | `install.sh` | Dockerfile + shim source + entrypoint + config templates. Replaced atomically on every `install.sh` run. |
| Built-in skills | `~/.local/share/agentctl/builtin-skills/` | `install.sh` | Project-curated baseline skills, replaced atomically on `install.sh` run. Read-only to `agentd`. |
| Custom skills | `~/.local/share/agentctl/custom-skills/` | `agentd` | Developer's own skills, mutated via `agentctl skill ...`. Mode `0700` parent, `0600` files. |
| Per-session dir | `~/.local/share/agentctl/sessions/<session_id>/` | `agentd` (created at start) | Volume, control sock, log, events buffer, **per-session composed skills snapshot**. |

`agentd.db` is opened with `journal_mode=WAL`, `synchronous=NORMAL`,
`foreign_keys=ON`, `busy_timeout=5000`. The WAL files live next to the DB
under the same dir.

## 2. sqlite schema

All tables use `INTEGER PRIMARY KEY` autoincrement where row-id is
incidental (`usage`, `events`), and stable string primary keys where the
row is referenced from elsewhere (`sessions.id`, `mcp_registry.name`).
Timestamps are stored as RFC3339Nano strings to keep DB browsers and
debugging tractable; we explicitly do not use `INTEGER` epoch nanos.

```sql
-- Schema version. One row, one column.
CREATE TABLE schema_version (
    version INTEGER NOT NULL PRIMARY KEY
);
INSERT INTO schema_version VALUES (1);

-- Sessions: one row per logical session, including terminated ones.
CREATE TABLE sessions (
    id                  TEXT PRIMARY KEY,                    -- ULID, e.g. "sess_01JFZ..."
    name                TEXT NOT NULL,
    status              TEXT NOT NULL                        -- starting|running|stopped|terminated|error
                          CHECK (status IN ('starting','running','stopped','terminated','error')),
    created_at          TEXT NOT NULL,                       -- RFC3339Nano
    last_activity_at    TEXT NOT NULL,
    terminated_at       TEXT,                                -- set when status=terminated
    container_id          TEXT,                              -- Docker container id, NULL when stopped/terminated
    image_id              TEXT NOT NULL,                     -- locally-built image ID (sha256:...) at create
    network_id            TEXT,                              -- Docker network id, NULL after teardown
    volume_path           TEXT,                              -- abs path; NULL after teardown
    control_sock_path     TEXT,                              -- abs path; NULL after teardown
    skills_snapshot_path  TEXT,                              -- abs path to per-session skills snapshot (composed at start, mounted ro at /skills/); NULL after teardown
    skills_snapshot_hash  TEXT NOT NULL,                     -- sha256 of the snapshot tree at session create; reproducibility pin
    model               TEXT NOT NULL,                       -- e.g. "claude-sonnet-4-6"
    mem_limit_bytes     INTEGER NOT NULL,
    cpu_limit_cores     REAL NOT NULL,
    mcp_set_json        TEXT NOT NULL,                       -- JSON array of MCP names captured at start
    mcp_status_json     TEXT,                                -- JSON {name: "ok"|"unreachable", reason?}
    repos_json          TEXT NOT NULL,                       -- JSON array of {name,url,base_sha,branch}
    session_token       TEXT NOT NULL,                       -- 256-bit random; control-sock auth (§api.md §4.4)
    last_error          TEXT,                                -- short code; set on transitions to error/aborted
    last_seq            INTEGER NOT NULL DEFAULT 0,          -- monotonic event seq emitted to clients
    queue_depth         INTEGER NOT NULL DEFAULT 0,          -- live; updated on enqueue/dequeue
    in_flight           INTEGER NOT NULL DEFAULT 0           -- 0|1; updated on turn start/end
        CHECK (in_flight IN (0, 1))
);
CREATE INDEX idx_sessions_status_activity ON sessions(status, last_activity_at);
CREATE INDEX idx_sessions_status_in_flight ON sessions(status, in_flight, queue_depth);

-- MCP registry: install-wide, edited via R5 surfaces.
-- `transport` is freeform; v1 recognizes `http` (Streamable HTTP) and
-- `sse` (Server-Sent Events). Future transports (e.g., `stdio` with a
-- companion command spec, `websocket`) add without a schema migration.
-- `kind` is also freeform; v1 recognizes `none` (no auth) and
-- `github_pat` (uses the developer's GitHub PAT). Future kinds (e.g.,
-- `oauth_device`, `bearer`) add without a schema migration. Unknown
-- transports or kinds are accepted into the registry but agentd skips
-- them at session start with a `mcp.skipped` event explaining why.
CREATE TABLE mcp_registry (
    name              TEXT PRIMARY KEY,
    url               TEXT NOT NULL,
    transport         TEXT NOT NULL,                          -- v1: http|sse. Freeform; agentd skips unknown at session start.
    kind              TEXT NOT NULL,                          -- v1: none|github_pat. Freeform; agentd skips unknown at session start.
    auth_config_json  TEXT,                                   -- kind-specific JSON; NULL for kinds that need none
    default_enabled   INTEGER NOT NULL DEFAULT 0
                        CHECK (default_enabled IN (0,1)),
    description       TEXT,
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL
);

-- Usage / cost: one row per turn.end, persists past session termination.
CREATE TABLE usage (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id           TEXT NOT NULL REFERENCES sessions(id) ON DELETE NO ACTION,
    turn_id              TEXT NOT NULL,
    at                   TEXT NOT NULL,                      -- RFC3339Nano (informational; ordering uses id)
    model                TEXT NOT NULL,
    input_tokens         INTEGER NOT NULL DEFAULT 0,
    output_tokens        INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens    INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens   INTEGER NOT NULL DEFAULT 0,
    cost_usd             REAL,                               -- NULL when model not in price table at insert
    price_table_version  INTEGER,                            -- version of [pricing] block when computed
    UNIQUE(session_id, turn_id)
);
CREATE INDEX idx_usage_session_at ON usage(session_id, at);
CREATE INDEX idx_usage_at ON usage(at);

-- Per-session event ring (durable backstop for the in-memory event buffer).
-- Rows older than (now() - 24h) and beyond row N for any session are
-- pruned by a daily sweeper. Clients prefer the in-memory buffer; this
-- table is fallback for reconnects within the same session lifetime.
CREATE TABLE events (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id   TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    seq          INTEGER NOT NULL,
    event_id     TEXT NOT NULL,                              -- ULID; matches the wire event id
    kind         TEXT NOT NULL,
    at           TEXT NOT NULL,
    data_json    TEXT NOT NULL,
    UNIQUE(session_id, seq)
);
CREATE INDEX idx_events_session_seq ON events(session_id, seq);
CREATE INDEX idx_events_session_at ON events(session_id, at);

-- Idempotency cache for SendMessage. Rows expire after 5 m.
CREATE TABLE message_idempotency (
    session_id        TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    idempotency_key   TEXT NOT NULL,
    message_id        TEXT NOT NULL,
    accepted_at       TEXT NOT NULL,
    PRIMARY KEY (session_id, idempotency_key)
);
CREATE INDEX idx_idem_accepted ON message_idempotency(accepted_at);

-- Audit/lifecycle log. Distinct from per-session NDJSON file (which is
-- human-tail-able); this is structured for queries.
CREATE TABLE session_lifecycle (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id   TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    at           TEXT NOT NULL,
    event        TEXT NOT NULL,                              -- created|started|stopped|resumed|terminated|errored|reconciled|restarted
    detail_json  TEXT
);
CREATE INDEX idx_lifecycle_session_at ON session_lifecycle(session_id, at);
```

### 2.1 Why the `events` table exists

R6 specifies a small server-side event buffer for replay on reconnect. We
keep a hot in-memory ring (1,000 events / ~5 MB / per session) for
fast-path reconnects, and a warm sqlite-backed fallback in `events`. After
24 hours or 50,000 rows per session (whichever lower), entries are
pruned. Clients that have been disconnected longer than the buffer fall
back to `SnapshotSession`.

### 2.2 Why `mcp_set_json` is JSON, not a join table

R5 says the MCP set is captured at session start and frozen for the
session's lifetime. A join table would let the registry's referential
integrity bleed back into running sessions; we want exactly the opposite
(R5: "Removing a registry entry while a session that uses it is running
does not affect that session"). Storing the captured set as JSON on the
sessions row makes that explicit.

### 2.3 Why `usage.session_id` does not cascade

Cost rows must outlive sessions (R10). We use `ON DELETE NO ACTION` and
never delete sessions rows — only mark them `terminated`. Hard delete is
done only by `agentctl doctor --purge-history` (out of v1 default flow).

## 3. Migrations

`agentd` runs schema migrations on startup before opening the CLI socket.

- Versioning: a single integer in `schema_version`. Each migration is a
  numbered SQL file embedded in the `agentd` binary.
- Forward-only. No automatic downgrades. A v1 → v2 binary upgrade applies
  outstanding migrations in a single transaction.
- If the binary is older than the DB (developer downgrade), `agentd`
  refuses to start with `error{code: "schema_too_new", details: {db_version,
  binary_max_version}}` and prints the exact upgrade command to recover.
- The very first install creates the schema directly at the latest
  version (no migration replay).

Migration file naming: `migrations/0001_initial.sql`,
`migrations/0002_*.sql`, etc. Each runs inside a single transaction.
`PRAGMA user_version` is set in lockstep with `schema_version` for tooling
that relies on either.

## 4. On-disk volume layout

Per-session directory: `~/.local/share/agentctl/sessions/<session_id>/`

```
sessions/<session_id>/
├── volume/                    # bind-mounted into container at /work
│   ├── <repo_basename>/       # one per --repo or in-session clone
│   │   └── …                  # owned by the runtime
│   ├── .history/              # runtime-owned conversation history
│   └── …                      # scratch files the agent creates
├── control/                   # bind-mounted at /run/agentctl/control/
│   └── agentd.sock            # 0660; only this socket; read-write
├── skills/                    # bind-mounted at /skills/ read-only
│   ├── <name>/                # composed at session start from
│   │   └── manifest.json      #   builtin-skills/ + custom-skills/
│   └── …                      #   (custom wins on collision)
├── secrets.env                # 0600; injected via Docker --env-file at run; cleared after start
├── session.json               # 0600; metadata read by the runtime shim
├── agentd.log                 # 0640; per-session NDJSON; rotated by agentd
├── agentd.log.1.gz, .2.gz, …  # rotated history (up to 7)
└── events.ndjson              # 0640; rolling 50 MB cap; backstop for reconnects (see §2.1)
```

The `skills/` snapshot is recreated fresh at every session start (it's
a copy, not a symlink, so live changes to `~/.local/share/agentctl/{builtin,custom}-skills/`
do not retroactively change a running session's view). Its sha256
hash is stored on the session row as `skills_snapshot_hash` for
reproducibility audit.

Key invariants:

- `volume/` is the **only** directory the container can write into via
  `/work`. The `control/` and `secrets.env` mounts are intentionally
  separate.
- `secrets.env` is created with `0600` and deleted after the container is
  started (the env vars are inherited; the file is no longer needed).
- `agentd.log` and `events.ndjson` are written by `agentd`, not the
  container. The container writes only into `volume/`.
- The session dir is removed atomically by `TerminateSession`: rename to
  `sessions/.tombstones/<id>-<ts>/` then `rm -rf` async. Failure to remove
  is non-fatal; doctor cleans tombstones on next start.

### 4.1 Inside the container's `/work`

`/work` is the mount point. The runtime owns everything under it. The
runtime's expected layout:

```
/work/
├── <repo_basename>/    # one per cloned repo
├── .history/           # runtime conversation history (private to runtime)
├── .scratch/           # scratch space the agent uses for intermediate output
└── …
```

`agentd` does not parse `/work/.history`; it treats it as opaque. R8 diff
generation runs inside the container (`git diff` against `.history/repo-bases.json`'s
recorded SHAs) — see `container-and-image.md` §3.

### 4.2 Disk pressure

R7 documents "no enforced cap in v1." We do enforce three soft signals:

1. `agentctl ls --verbose` shows volume size per session.
2. `agentctl doctor` warns when total volume disk usage exceeds 80% of
   the partition.
3. `agentctl stop --idle-over <size>` exists as a power-user knob (not
   surfaced in v1 docs).

## 5. Config file (`config.toml`)

Mode `0600`. All keys have working defaults; the file is created by `init`
with the bare minimum and the rest implicit.

```toml
[agentd]
web_addr = "127.0.0.1:7777"
log_level = "info"

[session]
idle_timeout = "15m"
max_idle = "24h"
mem_limit = "4GiB"
cpu_limit = 2.0
queue_policy = "queue"     # queue | reject

[image]
# Local-build only in v1. There is no remote registry ref. The image is
# built from `~/.local/share/agentctl/image/Dockerfile` by `agentctl init`
# and `agentctl update`. See ADR 0014.
local_tag = "agentctl/session-base:local"
build_context_path = "~/.local/share/agentctl/image"
pinned_id = "sha256:…"          # set by init/update
previous_id = ""                 # for `agentctl update --rollback`

[model]
default = "claude-sonnet-4-6"

[pricing]
# v1 hand-maintained price table; updatable by the developer.
# cost_usd = (input_tokens * input + output_tokens * output
#             + cache_read_tokens * cache_read + cache_write_tokens * cache_write) / 1_000_000

[pricing.tables]
version = 1

[pricing.tables.models."claude-opus-4-7"]
input        = 15.00
output       = 75.00
cache_read   = 1.50
cache_write  = 18.75

[pricing.tables.models."claude-sonnet-4-6"]
input        = 3.00
output       = 15.00
cache_read   = 0.30
cache_write  = 3.75

[pricing.tables.models."claude-haiku-4-5"]
input        = 0.80
output       = 4.00
cache_read   = 0.08
cache_write  = 1.00
```

v1 has no `[network]` block: session containers run with Docker's
default outbound posture (peer-isolated via `enable_icc=false`, but
otherwise unrestricted egress). A future `[network]` block would land
when v2 introduces egress filtering — see `v2-requirements.md` §V2.1.

A change to `[pricing.tables]` increments `version` and applies only to
**future** `usage` rows (R10). Historical rows keep their original
`cost_usd` and `price_table_version`.

## 6. Secrets file

```json
// ~/.config/agentctl/secrets.json — mode 0600
{
  "v": 1,
  "anthropic_api_key": "sk-ant-api03-…",
  "github_pat": "ghp_…",
  "github_pat_kind": "classic"
}
```

`agentd` reads this at every container create (not at startup) so token
rotation via `agentctl init --reset-token …` takes effect on next session.

## 7. Future-proofing notes (for v2; not implemented in v1)

These shape v1 schema choices but are explicitly **not** built:

- **Multi-user** (§16). All paths under `$HOME` and the absence of a
  `user_id` column on every table reflects v1's single-user invariant. To
  add multi-user later we'd add `user_id TEXT NOT NULL` with a default
  backfill — straightforward.
- **Session forking** (§16). The `sessions.id` is a stable opaque ULID;
  no parent/child link exists. A forked session would be a new row with
  a new id and a separate volume.
- **Remote agentd** (§16). Wire formats (`api.md`) are transport-clean —
  a future remote mode would mostly be auth and TLS.

We do not preemptively add columns or tables for these.
