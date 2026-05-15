# ADR 0017 — Use the official Docker Engine SDK for the container manager

- **Status:** Accepted.
- **Date:** 2026-05-10.
- **Deciders:** M2 sub-agent A.

## Context

`internal/cm` (the container manager landed in M2) drives container
lifecycle (`create`, `start`, `stop`, `kill`, `remove`, `inspect`,
`info`) plus M4-scope network create/remove. SCAFFOLD §1 names "Docker
SDK is mature" but did not pin a specific module version. The container
parameters in `architecture/container-and-image.md` §2 list flags that
correspond to specific Engine API fields (mounts, resources, restart
policy, network mode), so we need a typed client rather than shelling
out to `docker`.

## Decision

Add `github.com/docker/docker v25.0.6+incompatible` (the Engine API
SDK) as the sole new direct dependency for `internal/cm`. The
package is wrapped behind a small `cm.DockerClient` interface so unit
tests use a fake and don't require Docker to be present.

To avoid pulling Go 1.25 transitively (the latest otelhttp / otel /
golang.org/x/* releases require it), the indirect dependencies are
pinned at the highest versions still compatible with `go 1.23` per
SCAFFOLD §1:

- `go.opentelemetry.io/otel*` v1.20.0
- `go.opentelemetry.io/contrib/.../otelhttp` v0.46.0
- `golang.org/x/sys` v0.30.0
- `golang.org/x/time` v0.5.0
- `github.com/docker/go-connections` v0.5.0 (newer drops `DialPipe`)

All three downgrades are indirect; nothing in `internal/*` imports any
otel package. They exist purely because the Docker client transitively
references them for tracing instrumentation.

## Consequences

- M2-A and later milestones use a typed client; type errors surface at
  compile time, not at runtime as JSON parse failures.
- The container-creation request body has a single render path
  (`cm.BuildCreateRequest`) that maps `Spec` → SDK structs; tests
  pin the M2 shape and the M4 hardening additions can be made by
  flipping flags on the `CreateRequest` struct.
- `agentd`'s binary size grows by the docker SDK transitive surface
  (~6–8 MiB). Acceptable for a daemon that already embeds the SPA.
- Future Go bump: when SCAFFOLD authorizes `go 1.25+`, the otel /
  x/sys pins above can be lifted with no code changes. Until then,
  `go.mod` reflects the compat floor.

## Alternatives considered

- **Shell out to `docker` CLI** (M1's `CLIDockerProbe` style). Rejected:
  too many parameters to template safely; brittle output parsing for
  inspect/info; no way to share an authenticated connection between
  long-lived inspect loops and one-off calls.
- **`testcontainers-go`.** Rejected: it adds assembly-line-test machinery on
  top of the SDK we already need; cm only needs the lower layer.

## References

- `architecture/container-and-image.md` §2.
- `internal/cm/` (this ADR's implementation).
- SCAFFOLD §1 (Go 1.23, Docker SDK).
