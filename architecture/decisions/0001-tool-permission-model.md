# ADR 0001 — Tool permission model (§15.1)

- **Status:** Accepted (resolved before architecture; documented here for completeness).
- **Date:** 2026-05-09.
- **Deciders:** Product owner (recorded in requirements.md §15.1).

## Context

Per requirement R1–R10, the agent runtime makes tool calls (Bash, file
edits, MCP calls) on behalf of the developer. With permission prompting
**enabled**, every tool call would emit a permission event the developer
must approve before the runtime proceeds. With permission prompting
**disabled**, all tools auto-approve and the runtime runs end-to-end
without per-tool confirmations.

The container's isolation (R7) constrains what tools can affect:
per-session volume, per-session network, no peer-container or host
loopback access except the bind-mounted control sock.

## Decision

Run the agent runtime with `--dangerously-skip-permissions` (or the
runtime's equivalent flag). All tools and MCP calls auto-approve. The
container's isolation (R7) is the **sole** safety boundary.

## Consequences

- The conversation stream contains `tool.call` and `tool.result` events
  but **no** permission events.
- The blast radius of any tool call is bounded by the container's
  filesystem (`/work` and `tmpfs /home/agent`), the per-session network
  policy, and outbound endpoints (Anthropic, configured MCPs, GitHub).
- Destructive operations inside the container are possible (`rm -rf
  /work`); the only undo is `agentctl stop` and a new session.
- `git push` to remotes uses the developer's PAT; the developer is
  responsible for the consequences (including accidental force-pushes),
  the same as if they ran `git push` themselves.
- All R7 controls become safety-critical. We invest heavily in
  validating them (network self-test in `agentctl doctor`).

## Alternatives considered

- **Permission prompting on, with allowlist:** every Bash tool call
  prompts unless it matches an allowlist. *Rejected:* breaks the
  "ambitious autonomous coding" UX agentctl is built for; allowlists
  drift; users learn to bulk-approve.
- **Permission prompting on for write tools only, off for read:**
  cleaner UX but still interrupts flow; complexity not worth it for v1
  given the strong container boundary.
- **No isolation, prompt for everything:** the legacy IDE-extension
  posture. Not what agentctl is for; the whole point is a sandboxed
  agent.

## References

- requirements.md §15.1, R7, §16.
- container-and-image.md §4 (network policy).
- security.md §1 (blast radius).
