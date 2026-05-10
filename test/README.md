# agentctl test rig

End-to-end tests run inside Docker-in-Docker (DinD) so the daemon and the
session containers it spawns share one Docker engine.

## Prerequisites

- Docker Engine or Docker Desktop with `docker compose` plugin available.
- An Anthropic API key in the environment as `ANTHOPIC_KEY` (the typo is
  real in the test rig, matching the host environment SCAFFOLD §5.6 names).
  `run-e2e.sh` re-exports it as `ANTHROPIC_API_KEY` for the binary.
- Optional: `GITHUB_PAT_TEST` for scenarios that exercise GitHub.

## Running

```bash
ANTHOPIC_KEY=sk-ant-... ./test/run-all.sh
```

This is shorthand for:

```bash
docker compose -f test/docker-compose.test.yml up --abort-on-container-exit --build
```

The compose file boots two services:

- `dind` — `docker:24-dind` daemon listening on `tcp://dind:2375`.
- `tester` — `golang:1.23-bookworm` with `docker-cli`, `jq`, `sqlite3`,
  `python3`. It builds `agentctl` from the mounted source, lays down the
  image build context and the bundled built-in skills under
  `~/.local/share/agentctl/`, and runs every script in
  `test/scenarios/` in lexical order.

A named volume (`sessions-dir`) is shared between `dind` and `tester` so
bind-mount paths line up across the two engines (see SCAFFOLD §5.2).

## Test seams

The CLI honours these env vars when invoked from the rig:

- `AGENTCTL_HOME` — override the user home (default `$HOME`).
- `AGENTCTL_ALLOW_ROOT=1` — let `agentd` run as root inside the test
  container; the rig sets this in compose because the container runs as
  root.
- `AGENTCTL_SKIP_DOCKER_CHECK=1` — bypass the `docker info` reachability
  probe in `agentctl init`. Used only when running M1 unit tests on a
  host without Docker; the DinD rig does not set it.
- `AGENTCTL_SKIP_ANTHROPIC_VALIDATE=1` / `AGENTCTL_SKIP_GITHUB_PAT_CHECK=1`
  — bypass token validation against the upstream APIs. The scenario
  scripts set the GitHub one when `GITHUB_PAT_TEST` is unset.

The init flag `--skip-image-build` exists for unit tests that exercise
the rest of the init flow without paying for a Docker build. The DinD
rig does **not** pass this flag — scenario 01 exercises the real
`docker build` path against `image/Dockerfile` (3-10 min on first run,
seconds on cache hit).

## Scenarios

| # | Script | First milestone where it passes |
|---|---|---|
| 01 | `scenarios/01-init.sh` | M1 |
| 02..09 | (M2+) | future |

Scenario 01 verifies:

1. `agentctl init --foreground` succeeds end-to-end.
2. `~/.config/agentctl/{secrets.json,web_token}` exist with mode `0600`.
3. `~/.local/share/agentctl/agentd.db` exists.
4. `agentctl doctor` exits 0.
5. `GET /healthz` returns `{"docker":{"ok":true,...}}`.
6. Re-running `init` is a no-op (idempotency check from R1).

## Why DinD

`agentd` bind-mounts `~/.local/share/agentctl/sessions/<id>/` into
session containers. With a shared host socket, those containers would
run on a different daemon than the one `agentd` thinks it owns and the
mount paths would not exist. DinD pins one daemon for the test run and
the named `sessions-dir` volume keeps the path the same on both sides.

## What is not tested in M1

- Per-session container creation, message round-trip, fan-out (M2).
- MCP probes, Web UI SPA, multi-client (M3).
- Recovery / sweepers / network isolation (M4).
- Cost rows and diff/export (M5).

These scenarios are scaffolded for later milestones; they will land as
`02-*.sh` through `09-*.sh` when their dependencies ship.
