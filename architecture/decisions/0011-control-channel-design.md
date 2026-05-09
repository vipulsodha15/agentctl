# ADR 0011 — Container ↔ agentd control channel

- **Status:** Accepted.
- **Date:** 2026-05-09.
- **Deciders:** Architect.

## Context

The session container needs a single, narrow channel to `agentd`:
- carry runtime events out (assistant deltas, tool calls, usage);
- accept user messages and interrupts in.

R7 mandates "no peer container access, no host loopback access except
the dedicated control channel." Choices for that channel:

1. **TCP socket** on a host-loopback IP exposed only to the container.
2. **gRPC over Unix bind-mounted into the container.**
3. **NDJSON over Unix bind-mounted into the container.**
4. **Stdin/stdout to the container** (Docker's `attach` mechanism).

## Decision

NDJSON over a Unix domain socket bind-mounted into the container at
`/run/agentctl/control/agentd.sock`. The host directory
`~/.local/share/agentctl/sessions/<id>/control/` is bind-mounted
read-write into the container at `/run/agentctl/control/`. The socket
file itself is mode `0660`, the directory `0700`.

Frame: NDJSON, line-delimited, `\n` terminator, 1 MiB max line. Schema
in api.md §4.

Authentication: the shim sends `runtime.hello` with a per-session
`session_token` (256-bit random) generated at session create. agentd
verifies before sending `agentd.greet`. Single active connection per
session.

## Consequences

- The container has **zero** host network access through this channel.
  R7's "no host loopback" stays clean — the bind-mount is filesystem,
  not network.
- Implementation is trivial in any language (the runtime shim is
  small).
- Backpressure and rate limits are easy to reason about (we cap
  inbound 100 frames/s, drop only delta frames; see api.md §4.5).
- Recovery on agentd restart is simple: agentd reopens the socket; the
  shim reconnects on EOF.
- The bind-mount is the only host-side surface. R7's invariant holds
  with one exception (the control sock), which is the documented
  exception.

## Alternatives considered

- **TCP loopback.** Would need iptables exception to allow the
  container to reach host loopback for one specific port; messy,
  error-prone. Rejected.
- **gRPC streaming.** Heavier, codegen, debugger-unfriendly; not worth
  it for a single-purpose internal channel.
- **Docker `attach` (stdin/stdout).** The runtime needs stdout for
  diagnostics anyway; multiplexing the control protocol on top of it
  forces the runtime to know about the protocol. Bad layering.

## References

- requirements.md R7.
- api.md §4.
- container-and-image.md §2.4 (mounts), §2.6 (entrypoint).
- agentd.md §1.2.
