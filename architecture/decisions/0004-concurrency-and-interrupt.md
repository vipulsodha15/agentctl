# ADR 0004 — Concurrency and interrupt model (§15.4)

- **Status:** Accepted.
- **Date:** 2026-05-09.
- **Deciders:** Architect.

## Context

While the agent is mid-turn, a user (in any of multiple attached
clients) might send a new message. The choices listed in §15.4:

- **A.** Queue and deliver after current turn finishes.
- **B.** Interrupt current turn and start the new one.
- **C.** Reject with "agent is busy."

Two clients can also send "simultaneously"; some serialization rule is
needed.

## Decision

**Option A** (queue) plus an explicit `Interrupt` action.

- Each session has a single FIFO mailbox at agentd. Inbound
  `SendMessage`s are processed in arrival order at the daemon.
- While `in_flight = Some(turn_id)`, additional messages are queued.
  agentd emits `queue.depth=N` to all attached clients on every
  enqueue/dequeue.
- An explicit, separate operation `Interrupt` cancels the in-flight
  turn (sends `agentd.interrupt` on the control sock; runtime emits
  `turn.cancelled`). The queue is preserved unless `--clear-queue` is
  passed.
- Idle-stop sweepers skip sessions with `in_flight=1 OR queue_depth>0`.
- Hard-cutoff sweepers cancel and stop regardless.
- A config knob `session.queue_policy` exists with values `queue`
  (default) and `reject` (option C). It's an escape hatch we don't
  expect anyone to use.

UX surface: a "Stop" button in the Web UI and `agentctl interrupt
<session>` in the CLI both invoke `Interrupt`. The CLI also accepts
`SIGINT` while attached as a shortcut.

## Consequences

- The "agent is busy" error path is rare. Messages don't bounce.
- A user can type follow-up context while the agent is responding;
  they'll be processed in order.
- An explicit interrupt is one click / one keystroke — discoverable
  and predictable. Implicit interruption-on-new-message would surprise
  users who type while the agent is "thinking."
- Two clients that send near-simultaneously serialize cleanly; the
  user-facing queue events make it visible.
- The implementation is straightforward: one actor per session, single
  mailbox.

## Alternatives considered

- **Option B (auto-interrupt).** Implicit, low-friction, but breaks
  the "I'm going to add a thought" pattern; bad for collaborative use
  with two clients open.
- **Option C (reject when busy).** Forces the user to wait or
  interrupt. High friction; unnecessary given B's interrupt action.
- **Per-client queues.** Each client has its own queue, merged at the
  runtime. Hard to reason about ordering; rejected.

## References

- requirements.md §15.4, R4, R6.
- agentd.md §1.1, §2.
- api.md §1, §4.3.
