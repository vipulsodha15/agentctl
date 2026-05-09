# ADR 0005 — MCP reachability checks at session start (§15.5)

- **Status:** Accepted.
- **Date:** 2026-05-09.
- **Deciders:** Architect.

## Context

A session start has an enabled set of MCP servers (R5). Some of these
might be transiently unreachable (internal network blip, MCP service
down). Should agentd probe them and:

- hard-fail the session start, or
- soft-warn and proceed?

R3's documented error case already says "An enabled MCP URL is
unreachable at start → session starts anyway; the unreachable MCP is
reported in the session log and absent from the runtime's available
tools." That language implies soft-warn but leaves the probe behavior
unspecified.

## Decision

Soft-warn. Specifically:

- `agentd` issues a parallel TCP-connect (or HTTP `GET /` for URLs with
  a path) probe against each enabled MCP at session start, with a
  **1.5s** per-probe timeout and a **3s** overall ceiling so probes
  cannot blow the cold-start budget (§4: 5s p50).
- Reachable MCPs ⇒ recorded as `ok` on `sessions.mcp_status_json`.
- Unreachable MCPs ⇒ recorded with the failure reason and surfaced as
  a `mcp.unreachable` stream event the moment any client attaches.
- The runtime is **always** started with all enabled MCPs configured.
  If a transient MCP comes back during the session, the runtime can use
  it without restart.
- Override config knob: `session.mcp_probe = soft_warn (default) |
  block_on_failure | none`.

## Consequences

- Cold-start latency stays predictable. The 3s ceiling is comfortably
  inside the 5s/10s budget.
- Developers see "MCP X unreachable" in the UI and CLI immediately;
  they're not silently missing a tool.
- A flapping internal MCP doesn't block work; the runtime retries on
  use.
- Diverges slightly from "fail fast" instinct but matches R3's stated
  error case.

## Alternatives considered

- **Hard-fail.** Predictable but disruptive; one MCP outage blocks all
  new sessions. Rejected.
- **No probe at all.** The runtime would discover unreachable MCPs on
  first tool use and report tool-level errors. Loses the "tell the
  user up front" UX win.
- **Probe on a schedule throughout the session.** More info but a
  slow trickle of health events; over-engineered for v1. Defer.

## References

- requirements.md R3, R5, §15.5.
- agentd.md §1.1.
- api.md §5 (`mcp.unreachable`).
