# Delivery phasing

v1 is broken into five sequenced milestones. Each is independently
testable and ends in a demonstrable cut. We sequence so the riskier or
foundational pieces land first.

| # | Milestone | Goal in one sentence |
|---|---|---|
| M1 | Daemon skeleton + storage | `agentd` boots, persists, accepts CLI socket calls, runs migrations. |
| M2 | One session, end-to-end | A developer can `init`, `start`, send messages, `stop`. |
| M3 | Multi-client + Web UI parity | The Web UI matches the CLI 1:1 with auth + fan-out. |
| M4 | Recovery + hardening | Reboots, restarts, idle stops, network policy, hardened image. |
| M5 | Cost, diff/export, polish | R10 cost, R8 diff/export, doctor, phasing-out of debug-only seams. |

Each milestone covers a subset of R1–R10 acceptance criteria. The table
in §6 maps every requirement to its landing milestone.

## M1 — Daemon skeleton + storage

### Scope

- Repo layout, build pipeline, signed releases.
- `agentd` binary that:
  - parses `config.toml`,
  - opens / migrates `agentd.db` (data-model.md §3),
  - serves `/healthz` and a stub `Health` over the CLI socket,
  - logs to journald (Linux) and a file (macOS) per observability.md §2.
- `agentctl` CLI binary that:
  - implements `init` with Docker check, **local image build**
    (`docker build` against `~/.local/share/agentctl/image/`), token
    validation, perm fix-ups, registry seed apply, system-service
    install, foreground fallback,
  - implements `agentctl update` (re-build image, repin id),
  - implements `agentctl config get|set`,
  - implements `agentctl doctor` (subset: `bin.versions`, `fs.perms`,
    `db.integrity`, `service.active`, `agentd.health`,
    `docker.reachable`, `image.built`, `image.build_context`).
- systemd `--user` unit and launchd plist.

### Out of scope (M1)

Containers, sessions, MCPs, fan-out, Web UI.

### Exit criteria

- `agentctl init` on a clean Linux + macOS box completes in <2 min
  (excluding image pull).
- `systemctl --user is-active agentd` returns `active`; reboot brings
  it back automatically.
- `agentctl doctor` passes all M1 checks.
- Schema migrations applied; `agentd.db` opens cleanly on every boot.
- `journalctl --user -u agentd` shows structured NDJSON.

### Acceptance criteria covered

- R1: most of (init flow, secrets, service install, idempotency, perm
  fix-up). The Web UI URL/MCP-list portion of the summary is faked.
- R6: schema, basic DB persistence.

### Risks

- **System service install variance** across distros. *Mitigation:*
  test on Ubuntu 22.04 / 24.04, Debian 12, Fedora 40, plus macOS 13/14
  in CI. Foreground fallback as the safety net.

## M2 — One session, end-to-end

### Scope

- Container manager (`cm`): create/start/stop/remove via Docker SDK,
  with all flags from container-and-image.md §2.
- Base image v0 (no skills yet; just the runtime SDK + shim).
- Runtime shim (Python; uses `claude-agent-sdk`): connects to control
  sock, says `runtime.hello`, accepts `agentd.greet`, configures the
  SDK with model/MCPs/permission_mode=bypass, and translates SDK
  events into `runtime.event` frames on the control sock.
- Session manager actor: per-session mailbox, queue, in_flight,
  `SendMessage`, `Interrupt`, fan-out (in-memory).
- CLI: `start`, `attach`, `detach`, `ls`, `stop`, `interrupt`,
  `logs`.
- Per-session log file with rotation.
- Control-channel auth (session_token).

### Out of scope (M2)

Multiple clients, Web UI, MCPs (single hard-coded "no MCPs" path),
recovery from agentd restart, R8 diff/export, R10 cost.

### Exit criteria

- `agentctl start` lands a running container and an attached event
  stream within the cold-start budget on a developer-class laptop.
- `agentctl interrupt` cancels mid-turn within 1s p95.
- `agentctl stop` removes container + volume + DB row.
- The attached client sees the streaming response in real time.

### Acceptance criteria covered

- R2 most of: cold start, idle resume (basic), `ls`, `stop`. (Idle
  sweepers live but minimal; full coverage in M4.)
- R3 most of: env injection, secret wiring, runtime startup with
  `--dangerously-skip-permissions`. Skills delivered in M4.
- R4 partial: CLI half of R4 surface; multi-client fan-out works
  among multiple `attach` instances on the same session.

### Risks

- **Cold-start budget overshoot.** *Mitigation:* measure each phase
  (image pull / network / container / shim ready / repo clone)
  separately; if any phase regresses we know which.
- **Runtime headless / streaming flag set.** *Mitigation:* pin the
  runtime version in M2's image; iterate the flags with the runtime
  team if the headless surface is incomplete.

## M3 — Multi-client, Web UI, MCPs

### Scope

- Web HTTP/WS server (`web` module): static SPA, bearer token, Origin
  enforcement, all `/v1/*` endpoints from api.md §3.2.
- SPA: session list, session detail with conversation, MCP settings,
  cost panel placeholder, Stop button, message input, attach to a
  session.
- MCP registry: CRUD over CLI and Web; injection at session start;
  per-session `mcp_set`; reachability soft-warn probe.
- Skills manifest fetch + `/help` autocomplete in both clients (R9).
- `agentctl mcp list|add|remove|set-default`.
- Multi-attach fan-out (browser tab + CLI on the same session at
  once).

### Out of scope (M3)

Recovery, network policy hardening, R8 diff/export, R10 cost rows.

### Exit criteria

- A developer can `start` from CLI, see the new session in the Web UI
  immediately, send a message from the UI, see it echo in the CLI.
- Two browser tabs on the same session show identical streams.
- `Interrupt` from any one client cancels the in-flight turn.
- Adding an MCP via either client is reflected in the other within
  seconds.
- `/help` lists the per-session skills snapshot (built-in + custom) in both clients.
- `Origin` mismatch is rejected with `403`.

### Acceptance criteria covered

- R3 fully (skills in image, manifest exposure).
- R4 fully (parity, multi-client, fan-out).
- R5 fully (registry, per-session selection, edit-while-running
  invariants).
- R9 fully.

### Risks

- **CSRF / Origin-check bugs.** *Mitigation:* security review, a
  test suite that exercises bad-Origin / missing-token combinations.
- **Token handoff UX.** *Mitigation:* `agentctl ui` is the canonical
  open path; a clear error message when the SPA loads without a token
  steers users back.

## M4 — Recovery, isolation, hardened image

### Scope

- Recovery / reconciliation algorithm (overview.md §7).
- Idle-stop sweeper, hard-cutoff sweeper, idem_cleanup, tombstone_reap.
- Per-session Docker bridge networks with `enable_icc=false`
  (peer-isolation; no iptables manipulation in v1).
- Peer-isolation self-test in `agentctl doctor`.
- Image v1 (locally built per ADR 0014): no skills layer; `--read-only`
  rootfs, capability drops, pids-limit. Built from the bundled
  Dockerfile via `agentctl init` / `agentctl update`. Skills
  bind-mounted at session start.
- `agentctl skill {list,new,add,edit,remove,validate,show,export}` CLI
  surface (R9). Per-session skills-snapshot composition + bind-mount
  + `skills_snapshot_hash` recording.
- Backpressure + rate limits on control channel.
- Image update path: `agentctl update`, `update --report`, `restart
  <session>`, `update --rollback`, `update --restart-stopped`.

### Out of scope (M4)

R10 cost, R8 diff/export.

### Exit criteria

- `kill -9 agentd` then restart: all sessions resumable; reconciler
  cleans orphaned containers/networks.
- Reboot during a session: same behavior on next login.
- Peer-isolation self-test passes (two probes on two session networks
  cannot reach each other).
- Idle-stop fires on a session whose `last_activity_at` ages past 15m
  (test by setting `idle_timeout = "10s"` and observing).
- Hard-cutoff fires on a session whose `last_activity_at` ages past
  24h, including in-flight cancellation.
- `agentctl update` updates the pinned digest and prints the
  staleness report; `restart` adopts the new image preserving the
  volume.

### Acceptance criteria covered

- R2 fully (idle-stop, hard-cutoff, error states, recovery).
- R6 fully (continuity across all the listed scenarios).
- R7 fully (filesystem, processes, secrets, network, resource caps,
  trust boundary).

### Risks

- **Reconcile edge cases.** *Mitigation:* fault-injection test suite
  (kill agentd at every annotated state transition; verify the
  expected outcome).
- **Per-session network cleanup.** A session that fails to tear down
  cleanly leaves a Docker network behind. *Mitigation:* the reconciler
  removes orphan networks on every boot (overview.md §7).

## M5 — Cost, diff/export, doctor polish

### Scope

- `usage` table writes on `turn.end`; `cost_usd` computed from
  `[pricing.tables]`.
- Cost panel in Web UI, `agentctl cost`, `agentctl ls` cost column.
- Diff and export (R8): in-shim git operations; `agentctl diff`,
  `agentctl export --patch`, `agentctl export --push`. UI buttons.
- Full `agentctl doctor` checks (the rest of the table in
  install-and-update.md §5.1).
- `agentctl logs --daemon`, `--container`, polish.
- Documentation pass: `--help` text, README, troubleshooting.

### Exit criteria

- A turn that the API reports as N tokens at price P generates a
  `usage` row matching N×P.
- Aggregate `agentctl cost --since 7d` matches the SUM over `usage`
  rows.
- `agentctl diff` matches `git diff` inside the container against the
  recorded base.
- `agentctl export --push` succeeds for a writable PAT and surfaces a
  meaningful error on rejection.
- `agentctl doctor` runs every check, reports correctly, and `--fix`
  resolves common issues idempotently.

### Acceptance criteria covered

- R8 fully.
- R10 fully.
- R1, R3, R4, R5, R6, R7 polish (help text, doctor coverage).

### Risks

- **Per-model price drift.** *Mitigation:* document the
  `[pricing.tables]` block and supply a release-time refresh; surface
  unknown-model warnings in UI.
- **`git push --force` accidents.** Per §15.1 documented as the
  developer's responsibility, but we add `--push` confirmation when
  the working tree includes commits not on the recorded base.

## 6. Requirement-to-milestone matrix

| Req | M1 | M2 | M3 | M4 | M5 |
|---|---|---|---|---|---|
| R1 | bulk | — | — | — | polish |
| R2 | — | bulk | — | full | — |
| R3 | — | partial | full | — | — |
| R4 | — | partial | full | — | — |
| R5 | — | — | full | — | — |
| R6 | partial | partial | — | full | — |
| R7 | — | partial | — | full | — |
| R8 | — | — | — | — | full |
| R9 | — | — | full | — | — |
| R10 | — | — | — | — | full |

"bulk" = most acceptance criteria; "partial" = some acceptance
criteria; "full" = all.

## 7. CI investment timeline

| Milestone | Test surface |
|---|---|
| M1 | Unit tests on schema migrations, config, secrets file perms; smoke test for `init` on Linux/macOS runners. |
| M2 | Container creation integration tests against real Docker on Linux; mock-Docker on macOS. Actor mailbox unit tests. |
| M3 | E2E browser tests (Playwright) hitting localhost; multi-client fan-out tests; CSRF tests. |
| M4 | Reconcile fault-injection harness; peer-isolation self-test in Linux + macOS CI; local image build smoke test on multiple distros (Ubuntu 22.04/24.04, Debian 12, Fedora 40, macOS Docker Desktop) in release CI. |
| M5 | Cost computation parity tests; diff/export E2E; `agentctl doctor --fix` E2E. |

## 8. Cuttable scope (if we slip)

If a milestone overruns, here's what we'd defer to v1.1 in priority
order:

1. `agentctl logs --container` (Docker logs proxy is convenient but
   not on the critical path).
2. `agentctl update --rollback` (rollback by editing config.toml and
   `restart` works).
3. `agentctl doctor --fix` for non-trivial repairs (the read-only
   doctor still works; user runs `init --repair`).

Items we will **not** cut (would block v1):

- R6 reboot recovery.
- R7 isolation guarantees on Linux.
- R10 cost rows.
- §15.1 invariant (permission-prompting-off + container-as-boundary).
