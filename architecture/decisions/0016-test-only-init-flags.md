# ADR 0016 — Test-only `agentctl init` flags

- **Status:** Accepted.
- **Date:** 2026-05-10.
- **Deciders:** Architect (M1 implementation).

## Context

The DinD rig (`test/docker-compose.test.yml` + `test/run-e2e.sh`)
exercises `agentctl init` end-to-end against a real Docker daemon, but
M1 also needs Go unit tests and CI smoke tests that run on hosts where
Docker is not available. The init flow has two steps that demand
Docker: the `docker info` reachability probe and the `docker build` of
the session base image. Without an off-switch, neither path is testable
without DinD, which forces every contributor's `go test ./...` to either
pull DinD up or stub out the calls in-process.

We also want unit tests that exercise the real Anthropic / GitHub
validation paths off — the SCAFFOLD §5 envvars for that
(`AGENTCTL_SKIP_GITHUB_PAT_CHECK=1`) already exist, but there is no
counterpart for Anthropic.

## Decision

Add two **test-only** flags to `agentctl init`:

- `--skip-image-build` — skip the `docker build` step. The flag carries
  no effect on `--repair` (which always rebuilds).
- `--skip-docker-check` — skip the `docker info` reachability probe.

Plus one envvar:

- `AGENTCTL_SKIP_ANTHROPIC_VALIDATE=1` — skip the Anthropic key
  validation HTTP call. Mirrors the existing
  `AGENTCTL_SKIP_GITHUB_PAT_CHECK=1`.

These flags / vars are **not** documented in `--help` for end users; the
help text marks them `(test-only)` to make their purpose clear when
someone reads the source. The DinD rig does **not** set them — scenario
01 exercises the real Docker path. They exist purely so that:

1. `go test ./...` can run on a contributor's laptop without DinD.
2. The host build environment for these milestone runs (which has no
   Docker engine) can still smoke-test init/config/doctor against the
   remaining surface.

## Consequences

- The init flow is testable without Docker, at the cost of three
  documented escape hatches that bypass real-world checks.
- The DinD rig stays the source of truth for "init really worked"; the
  test-only flags are explicitly excluded there.
- Future milestones can rely on the same envvar pattern when they add
  validation checks against external services.

## Alternatives considered

- **Stub Docker entirely behind an interface.** The init flow is a few
  shell commands deep into the user's environment; an in-process
  Docker stub would be heavier than the actual production code path.
  Rejected for v1.
- **Host-only flag (only honored inside the test container).** Adds
  complexity (env-detection plus flag) for no real benefit; the flags
  are not exposed in `--help` anyway.

## References

- SCAFFOLD §5.4 (init flags for non-interactive use).
- `test/README.md` (test seams documentation).
- `architecture/install-and-update.md` §2 (init phases).
