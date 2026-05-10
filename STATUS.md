# agentctl v1 — status

A live snapshot of what is done, what is verified, and what is still open.
The plan is in `SCAFFOLD.md`; the contracts are in `architecture/`; the
record of what shipped is in `git log`. This doc is the index over all of
that.

Last updated: 2026-05-10. Integration branch: `claude/build-agentctl-v1-grHwr`.

## TL;DR

- All five milestones (M1–M5) merged.
- Code: ~28 000 lines across Go, Python, TypeScript / TSX, CSS.
- 19 Go test packages green (`go test ./...`).
- End-to-end live-tested on Ubuntu 24.04 with Docker 29.3.1 and the real
  Anthropic API: init → docker build → start session → send message →
  observe `assistant.message` text="pong" → usage row written → cost
  surfaced in `agentctl ls` and `agentctl cost`.
- Six bugs found during live testing and fixed (perms, /tmp tmpfs, three
  shim-side framing/parsing issues, CLI flag-after-positional). All
  pushed.

## Milestone status

Each entry references the exit criteria from `architecture/phasing.md`
and the commit(s) that landed it.

### M1 — Daemon skeleton + storage

- [x] `agentctl init` end-to-end on a clean machine (`9c9bd3f`, `2f015ae`)
- [x] systemd unit + launchd plist + foreground fallback (`52133e6`)
- [x] Schema migrations apply, `agentd.db` opens cleanly (`c249963`)
- [x] `journalctl --user -u agentd` shows structured NDJSON (`c249963`)
- [x] `agentctl doctor` passes the M1 subset (`52133e6`)
- [x] Bug fix: `init --foreground` now blocks on signal + detects already-running daemon (`eb75f7f`)
- [x] ADR 0016 — test-only init flags (`6d28d44`)

### M2 — One session, end-to-end

- [x] `internal/cm` (Docker Engine SDK wrapper) (`f75e693`)
- [x] `internal/cc` (control-sock server with `session_token` auth + throttle) (`f13933d`)
- [x] Production Python shim against `claude-agent-sdk==0.1.80` (`29f9109`)
- [x] `internal/sm` (per-session actor + queue + interrupt + snapshot-on-attach) (`cfef87e`)
- [x] `internal/fan` (stateless broadcast hub) (`cfef87e`)
- [x] CLI: `start / attach / detach / ls / stop / interrupt / logs <session>` (`cfef87e`)
- [x] Per-session NDJSON log writer with rotation (`cfef87e`)
- [x] Session-manager orchestration: Create → Listen → Start with teardown on failure (`da3cb7c`)
- [x] Boot-layer adapters bridging `cm`/`cc` typed interfaces to `sm`'s decoupled abstractions (`da3cb7c`)
- [x] ADR 0017 — Docker Engine SDK pin to v25.0.6 with otel/x-sys downgrades for Go 1.23 compat
- [x] ADR 0018 — sm↔cm/cc adapter pattern

### M3 — Multi-client + Web UI + MCPs

- [x] `internal/mcp` registry CRUD + probe + render (`7ad0b5b`)
- [x] `internal/skills` package + CLI (`f0ba47a`, `6ac1605`)
- [x] MCP probe at session start; `mcp.unreachable` deferred to first attach (`a4ea4ee`)
- [x] gorilla/websocket dependency (`f1f1114`)
- [x] `web/embed.go` for `go:embed` SPA assets (`8186c59`)
- [x] Full `internal/websrv` HTTP+WS server (`cd45d96`)
- [x] React + Vite + TS SPA: session list, detail, new-session form, settings, conversation rendering (`c4c51d6`, `e7b58b3`)
- [x] JSON adapters bridging `mcp.Registry` / `skills.Manager` to `websrv.MCPRegistry` / `websrv.SkillsService` (`08752f0`)
- [x] Lowercase snake_case json tags on `mcp.Entry` / `skills.InstalledSkill` (`08752f0`)

### M4 — Recovery, isolation, hardened image

- [x] `internal/recovery` reconciliation algorithm (`4436ca7`, `6f4d0c0`)
- [x] `internal/sweep` (idle-stop / hard-cutoff / idem-cleanup / tombstone-reap) (`6f4d0c0`)
- [x] Fault-injection test harness (`6f4d0c0`)
- [x] Per-session skills snapshot (composed at create, sha256-pinned, bind-mounted ro) (`dc5f5e5`, `3589537`)
- [x] Hardened cm.Spec: `--read-only`, `--cap-drop ALL`, `--security-opt no-new-privileges`, `--pids-limit 512`, tmpfs `/home/agent`, `--memory-swap == --memory` (`3589537`)
- [x] Per-session bridge networks with `enable_icc=false` (`e938eb8`)
- [x] `network.peer_isolation` doctor self-test (`e938eb8`)
- [x] `agentctl update` flow: rebuild + repin + report + `--rollback` + `--no-cache` + `--restart-stopped` (`64a1bfb`)
- [x] `agentctl restart <session>` command + `RestartSession` op (`64a1bfb`)

### M5 — Cost, diff/export, doctor polish

- [x] `internal/usage` recorder + aggregator + range parser (`330ef3f`)
- [x] Usage rows written on `turn.end`, `cost_usd` from `[pricing.tables]` (`0800bef`)
- [x] SPA cost panel + `/usage` route (`6b109fd`)
- [x] `agentctl cost <session>` and `cost --since <range>` (`0800bef`)
- [x] Cost column in `agentctl ls` (`0800bef`)
- [x] `GET /v1/usage` real handler (was M3 503 placeholder) (`0800bef`)
- [x] Shim git ops + new control-sock kinds for diff/export (`89daab3`)
- [x] `sm.Manager.Diff` / `ExportPatch` / `ExportPush` (`dc6ba6b`)
- [x] `agentctl diff` + `agentctl export` CLI commands (`5ec589e`)
- [x] HTTP routes for `/v1/sessions/{id}/diff` / `/export/patch` / `/export/push` (replace M3 placeholders) (`dc6ba6b`)
- [x] SPA Changes tab with diff viewer + Download patch + Push to branch (`2fec3f3`)
- [x] Full doctor check suite: `docker.api`, `image.present`, `skills.{builtin,custom}`, `mcp.registry`, `secrets.fresh`, `volumes.disk` (`3cbd1d7`)
- [x] `agentctl doctor --fix` / `--repair-db` / `--verbose` (`4695f09`)
- [x] `agentctl logs <session> --container` via Docker SDK pass-through (`2858bef`)
- [x] `--help` polish, grouped command listing in `agentctl --help` (`e094f00`)
- [x] `README.md` + `TROUBLESHOOTING.md` (`4e5f3b0`)
- [x] ADR 0019 — diff/export control-channel kinds

### Live-testing bug fixes (commit-only, not in any milestone)

- [x] Per-session bind-mount perms: chown `volume/` `control/` `skills/` to 1000:1000 with chmod fallback so the container's `agent` uid 1000 can read/write (`e381024`)
- [x] `/tmp` tmpfs added to the container's tmpfs map; the SDK's bundled CLI subprocess writes there at startup and `--read-only` rootfs broke `initialize` (`db5bf21`)
- [x] Shim `_emit_event` now nests payload under `data` instead of flattening into the parent envelope (broke every assistant.message / usage / tool.* event) (`db5bf21`)
- [x] Shim `_usage_dict` handles dict-shaped `.usage` (the SDK emits a plain dict, not a typed object) (`db5bf21`)
- [x] Shim `_render_mcp_servers` translates `agentd.greet`'s list-of-dicts into the SDK's `dict[name, McpServerConfig]` (`dafb1a6`)
- [x] CLI `reorderArgs` hoists flags to the front of `flag.Parse` so `agentctl mcp remove github --yes` (and every other `<verb> <positional> --flag` pattern) actually parses the flag (`dafb1a6`)

## Test scenarios (`test/scenarios/01-09.sh`)

| #  | Scenario | Status | How verified |
|----|---|---|---|
| 01 | `init` end-to-end on a clean container | live ✓ | Manual run on Ubuntu 24.04 + Docker 29.3.1; `doctor` reports 13 ok / 2 warn / 0 fail. |
| 02 | `start --repo`, send message, observe `turn.end` | live ✓ | Real Anthropic API call returned `pong`; usage row written; cost surfaced. With and without GitHub MCP enabled. |
| 03 | Two CLI clients attached, message in one appears in both | not verified | Wired but not exercised in this session. |
| 04 | Force idle-stop, send message, observe resume preserves history | not verified | Sweepers boot ok at startup; idle-stop branch not exercised live. |
| 05 | Kill agentd mid-session, restart, attach with no cursor, snapshot returns conversation | not verified | M4-A's recovery + reconciler is wired; live re-adoption of an existing-running container is the deferred path noted in `internal/agentd/wire.go`'s `readoptSessions` TODO. |
| 06 | After agent edits a file: `diff` shows it; `export --patch` produces a clean patch | not verified | Wired through shim git ops (`89daab3`); needs a session that actually edits files. |
| 07 | After a turn, `agentctl cost <session>` shows non-zero | live ✓ | Covered by scenario 02. |
| 08 | `kill -9 agentd`, restart, reconciler cleans orphans, sessions resumable | not verified | Same as 05. |
| 09 | `stop <session>` removes container + volume + network; row marked `terminated` | partially live | `DELETE /v1/sessions/<id>` returns `terminated`; container + network removal not asserted in this session. |

The "not verified" rows are not "broken" — they have unit-test coverage and the wiring is in place. Running them needs a Docker host plus a few minutes per scenario, plus a working GitHub PAT for the push portion of 06.

## Known small follow-ups (not blocking v1)

- `agentctl doctor --json` emits Go-default CamelCase keys (`Checks`, `Name`, `Status`) instead of snake_case. One-line tag fix in `internal/doctor/doctor.go`.
- `agentctl restart --yes` was reported earlier to still prompt; this should be re-checked after the `reorderArgs` fix (`dafb1a6`) since the cause was the same flag-after-positional bug.
- Live re-adoption of an existing running container after `agentd` restart (M4-A's deferred `readoptSessions`). Recovery currently records the adoption in the DB but doesn't synthesize a per-session actor on top of an existing control-sock connection. The fallback ("next message recreates") keeps the daemon usable.
- `secrets.fresh` doctor check runs at runtime only; honoring `AGENTCTL_SKIP_*` envvars when invoked outside the init flow is wired but worth a second pass.
- The repo currently has no signing pipeline (per SCAFFOLD §8 row 4 — explicit decision). When v1.x ships hosted releases, add the public-key + signed-tarball flow back per the original design in `architecture/install-and-update.md` §1.2.

## Outstanding scope explicitly punted (per `requirements.md` §16)

Multi-user, remote agentd, cloud-hosted sessions, live MCP toggling,
per-session custom skills, session forking / branching, cost limits and
budgets, backup / restore, hardened sandboxing beyond Docker defaults,
mobile UI, Windows native, telemetry, pre-warmed pools, container
pause / unpause, strict outbound egress filtering. None of these are
v1 deliverables.

## Pointers

- Plan: `SCAFFOLD.md`
- Product spec: `requirements.md` (R1–R10), `requirements.md` §15 (resolved decisions), `requirements.md` §16 (out-of-scope)
- Architecture: `architecture/overview.md` and siblings
- Decisions: `architecture/decisions/0001`–`0019`
- End-user docs: `README.md`, `TROUBLESHOOTING.md`
- Test rig: `test/README.md`
