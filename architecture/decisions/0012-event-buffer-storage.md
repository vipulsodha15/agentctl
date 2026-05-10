# ADR 0012 — Event buffer storage for client reconnects

- **Status:** Superseded by ADR 0015 (2026-05-10). The two-tier
  in-memory + sqlite design described below is **not** built. v1 ships
  with snapshot-on-attach + live tail and no replay buffer. This ADR is
  retained for the historical reasoning.
- **Date:** 2026-05-09.
- **Deciders:** Architect.

## Context

R6 requires that a client reconnecting after a disconnect can resume
the event stream without losing events. R6 says "last 200 events or
last 5 minutes, whichever larger" plus a fallback to the runtime's
history files for older state.

Implementation choices:

1. **In-memory only.** Loses on agentd restart; would force a
   snapshot fetch from the runtime on every restart-resume.
2. **In-memory + sqlite-backed.** Two tiers; hot path stays in memory,
   warm path goes to disk.
3. **Disk-only (NDJSON file per session).** Simple, but every read
   touches disk.

Per R6: "Forcing agentd to restart does not lose any session data;
sessions are listable and resumable immediately afterward." The runtime
history on the volume is the ultimate authority; the buffer is for
**fast** reconnects without re-rendering.

## Decision

Two-tier:

- **In-memory ring buffer** per session, holding the last 1,000 events
  (~5 MB cap). Subscribers attach with optional `since_event` cursor;
  ring serves cursor up to its oldest seq.
- **sqlite `events` table backstop** (data-model.md §2). Events older
  than the in-memory ring but newer than 24h or below 50,000 rows per
  session are served from this table.
- **Beyond that**: client falls back to `SnapshotSession` (api.md §2.4)
  which proxies to the runtime's history.

Writes: every event is pushed to the ring AND inserted into `events`
in the same actor turn. Ordering is preserved by `seq`.

Pruning: `events_prune` sweeper runs hourly; deletes rows older than
24h or beyond per-session row caps.

## Consequences

- Quick reconnects (browser tab refresh, CLI restart): served from
  memory, sub-millisecond.
- agentd restart: the memory ring is gone, but the sqlite backstop
  serves the same window. R6's restart criterion is met.
- Long disconnects (hours): client falls back to snapshot, gets a
  fresh history from the runtime.
- Disk cost: ~1 KB/event × 50k events × N sessions. With 10 sessions
  fully ringed, ~500 MB. We accept this; events are pruned.
- The `events` table doubles as a forensic record in the v1 lifetime
  for "what happened in session X."

## Alternatives considered

- **In-memory only.** Loses the agentd-restart promise. Rejected.
- **sqlite only.** Every read hits disk; for a typing-fast user with
  rapid reconnects it's measurable. Rejected.
- **Per-session NDJSON file (no DB).** Replays are streaming-friendly
  but querying ("seq > N") is O(file). The DB is right-sized.

## References

- requirements.md R6.
- data-model.md §2.1.
- agentd.md §3.
