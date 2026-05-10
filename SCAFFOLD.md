# SCAFFOLD — agentctl v1 implementation plan

This document is the agreed-upon shape of the v1 implementation **before**
any code is written. It pins down the tech stack, the repo layout, the
milestone branch plan, the sub-agent assignments, and the test rig outline.
It does not redesign anything in `requirements.md` or `architecture/`; if
something here disagrees with those docs, those docs win.

## 1. Tech stack (confirmed — not re-litigating)

| Layer | Choice | Notes |
|---|---|---|
| `agentctl` + `agentd` binary | Go 1.23 | Single binary; subcommand routing on `argv[0]`. `embed` for SPA assets and SQL migrations. Docker SDK is mature. No CGO needed. |
| sqlite | `modernc.org/sqlite` | Pure-Go driver. WAL + `synchronous=NORMAL` + `foreign_keys=ON` + `busy_timeout=5000` per `data-model.md` §1. |
| HTTP / WS server | `net/http` + `gorilla/websocket` | Stdlib `net/http` plus a small WS lib. No heavy framework. |
| Config | `pelletier/go-toml/v2` | De facto standard TOML 1.0 parser. |
| ULID | `oklog/ulid/v2` | For session ids, message ids, turn ids, event ids. |
| Logger | stdlib `log/slog` with NDJSON handler + redactor wrapper | Per `observability.md` §2. |
| Web UI SPA | React 18 + Vite + TypeScript | Embedded in `agentd` via `embed.FS`. No SSR. Routes client-side. |
| Runtime shim (in-container) | Python 3.11 + `claude-agent-sdk==0.1.80` | Per ADR 0014. |
| Testing — Go | `go test`, `testcontainers-go` for Docker integration | Optional. Not required for E2E (which is shell). |
| Testing — SPA | Playwright (browser E2E) | Run inside the Docker test rig. |
| Lint | `gofmt`, `go vet`, `golangci-lint` | Run on every commit. SPA uses `eslint` + `tsc --noEmit`. |

**Justifications already implicit in the docs:** Go 1.23 is named in the
implementation prompt; `modernc.org/sqlite` is named; the SPA tech is named;
the shim language and SDK pin are named in ADR 0014. Anything else above
(slog, ULID, `pelletier/go-toml/v2`, `gorilla/websocket`) is chosen here for
the first time and will be added with a one-line justification when first
imported per the "no new dependencies without justification" rule.

## 2. Repository layout

```
/
├── SCAFFOLD.md                         # this doc
├── README.md                           # M5
├── requirements.md
├── v2-requirements.md
├── architecture/
│   ├── overview.md
│   ├── api.md
│   ├── data-model.md
│   ├── agentd.md
│   ├── container-and-image.md
│   ├── install-and-update.md
│   ├── observability.md
│   ├── security.md
│   ├── phasing.md
│   └── decisions/0001..0015 + 0016+ landed during implementation
├── go.mod
├── go.sum
├── cmd/
│   └── agentctl/main.go                # subcommand router on argv[0] / argv[1]
├── internal/                           # all Go private to the binary
│   ├── version/                        # build-time version string
│   ├── config/                         # config.toml load + watch
│   ├── secrets/                        # secrets.json + web_token + perms
│   ├── store/                          # sqlite + migrations (embed.FS) + repos
│   ├── log/                            # slog NDJSON + redactor + per-session writer
│   ├── ulidgen/                        # central ULID generator
│   ├── api/                            # logical API handlers (transport-agnostic)
│   ├── socksrv/                        # CLI Unix socket server (NDJSON RPC)
│   ├── websrv/                         # HTTP + WS server (auth, CSRF, embed SPA)
│   ├── sm/                             # session manager (per-session actor)
│   ├── cm/                             # container manager (Docker SDK wrappers)
│   ├── cc/                             # control channel server (per-session sock)
│   ├── fan/                            # event fan-out (broadcast channel)
│   ├── mcp/                            # MCP registry CRUD + reachability probe
│   ├── skills/                         # skills snapshot composition + manifest
│   ├── usage/                          # cost recorder + price tables
│   ├── sweep/                          # idle, hard-cutoff, idem cleanup, tombstones
│   ├── recovery/                       # startup reconciliation algorithm
│   ├── doctor/                         # all `agentctl doctor` checks
│   ├── service/                        # systemd unit + launchd plist install
│   ├── update/                         # `agentctl update` flow (image rebuild, repin, rollback)
│   ├── cli/                            # `agentctl` subcommand implementations
│   ├── cliclient/                      # CLI-side socket dialer + RPC stubs
│   ├── ui/                             # `agentctl ui` opener (xdg-open / open / start)
│   └── proto/                          # logical-API request/response types (shared)
├── image/                              # the Docker build context laid down by install.sh
│   ├── Dockerfile
│   ├── entrypoint                      # /usr/local/bin/agentctl-entrypoint
│   ├── shim/                           # Python runtime shim source
│   │   ├── __main__.py
│   │   ├── control.py
│   │   ├── runtime.py
│   │   ├── repos.py
│   │   └── requirements.txt
│   └── config-templates/               # /etc/agentctl/templates/*
├── builtin-skills/                     # 3 placeholder skills shipped in v1: refactor/, tests/, docs/
│   ├── refactor/
│   ├── tests/
│   └── docs/
├── installer/
│   └── install.sh                      # repo-checkout installer (no signing, no CDN in v1)
├── web/                                # React SPA
│   ├── package.json
│   ├── vite.config.ts
│   ├── tsconfig.json
│   ├── index.html
│   └── src/...                         # M3
├── store/migrations/                   # embedded by internal/store via go:embed
│   └── 0001_initial.sql
├── registry/
│   └── registry.seed.toml              # embedded via go:embed
└── test/
    ├── Dockerfile.test
    ├── docker-compose.test.yml
    ├── run-e2e.sh
    ├── run-all.sh                       # convenience: build + compose up
    ├── README.md
    └── scenarios/
        ├── 01-init.sh
        ├── 02-start-and-message.sh
        ├── 03-multi-client-fanout.sh
        ├── 04-idle-resume.sh
        ├── 05-reconnect-snapshot.sh
        ├── 06-diff-and-export.sh
        ├── 07-cost-row.sh
        ├── 08-recovery-after-kill.sh
        └── 09-stop-cleanup.sh
```

Key invariants:

- **One binary, two names.** `cmd/agentctl/main.go` inspects `argv[0]`. If it
  ends in `agentd` it runs the daemon; otherwise it runs CLI dispatch.
  `install.sh` symlinks `agentd → agentctl` in `INSTALL_DIR`.
- **No code in the repo root.** Everything Go is in `cmd/` or `internal/`.
- **Build-context lives in repo at `image/`.** `install.sh` copies that tree
  into `~/.local/share/agentctl/image/`. The CI test rig copies it directly
  (no install.sh run needed in CI).
- **No release-tarball distribution in v1.** `install.sh` is invoked from a
  repo checkout (`bash installer/install.sh`); it builds the binary from
  source and lays down `image/` + `builtin-skills/`. No CDN-hosted tarball,
  no signature verification, no embedded public key. Hosted releases and
  signing are post-v1 concerns.
- **Migrations and SPA assets are embedded.** `internal/store` uses
  `go:embed migrations/*.sql`; `internal/websrv` uses `go:embed` against the
  Vite build output.

## 3. Milestone branch plan

`claude/build-agentctl-v1-grHwr` is the integration branch for v1 per the
session's branch directive — every milestone merges back into it. Each
milestone's work happens on a `milestone/mN` branch off the integration
branch. Within a milestone, parallel sub-agents run in git worktrees that
branch off the milestone branch and merge back via PR (or fast-forward).

```
claude/build-agentctl-v1-grHwr            (integration; all milestones land here)
 ├── milestone/m1                          (M1; sequential, single agent)
 ├── milestone/m2                          (M2; merges from sub-agent worktrees)
 │    ├── m2/container                     (worktree A: cm + image + shim + control auth)
 │    └── m2/actor                         (worktree B: session actor + CLI start/attach/etc.)
 ├── milestone/m3                          (M3; merges from three worktrees)
 │    ├── m3/mcp                           (worktree B: registry CRUD + probes)
 │    ├── m3/web                           (worktree A: HTTP/WS + auth + endpoints)
 │    └── m3/spa                           (worktree C: React SPA)
 ├── milestone/m4                          (M4; merges from three worktrees)
 │    ├── m4/recovery                      (worktree A: reconciler + sweepers + fault rig)
 │    ├── m4/skills                        (worktree B: skill CLI + snapshot + hardened image)
 │    └── m4/network                       (worktree C: per-session networks + doctor + update flow)
 └── milestone/m5                          (M5; merges from three worktrees)
      ├── m5/cost                          (worktree A: usage table writes + cost UI/CLI)
      ├── m5/diff                          (worktree B: in-shim git + diff/export endpoints)
      └── m5/doctor                        (worktree C: full doctor + logs polish + README)
```

A milestone merges into `claude/build-agentctl-v1-grHwr` **only** when its
exit criteria in `phasing.md` pass under the Docker test rig. The
sub-milestone worktree branches (`mN/*`) are local-only — they never push
to origin separately; only the integration branch does.

### Merge order within each milestone

- **M2:** A (container) → B (actor). B can stub the control sock until A
  lands, but its acceptance test depends on A's contract.
- **M3:** B (mcp) → A (web) → C (spa). C depends on A's endpoint shapes;
  A depends on B's registry CRUD.
- **M4:** parallel; merge in any order. Recovery's fault-injection touches
  the same code paths as the network and skills work, so a final integration
  pass on `feature/m4` rebases all three.
- **M5:** parallel; merge in any order.

## 4. Sub-agent assignments per milestone

Each sub-agent is launched via `Agent` with `subagent_type=general-purpose`
and `isolation=worktree`. Each prompt includes:

- **Scope:** what files / packages it owns, what it must not touch.
- **Inputs:** the relative paths of the docs it should read.
- **Exit criteria:** the exact bullets from `phasing.md` it has to satisfy.
- **Test scenario(s):** which `test/scenarios/NN-*.sh` it must make pass.

### M1 — Daemon skeleton + storage (no sub-agents)

One foreground agent does all of M1. Foundation work that everything else
depends on:

- Repo scaffolding (`go.mod`, dirs above, `Makefile` targets `build`,
  `lint`, `test`, `e2e`).
- `internal/version`, `internal/config` (toml load + write + perm fix-ups),
  `internal/secrets` (read/write + perms), `internal/log` (NDJSON slog +
  redactor + per-session writer skeleton), `internal/store` (sqlite open +
  migrations + minimal repo for sessions + mcp_registry).
- `internal/api` shape with `Health` only.
- `internal/socksrv` (Unix-sock NDJSON server with `Health`).
- `internal/websrv` skeleton — only `/healthz` (no auth) + bearer-token
  middleware stub.
- `internal/cli` for `agentctl init`, `agentctl update` (image rebuild),
  `agentctl config get|set`, `agentctl doctor` (M1 subset:
  `bin.versions`, `fs.perms`, `db.integrity`, `service.active`,
  `agentd.health`, `docker.reachable`, `image.built`, `image.build_context`).
- `internal/service` to write systemd unit + launchd plist + foreground
  fallback.
- The `image/` build context (Dockerfile + Python shim source — shim has
  hello/greet only at this stage; it doesn't speak to the SDK yet).
- `installer/install.sh` (signature step stubbed for M1 — laid down for M5
  polish).
- `test/` rig (Dockerfile.test, docker-compose.test.yml, run-e2e.sh, scenario
  01).

**Exit criteria covered:** all M1 bullets in `phasing.md`. Test scenario 01
passes.

### M2 — One session, end-to-end (2 sub-agents)

| Sub-agent | Worktree | Scope |
|---|---|---|
| **A — Container & image** | `feature/m2-container` | Finish the Dockerfile to actually run the SDK shim. Implement `internal/cm` (Docker SDK wrapping `create`, `start`, `stop`, `kill`, `rm`, network `create`/`remove`, with all flags from `container-and-image.md` §2). Implement `internal/cc` (per-session control sock acceptor with `session_token` auth). Implement the Python shim end-to-end (talks to control sock, drives `claude-agent-sdk`, emits all `runtime.event` kinds, handles `agentd.message`/`agentd.interrupt`/`agentd.shutdown`/`agentd.snapshot_request`). |
| **B — Session actor & CLI** | `feature/m2-actor` | Implement `internal/sm` (per-session actor: queue, in_flight, subscribers, fan-out — all in-memory, stateless reconnects via control-sock snapshot). Implement `internal/fan` (stateless broadcast channel). Implement CLI commands: `start`, `attach`, `detach`, `ls`, `stop`, `interrupt`, `logs <session>`. Per-session NDJSON log writer with rotation. Add `Health`'s `docker.ok` check. |

A blocks B on the control-channel contract; B uses a fake `cm` and a stub
control sock until A's branch is ready. Final merge order: A → B.

**Exit criteria covered:** M2 bullets. Test scenarios 02 and 09 pass.

### M3 — Multi-client + Web UI + MCPs (3 sub-agents)

| Sub-agent | Worktree | Scope |
|---|---|---|
| **A — Web HTTP/WS** | `feature/m3-web` | Implement `internal/websrv` fully: bearer token middleware, Origin / Sec-Fetch-Site enforcement, token-handoff loader page (`/`), all `/v1/*` routes from `api.md` §3.2, WS `AttachStream` upgrade with subprotocol `agentctl.v1`. Embed the SPA build artifacts via `go:embed`. |
| **B — MCP registry** | `feature/m3-mcp` | Implement `internal/mcp`: registry CRUD (CLI + API ops), reachability probe (1.5s/probe, 3s ceiling, parallel, per ADR 0005), MCP-config rendering at session start (only selected MCPs, with `kind`-derived headers), skills manifest fetch from running container. CLI: `agentctl mcp {list,add,remove,set-default}`. |
| **C — SPA** | `feature/m3-spa` | React + Vite + TS app under `web/`. Loader page that extracts `#t=` to cookie. Routes: session list, session detail (conversation, message input, MCP panel, Stop button), new-session form (name, MCP checkboxes, `--repo`s), Settings → MCPs CRUD. WS attach with snapshot-first then live-tail rendering. Skill autocomplete on `/`. |

Merge order: B → A → C.

**Exit criteria covered:** M3 bullets. Test scenarios 03 and 05 pass.

### M4 — Recovery, isolation, hardened image (3 sub-agents)

| Sub-agent | Worktree | Scope |
|---|---|---|
| **A — Recovery & sweepers** | `feature/m4-recovery` | Implement `internal/recovery` per `overview.md` §7. Implement `internal/sweep`: idle-stop, hard-cutoff, `idem_cleanup`, tombstone reaping. Build a fault-injection harness (table-driven: kill agentd at every annotated state transition). Make `kill -9 agentd` then restart leave all sessions resumable. |
| **B — Skills & hardened image** | `feature/m4-skills` | Implement `internal/skills`: per-session snapshot composition (cp from `builtin-skills/` + `custom-skills/`, custom wins, sha256 the tree, store on session row). CLI: `agentctl skill {list,new,add,edit,remove,validate,show,export,import}`. Tighten the Dockerfile: `--read-only`, `--cap-drop ALL`, `--pids-limit 512`, `--security-opt no-new-privileges`, tmpfs `/home/agent`. |
| **C — Per-session networks & update flow** | `feature/m4-network` | Implement per-session bridge network creation (`enable_icc=false`) in `cm`. Implement the doctor `network.peer_isolation` self-test (two probe containers, expect connect timeout). Implement the full `agentctl update` flow: rebuild + repin + report + rollback (`update --rollback`) + `update --restart-stopped`. `agentctl restart <session>`. |

**Exit criteria covered:** M4 bullets. Test scenarios 04 and 08 pass; the
network self-test passes inside DinD.

### M5 — Cost, diff/export, doctor polish (3 sub-agents)

| Sub-agent | Worktree | Scope |
|---|---|---|
| **A — Cost** | `feature/m5-cost` | Wire `internal/usage`: insert `usage` row on each `runtime.event{kind=usage}` emitted at `turn.end`. Compute `cost_usd` from `[pricing.tables]`. Cost panel in SPA (per-turn timeline, model breakdown). CLI: `agentctl cost <session>`, `agentctl cost --since <range>`, cost column in `agentctl ls`. |
| **B — Diff & export** | `feature/m5-diff` | Shim-side git ops: `agentd.diff_request`, `agentd.export_patch`, `agentd.export_push`. CLI: `agentctl diff`, `agentctl export --patch`, `agentctl export --push`. SPA: "Changes" tab + "Download patch" / "Push to branch" buttons. |
| **C — Doctor polish & docs** | `feature/m5-doctor` | All remaining doctor checks: `docker.api`, `image.present`, `skills.builtin`, `skills.custom`, `mcp.registry`, `secrets.fresh`, `volumes.disk`. `agentctl logs --daemon`, `agentctl logs <session> --container`. `--help` text for every command. README + troubleshooting. Polish `agentctl ls --verbose`. |

**Exit criteria covered:** M5 bullets. Test scenarios 06 and 07 pass.

## 5. Test rig outline

The product manages Docker containers, so the E2E rig must run inside Docker
with a working Docker engine. We use **Docker-in-Docker (DinD)**, not the
host socket — DinD keeps `agentd`'s host paths and the spawned-container
paths consistent (R7 mounts under `~/.local/share/agentctl/sessions/<id>/`
must exist on the same daemon).

### 5.1 Components

`test/Dockerfile.test`:

- Base: `golang:1.23-bookworm`.
- Adds: `docker-cli`, `python3` + `python3-pip` (for asserting against
  shim version, not for running the shim itself), `sqlite3`, `jq`, `curl`,
  `git`, `nodejs` + `npm` (for SPA build during E2E), Playwright deps
  (M3+).
- Copies in the repo source.
- Default `CMD` is `/work/test/run-e2e.sh`.

`test/docker-compose.test.yml`:

```yaml
services:
  dind:
    image: docker:24-dind
    privileged: true
    environment:
      DOCKER_TLS_CERTDIR: ""             # plain HTTP on tcp://dind:2375
    command: [ "--host=tcp://0.0.0.0:2375", "--host=unix:///var/run/docker.sock" ]
    volumes:
      - sessions-dir:/root/.local/share/agentctl
    networks: [ default ]

  tester:
    build:
      context: ..
      dockerfile: test/Dockerfile.test
    depends_on: [ dind ]
    environment:
      DOCKER_HOST: tcp://dind:2375
      ANTHROPIC_API_KEY: ${ANTHOPIC_KEY}
      GITHUB_PAT_TEST: ${GITHUB_PAT_TEST:-}
    volumes:
      - sessions-dir:/root/.local/share/agentctl
      - ../:/work
    working_dir: /work
    command: [ "bash", "test/run-e2e.sh" ]
    networks: [ default ]

volumes:
  sessions-dir: {}

networks:
  default: {}
```

The `sessions-dir` volume is shared between `dind` and `tester` so a path
that `agentd` (running in `tester`) writes is the same path the spawned
container (running in `dind`) sees on its mounts.

`test/run-e2e.sh` orchestration (skeleton):

```bash
#!/usr/bin/env bash
set -euo pipefail

# 1. wait for DinD
until docker info >/dev/null 2>&1; do sleep 1; done

# 2. alias the API key
export ANTHROPIC_API_KEY="${ANTHROPIC_API_KEY:-${ANTHOPIC_KEY:-}}"
[[ -n "$ANTHROPIC_API_KEY" ]] || { echo "ANTHROPIC_API_KEY unset"; exit 64; }

# 3. build agentctl
( cd /work && go build -o /usr/local/bin/agentctl ./cmd/agentctl )
ln -sf /usr/local/bin/agentctl /usr/local/bin/agentd

# 4. lay down image build context (in real life install.sh does this)
mkdir -p ~/.local/share/agentctl/image
cp -r /work/image/. ~/.local/share/agentctl/image/

# 5. non-interactive init
agentctl init \
  --anthropic-key "$ANTHROPIC_API_KEY" \
  --github-pat "${GITHUB_PAT_TEST:-test-pat-not-validated}" \
  --no-import-claude-skills \
  --foreground &                             # foreground fallback in CI
AGENTD_PID=$!
trap "kill $AGENTD_PID 2>/dev/null || true" EXIT

# 6. wait for agentd healthy
until curl -sf http://127.0.0.1:7777/healthz >/dev/null; do sleep 0.5; done

# 7. run scenarios in order
for scenario in /work/test/scenarios/*.sh; do
  echo "=== running $(basename "$scenario") ==="
  bash "$scenario"
done

echo "all scenarios passed"
```

### 5.2 Why DinD over socket mount

- agentd bind-mounts `~/.local/share/agentctl/sessions/<id>/{volume,control,skills}/`
  into spawned containers. With socket mount the spawned containers are
  siblings on the host's Docker daemon, so the bind-mount paths don't
  exist there. DinD makes the daemon and agentd share one filesystem view
  via the named volume.
- M4's per-session bridge networks (`enable_icc=false`) need a clean
  Docker namespace; DinD gives one per test run.
- `docker compose down -v` resets the daemon state between runs.

### 5.3 systemd in the test container

Skipped. `agentctl init`'s service install fails inside a container; the
foreground fallback (M1 deliverable) is what the test rig exercises. We
document this in `test/README.md`.

### 5.4 Init flags for non-interactive use (added in M1)

- `--anthropic-key <key>` — bypass the Anthropic key prompt.
- `--github-pat <pat>` — bypass the GitHub PAT prompt.
- `--no-import-claude-skills` — skip the Claude Code skills import prompt.
- `--foreground` — skip systemd / launchd install; run `agentd` in
  foreground for the current shell. Distinct from the natural foreground
  fallback that triggers when service install fails — `--foreground` is
  the explicit "I want foreground, even though service install would have
  worked" signal CI uses.

These are all already covered by R1's "init flow" intent; we make them
flags rather than env vars so they show up in `--help`.

### 5.5 Required scenarios (must all pass for v1 done)

| # | Scenario | Verifies | First milestone where it passes |
|---|---|---|---|
| 01 | `init` end-to-end on a clean container | R1 | M1 |
| 02 | `start --repo`, send a message, observe `turn.end` | R2, R3 | M2 |
| 03 | Two CLI clients attached, message in one appears in both | R4 | M2 (CLI fan-out); M3 (browser tab + CLI) |
| 04 | Force idle-stop (set `idle_timeout=10s`), send a message, observe resume preserves history | R2, R6 | M4 |
| 05 | Kill agentd mid-session, restart, attach with no cursor, snapshot returns conversation | R6, ADR 0015 | M4 |
| 06 | After agent edits a file: `diff` shows it; `export --patch` produces a clean patch | R8 | M5 |
| 07 | After a turn, `agentctl cost <session>` shows non-zero | R10 | M5 |
| 08 | `kill -9 agentd`, restart, reconciler cleans orphans, sessions resumable | R6 | M4 |
| 09 | `stop <session>` removes container + volume + network; row marked `terminated` | R2, R7 | M2 (stop); M4 (network) |

Scenarios 02, 03, 06, 07 require the Anthropic API key to actually drive a
real model turn. Scenarios 06's `--push` portion and any GitHub MCP
reachability probe are gated on `GITHUB_PAT_TEST`; absent the PAT, those
sub-checks are skipped (not failed).

### 5.6 Environment variables

The host environment provides the Anthropic API key as `ANTHOPIC_KEY`
(the typo is real). `run-e2e.sh` aliases it to `ANTHROPIC_API_KEY`, which
is what every code path (init flag, container env injection) expects. The
key is **never** committed and **never** baked into images;
`docker-compose.test.yml` references `${ANTHOPIC_KEY}` from the shell.

For tests that need a real GitHub PAT (R8 push tests, GitHub MCP probe),
use `GITHUB_PAT_TEST`. Scenarios skip those sub-checks (don't fail) when
absent.

## 6. Definition of done

A milestone is done when:

1. All exit criteria in `phasing.md` for that milestone are checked off.
2. The relevant scenarios in `test/scenarios/` pass under
   `docker compose -f test/docker-compose.test.yml up --abort-on-container-exit`.
3. `gofmt`, `go vet`, `golangci-lint run` are clean.
4. The PR body lists each exit criterion with a checkbox and references
   the scenario script that verified it.

The whole product is done when:

1. M1–M5 are merged into `main`.
2. `bash test/run-all.sh` exits 0 on a fresh Docker host with `ANTHOPIC_KEY`
   set.
3. `agentctl doctor` inside the test container reports all checks green.

## 7. Conventions (re-stated for sub-agents)

- **Commit messages:** imperative, one-line subject; body explains *why*.
- **One concern per PR.** No drive-by refactors.
- **Comments only where the *why* is non-obvious.** Identifiers explain
  *what*.
- **No new dependencies without a one-line justification in the PR body.**
- **ADRs are binding.** New decisions during implementation get a new ADR
  (0016+) before the code lands.
- **Sub-agent PRs must include the test scenario that proves their slice.**
  When a sub-agent finishes, the parent verifies the diff in the worktree
  before merging — agent summaries describe intent, not what shipped.

## 8. Decisions made during scaffold review

Resolutions for the points the docs left open. These are binding for v1
unless a new ADR (0016+) is filed.

| # | Topic | Decision |
|---|---|---|
| 1 | WebSocket library | `gorilla/websocket`. |
| 2 | TOML library | `pelletier/go-toml/v2`. |
| 3 | SPA package manager | `npm` (default Node tooling, no extra CI install). |
| 4 | `install.sh` distribution | **Repo-checkout install only.** v1 ships no hosted release tarball, no CDN, no signature verification, no embedded public key. `installer/install.sh` is invoked from a local checkout (`bash installer/install.sh`); it builds the binary from source, lays down `image/` + `builtin-skills/`, drops the binary into `INSTALL_DIR`. Hosted releases + signing are deferred to v1.x. |
| 5 | Built-in skills | Three placeholder skills shipped: `refactor/`, `tests/`, `docs/` (matches the sample in `observability.md` §9). Lightly defined; primarily exercise the manifest format and `/help` rendering. Polish post-v1. |
| 6 | `init --foreground` flag | Add in M1 alongside `--anthropic-key`, `--github-pat`, `--no-import-claude-skills`. Forces foreground mode (skips systemd / launchd install) for CI use. Distinct from the existing fallback that triggers on service-install failure. |
| 7 | Integration branch | `claude/build-agentctl-v1-grHwr` is the v1 integration branch. Each `milestone/mN` rebases onto it before merge; sub-milestone `mN/*` worktree branches stay local. |
