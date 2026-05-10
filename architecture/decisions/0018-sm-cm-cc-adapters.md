# ADR 0018 — Translate sm <-> cm/cc at the boot layer

- **Status:** Accepted.
- **Date:** 2026-05-10.
- **Deciders:** M2 sub-agent B.

## Context

`internal/sm` (session manager / actor) was developed in parallel with
`internal/cm` (container manager) and `internal/cc` (control channel
server). Their public surfaces ended up close-but-not-identical:

- `sm.ContainerManager` exposes a session-shaped `Create/Start/Stop/Remove`
  with an `sm.ContainerSpec` that does not name a `MountType`. `cm.Manager`
  has the same operations but with `cm.Spec` and `cm.Mount{Type,...}`.
- `sm.ControlServer.Listen(sessionID, sockPath, sessionToken, handler)`
  registers a per-session `ControlHandler` that the actor closes over.
  `cc.Server.Listen(sessionID, sockPath)` plus
  `cc.Server.AdoptInjector(verifier, adopter)` registers a single global
  `TokenVerifier` and a single global `Adopter` callback.
- `sm.ControlConn` is a direct Send/Recv duplex; `cc.Server` hands the
  actor a `cc.Conn` plus an `events <-chan cc.Frame` that the readloop
  feeds. The `Frame` types are field-for-field identical but live in
  different packages.

We need a place that translates one shape into the other without coupling
sm to cm/cc directly (sm has its own unit tests that compile without
docker pulled in) and without forcing cm/cc to grow toward sm's actor
model (cm/cc are intentionally session-agnostic infrastructure).

## Decision

Adapters live in `internal/agentd/wire.go` — the same package that boots
the daemon. They are not exported.

- `cmAdapter` wraps a `cm.Manager`, copies `sm.ContainerSpec` ->
  `cm.Spec` (mount-type translation included), implements
  `sm.ContainerManager`.
- `ccAdapter` wraps a `cc.Server`. It maintains two maps: `session_id ->
  (token, handler)` and `token -> session_id`. It implements both
  `cc.TokenVerifier` and `cc.Adopter` (so the daemon registers it as
  both via `AdoptInjector`) **and** `sm.ControlServer`. On
  `Adopt(sessionID, conn, events)` it looks up the per-session handler
  and invokes it with a `ccConnAdapter` that bridges
  `cc.Conn.Send`/`events` to `sm.ControlConn.Send`/`Recv`.

The actor sees a per-session, per-handler control connection — which is
how the existing actor code is written (the handler closes over `*actor`
and calls `InjectControlConn`). The cc server stays oblivious to
sessions beyond the listener bookkeeping it already does.

A small extension to `sm.ContainerManager` was needed: the interface now
includes `Start(ctx, id) error`, mirroring `cm.Manager.Start`. Without
it `manager.Create` could not run the documented Create -> Listen ->
Start ordering from `architecture/overview.md` §6.2.

## Why per-session handlers, not a global verifier-adopter pattern in sm

The `cc` package's global `(TokenVerifier, Adopter)` pair is the right
shape for `cc`: it accepts any inbound socket, looks up the bearer
token, hands the connection off. It's session-shaped only at the very
end (Adopt knows the session_id).

The actor needs a *typed* handler that closes over `*actor` for that
specific session, because the alternative (a single `Adopter` that calls
`manager.Lookup(sessionID).InjectControlConn(...)`) would force the
manager to expose actor lookup by id and would put the per-session token
table inside sm. Keeping the table inside the adapter localizes the
"token -> session" indirection where it belongs (boot layer, next to
where the session's secret env file is written), and keeps the actor's
Inject path the way the unit tests use it (no global registry, no name
-> handler resolution).

## Consequences

- Adding a new control message kind only requires editing
  `internal/cc/frame.go` and the `sm` constant block; the adapter
  copies the field set blindly.
- A second control-channel transport (TLS over TCP, say) would replace
  cc and the adapter, leaving sm unchanged.
- The token table is the only piece of session state in cc adapter; it
  stays in memory only — agentd restart recovery (M3) re-registers
  every persisted session and re-derives the token from the session row.
- Tests for sm continue to use a fake `ControlServer` directly; the
  adapter is exercised by the integration smoke path (no Docker -> we
  log a warning, sm marks the session error, the daemon stays up).

## Alternatives considered

- **Make sm use cc's interfaces directly.** Rejected: forces sm to
  import cc and pulls cm/docker into sm's test binary; also forces
  the global-adopter shape into sm where per-session handlers are a
  better fit for the actor model.
- **Make cc use sm's interfaces.** Rejected for the symmetric reason:
  cc would have to know about session-token lookup, putting a piece
  of secrets policy into the framing layer.
- **Generate the adapter.** Rejected as overkill for ~150 lines.

## References

- `internal/agentd/wire.go` — this ADR's implementation.
- `internal/sm/deps.go` — sm's view of the two interfaces.
- `architecture/overview.md` §6.2 — Create -> Listen -> Start ordering.
- ADR 0011 — control-channel design.
- ADR 0017 — Docker SDK dependency.
