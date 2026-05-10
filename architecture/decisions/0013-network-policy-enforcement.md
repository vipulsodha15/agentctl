# ADR 0013 — Per-session network policy enforcement

- **Status:** Superseded.
- **Date:** 2026-05-09.
- **Superseded:** 2026-05-10 — strict outbound network filtering removed
  from v1 scope; deferred to v2. See `v2-requirements.md` §V2.1.
- **Deciders:** Architect (original); product owner (deferral).

> **Note:** the design captured below was **not** adopted in v1. v1
> ships with Docker-native peer isolation only (per-session bridge with
> `enable_icc=false`) and no host-firewall manipulation. The original
> design is retained for historical context and as a starting point for
> v2 egress-restriction work.

## Context

R7 requires per-session network isolation: containers can reach
Anthropic and configured MCPs, **cannot** reach peer containers, and
**cannot** reach host loopback (where agentd's Web UI runs) other than
the bind-mounted control sock.

Linux Docker uses iptables under the hood; we can directly add rules.
Docker Desktop on macOS runs Linux in a VM; host-side iptables doesn't
exist for the developer's containers.

## Decision

Linux: agentd manages a custom iptables chain `AGENTCTL-EGRESS` jumped
to from `DOCKER-USER`. Per-session rules drop traffic to the host
bridge IP, drop unconfigured RFC1918 ranges, accept configured MCP
CIDRs and Anthropic + GitHub IP pools (refreshed hourly). Default deny
on the chain's tail. Rules are idempotently rebuilt on every
`agentd` start.

macOS: rely on per-session bridge networks with ICC off (which Docker
Desktop honors) for peer-isolation. Egress filtering is best-effort
because the developer's iptables aren't reachable. We document the
gap. `agentctl doctor`'s `network.policy` self-test warns rather than
fails on macOS.

In both: the only host-side bind-mount is the control sock dir, mode
`0700`.

## Consequences

- Linux gets full R7 enforcement.
- macOS: peer isolation works; egress filtering is best-effort.
  Developers using macOS for sensitive work should be aware. Hardening
  to feature parity needs a Docker plugin (out of v1).
- Doctor's self-test gives confidence the rules actually work — both
  initial install and after any environmental change.
- Adding new MCP CIDRs is a one-line config change; the network
  manager re-resolves on the next session start.

## Alternatives considered

- **Run a userland egress proxy in each session container.** Forces
  every connection through a sidecar that we control. Achievable
  feature-parity on macOS but adds a sidecar process per session and
  another moving piece. Defer to v2.
- **No egress filtering, rely on container isolation alone.** Doesn't
  meet R7. Rejected.
- **Custom Docker network plugin.** Heavy implementation; would need
  signing/distribution. v2.

## References

- requirements.md R7.
- container-and-image.md §4.
- security.md §4.
