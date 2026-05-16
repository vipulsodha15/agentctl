# Container Restart Handling — Audit & Edge Cases

Audit of how agentctl handles container lifecycle when containers stop without
the daemon initiating it, and the gaps in restart-on-message recovery.

Scope: `internal/sm/actor.go`, `internal/sm/manager.go`, `internal/recovery/recovery.go`,
`internal/cm/`.

---

## 1. Current Behavior

### 1.1 How container exit is detected

There is no active polling of container state during normal daemon operation.
Detection relies on three passive signals:

1. **Control-connection closure.** The shim's TCP control conn is the only
   live signal. When the container exits, the shim's socket closes, and
   `actor.readControl` (`internal/sm/actor.go:427-438`) gets a `Recv` error
   and enqueues `mboxControlClosed`.
2. **Message arrival on a `stopped` session.** `manager.Send` checks status
   and triggers `Restart` if the session is already marked `stopped`
   (`internal/sm/manager.go:910-914`).
3. **Daemon-startup recovery.** `recovery.reconcileRunning`
   (`internal/recovery/recovery.go:232-261`) audits each DB row marked
   `running`/`starting`; if no live container exists it flips status to
   `stopped` with `last_error="container_exited_at_recovery"`.

### 1.2 What `handleControlClosed` does today

`internal/sm/actor.go:409-425`:

```go
func (a *actor) handleControlClosed(conn ControlConn) {
    a.mu.Lock()
    if conn != nil && a.control != conn {
        a.mu.Unlock()
        return // stale close from a prior conn
    }
    a.control = nil
    a.runtimeReady = false
    a.mu.Unlock()
    a.opts.Logger.Info("session.control_disconnected")
}
```

It clears `control` and `runtimeReady`, nothing else:

- `summary.Status` stays `"running"`.
- DB row stays `status="running"`.
- `inFlight`/`currentTurn` are not touched.
- No terminal event is broadcast to SSE/event consumers.

### 1.3 The auto-restart-on-message path

`internal/sm/manager.go:900-914`:

```go
func (m *manager) Send(ctx context.Context, req SendRequest) (SendResult, error) {
    a := m.actorFor(req.SessionID)
    if a == nil {
        return SendResult{}, ErrSessionNotFound
    }
    if a.snapshotSummary().Status == "stopped" {
        if _, err := m.Restart(ctx, req.SessionID); err != nil {
            return SendResult{}, fmt.Errorf("restart stopped session: %w", err)
        }
    }
    // ... enqueue message into actor mailbox ...
}
```

This only fires for sessions explicitly stopped (via `Stop`) or sessions
recovered as stopped at daemon startup. **It does not fire for a session
whose container died mid-life while the daemon was running**, because
`handleControlClosed` never flipped the status to `stopped`.

### 1.4 Message dispatch precondition

`internal/sm/actor.go:294-310` (`handleSend`):

```go
// Starting a turn requires the shim to have sent RuntimeReady — otherwise
// the AgentdMessage frame either has no control conn to land on (a.control
// nil) or hits the shim before it enters its inbound loop, and the message
// is silently lost.
if a.inFlight == "" && a.runtimeReady {
    // start turn
} else {
    // queue
}
```

The queue is only drained when `RuntimeReady` arrives (`actor.go:445-466`)
or when `completeTurn` fires (`actor.go:561-577`).

---

## 2. Headline Failure Modes

### 2.1 Case A — Container exits, daemon doesn't know

1. Container crashes / OOM / `docker stop` from outside.
2. Shim's socket closes; `mboxControlClosed` enqueued.
3. `handleControlClosed` clears `control` + `runtimeReady`, leaves status `"running"`.
4. User sends a message.
5. `manager.Send` sees `Status == "running"` and skips the auto-restart branch.
6. `handleSend` sees `runtimeReady == false` and queues the message.
7. Queue only drains on `RuntimeReady`, which can never fire because no
   shim exists.
8. **Message parked indefinitely.**

### 2.2 Case B — Container dies mid-turn

Same as A, plus:

- `inFlight` is set when the conn closes. `handleControlClosed` does not
  clear it; only `completeTurn` or `handleStop` clear it.
- The in-flight client (SSE/CLI) never receives a terminal frame
  (`turn.completed` or `turn.error`).
- Even if Case A is fixed (status flipped to `stopped` on conn close),
  `handleSend` line 304 (`if a.inFlight == "" && a.runtimeReady`) refuses
  to start a new turn because `inFlight` is still set.
- New messages queue behind a turn that will never complete.

---

## 3. Additional Edge Cases

### A. Detection blind spots

| # | Case | Severity | File:Line |
|---|------|----------|-----------|
| A1 | Shim alive but container dead (e.g. `docker pause`, hung process). Socket stays open in kernel; `handleControlClosed` never fires; writes buffer into a dead conn; no heartbeat exists. | Correctness | `actor.go:294-310` |
| A2 | TCP half-open (`CLOSE_WAIT`) after unclean container exit. Local writes succeed but frames are lost. | Correctness | n/a |
| A3 | Container started but shim never sends `RuntimeHello`/`RuntimeReady`. No startup timeout — session sits in "starting" / `runtimeReady=false` forever; every Send queues. | Correctness | `manager.go:553+` |
| A4 | `docker rm -f <ctr>` from outside. If conn closes, falls into Case A; otherwise next `Containers.Inspect`/`Stop` will 404 (errors swallowed at `manager.go:1259-1260`). | Correctness | `manager.go:1259-1260` |

### B. State-machine races

| # | Case | Severity | File:Line |
|---|------|----------|-----------|
| B1 | Concurrent `Restart` on same session. `manager.Restart` does Interrupt → Stop+Remove → `markRestarting` → `provisionContainer` → DB update with **no per-session mutex**. Two concurrent Sends on a `stopped` session both call `Restart` in parallel → two new containers created, two control listeners, second clobbers first state in actor. One orphan container left running. | Correctness | `manager.go:1244-1308`, `manager.go:911` |
| B2 | `Stop` racing with in-flight `Restart`. `Restart`'s Stop+Remove+provision happen outside the actor mailbox; a `manager.Stop` landing mid-Restart can flip status to `stopped` while a new container is being created. | Correctness | `manager.go:1244-1308` vs `manager.go:1348+` |
| B3 | `mboxControlClosed` racing with fresh conn. Identity check at `actor.go:417` covers the common case, but `markRestarting` closes the old conn synchronously while old `readControl` is still parked in `Recv`. Window where a Send sees `control != nil` but goroutine is about to die. | UX | `actor.go:995-1007`, `actor.go:413-420` |
| B4 | `RuntimeReady` arriving after `Stop`. If a buffered `RuntimeReady` frame lands in the mailbox after `mboxStop` ran, `handleControlFrame` flips status back to `"running"` and writes `UPDATE sessions SET status='running'`. Stopped session resurrects itself. | Correctness | `actor.go:445-462` |

### C. Partial failures during provision/restart

| # | Case | Severity | File:Line |
|---|------|----------|-----------|
| C1 | Network created, `UPDATE sessions SET network_id=?` fails. Actor knows netID; DB doesn't. Daemon restart → recovery has no record → new network created on next provision → first orphaned. | Resource leak | `manager.go:599-605` |
| C2 | `Control.Listen` succeeds, `Containers.Create` fails. Cleanup path not obvious; TCP listener may stay open with a stale handler. | Resource leak | `manager.go:648-657` |
| C3 | `Containers.Create` succeeds, `Start` fails. Container exists in `Created` state, never started. Container ID may or may not be set on actor depending on ordering. | Correctness / leak | `manager.go:553+` |
| C4 | `Start` succeeds, restart DB UPDATE fails. Container running, but DB still says `starting`. Recovery on next daemon boot has to decide. | Correctness | `manager.go:1297-1301` |
| C5 | `recordIdempotency` write error swallowed with `_ =`. Replay of same key produces a duplicate turn. | Correctness | `manager.go:921` |

### D. In-flight turn orphaning

| # | Case | Severity | File:Line |
|---|------|----------|-----------|
| D1 | `inFlight` has no deadline. A turn "running" for hours with no shim events is indistinguishable from a healthy long turn. | UX / correctness | `actor.go:294-310` |
| D2 | No terminal frame broadcast on mid-turn disconnect. SSE/CLI consumers hang. | UX | `actor.go:409-425` |
| D3 | Interrupt while `inFlight != ""` but `control == nil`. Interrupt frame can't reach anyone; request resolves but nothing happens; stale `inFlight` persists. | Correctness | `actor.go:330+` |

### E. Queue / backpressure

| # | Case | Severity | File:Line |
|---|------|----------|-----------|
| E1 | Unbounded `a.queue`. A dead session that keeps receiving messages grows memory without bound. No TTL — a message queued today gets delivered two days later when the container restarts. | Resource / UX | `actor.go` (queue) |
| E2 | Idempotency TTL independent of container lifetime. A retry after restart may dedupe or duplicate depending on key TTL. | Correctness | `manager.go:1387-1391` |

### F. Recovery edge cases

| # | Case | Severity | File:Line |
|---|------|----------|-----------|
| F1 | Multiple containers carrying the same `agentctl.session` label (e.g. from B1 above). Adoption vs. cleanup of the loser is unspecified. | Correctness | `recovery.go:199+` |
| F2 | Container labeled for a session no longer in DB. Network sweep exists; container sweep parity should be checked. | Resource leak | `recovery.go` |
| F3 | DB says `starting`, no matching container. Reconcile path branches on `running`/`stopped`; `starting` may need its own handling. | Correctness | `recovery.go:152, 199+` |
| F4 | DB says `running`, container is `Created` but not Started. Current `!c.Running` check lumps it with exited and removes it — probably correct, worth being explicit. | UX | `recovery.go:243` |

### G. External state surprises

| # | Case | Severity | File:Line |
|---|------|----------|-----------|
| G1 | Image GC'd by `docker image prune`. `provisionContainer` Create fails. No auto-pull. | UX | `manager.go:553+` |
| G2 | Volume bind path deleted on host. Container fails to start with mount error; partial provision state. | Correctness | `manager.go:607+` |
| G3 | Network removed externally. `Restart` reuses cached `networkID` → Create fails with "network not found". No retry-by-recreate. | UX | `manager.go:1290-1291` |

### H. Cleanup leaks

| # | Case | Severity | File:Line |
|---|------|----------|-----------|
| H1 | `readControl` goroutine leak. If a session is `Stop`ped or `markRestarting`-ed and `mboxControlClosed` is never delivered (mailbox closed first), the goroutine leaks for process lifetime. | Resource leak | `actor.go:427-438` |
| H2 | `Control.Stop(sessionID)` is called in `handleStop` and `Restart`. On terminate/delete paths and `provisionContainer` early-error paths, verify it's always called. | Resource leak | `actor.go:278`, `manager.go:1264` |

---

## 4. Proposed Scope for `claude/container-restart-handling`

### Must-fix (closes Cases A and B above)

1. **In `handleControlClosed` (when not part of an intentional Stop):**
   - Clear `inFlight` / `currentTurn`.
   - Emit a terminal turn event (`turn.aborted` or `turn.error`) so streaming
     consumers see closure.
   - Flip `summary.Status` to `"stopped"` and write
     `UPDATE sessions SET status='stopped', last_error='container_exited'`.
   - Optionally remove the dead container so `Restart` doesn't trip over a
     stale containerID.
2. **Distinguish intentional vs. unintentional close.** Stop/Restart paths
   already close the conn deliberately — they shouldn't trigger the
   "container died" path. Use a flag (`expectingClose`) or close-reason
   pattern.
3. **Add a startup readiness timeout** for the "container created but no
   `RuntimeReady` within N seconds" case (A3).
4. **Per-session mutex around `Restart`** so two concurrent Sends on a
   stopped session can't double-provision (B1, B2).

### Nice-to-have on this branch

5. **Bound the queue / TTL queued messages** (E1) so dead sessions don't
   grow unbounded and stale messages don't get delivered to a much-later
   container.

### Follow-ups (separate branches)

- Heartbeat / liveness probe on the control conn (A1, A2).
- Recovery handling for `status="starting"` rows with no container (F3).
- Surface container exit code / OOM signal in `last_error` (operator
  visibility).
- Cleanup of orphan networks/containers with mismatched labels (F1, F2, C1).
- Robustness against externally-mutated Docker state (G1-G3).
- Propagate `recordIdempotency` errors (C5).

---

## 5. Open Questions

- Eager vs. lazy restart: should `handleControlClosed` immediately
  re-provision, or wait for the next Send (current model)? Lazy is cheaper
  and matches the existing `stopped`-path behavior. Eager keeps the session
  warm but burns resources on idle dead sessions. **Recommendation: lazy.**
- Should queued messages survive a container restart? Today they do (queue
  is in the actor, not the container). After a fix that drains them on
  conn-close, do we re-queue them after restart or fail them with an
  error so the client retries? **Recommendation: fail with error; client
  retries with idempotency key.**
- Distinguishing OOM (exit 137) from clean exit (exit 0) from crash (exit
  1+): worth surfacing in `last_error` for operator diagnosis. Out of
  scope for this branch but cheap to add.
