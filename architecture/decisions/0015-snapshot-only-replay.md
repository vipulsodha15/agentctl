# ADR 0015 — Snapshot-only replay (no event buffer)

- **Status:** Accepted.
- **Date:** 2026-05-10.
- **Deciders:** Product owner.
- **Supersedes:** ADR 0012 (two-tier in-memory + sqlite event buffer).

## Context

R6 requires that a client reconnecting after a disconnect can re-establish
a coherent view of the session. The earlier design (ADR 0012) implemented
this with a two-tier replay buffer:

1. In-memory ring of the last 1,000 events per session (~5 MB).
2. sqlite `events` table backstop holding up to 50,000 rows / 24 h per
   session, with an hourly `events_prune` sweeper.
3. `SnapshotSession` as a fallback when both buffers were too old.

The intent was incremental reconnect: a client passes its last seen
`since_event` cursor and the server replays only what it missed.

In review this turned out to be over-engineered for a localhost,
single-user developer tool:

- The SDK already persists the conversation to
  `/work/.claude/projects/-work/<sdk_session_id>.jsonl` on the per-session
  volume — that is the durable, replayable record. The `events` table
  duplicated the same conversation in a different shape.
- The hot path paid for it: every `assistant.delta` (token-stream chunks,
  often hundreds per turn) became a sqlite INSERT in the same actor step
  that broadcasts to subscribers. Streaming throughput was bounded by
  sqlite write rate.
- Disk cost was material: ~1 KB/event × 50k events × N sessions ≈ 500 MB
  for 10 sessions, on top of the JSONL.
- The optimization it delivered — saving a snapshot read on the rare
  `agentd`-restart-with-stale-cursor path — is worth ~hundreds of
  milliseconds of local file read.
- For *past* turns, delta-level replay is not what any UI needs: clients
  render the final assistant message text. Delta granularity only matters
  for the *in-flight* turn.

## Decision

**Snapshot + live tail. No replay buffer.**

- On every `AttachStream` (initial or reconnect), the server issues
  `agentd.snapshot_request` on the control sock, awaits
  `runtime.snapshot` from the shim (which reads the SDK's JSONL on the
  volume), and writes one `session.snapshot` event to the subscriber
  containing the conversation plus current operational state pulled from
  the per-session actor (queue depth, in-flight turn id, MCP statuses,
  repos).
- The server then broadcasts live events to that subscriber until it
  disconnects. No backlog is retained.
- There is no in-memory event ring, no sqlite `events` table, no on-disk
  `events.ndjson`, no `since_event` cursor, no `buffer_overflow` error,
  no `events_prune` sweeper.
- Slow subscribers: each broadcast write is non-blocking with a small
  bounded send buffer (e.g., 64 frames). If a subscriber's buffer fills
  or a write fails, agentd drops the subscription with
  `stream_end{reason: slow_consumer}`. The client reconnects and gets a
  fresh snapshot.
- Clients dedupe by `event_id` if they care; the live stream is
  at-least-once.

## Consequences

### Wins

- **One source of truth for the conversation.** The SDK's JSONL is both
  the SDK's `resume` input and the snapshot source. agentd writes no
  parallel copy.
- **No write amplification on the streaming hot path.** Token-streaming
  deltas broadcast to subscribers without touching sqlite.
- **Less plumbing.** Drops one sqlite table + 2 indexes, one on-disk
  events file per session, one in-memory ring, one prune sweeper, the
  cursor/buffer-overflow protocol, three sessions-row columns
  (`last_seq`, `queue_depth`, `in_flight`), one HTTP endpoint
  (`/v1/sessions/{id}/snapshot`), one logical op (`SnapshotSession`),
  and one metric (`agentd_event_buffer_overflows_total`).
- **Stateless fan-out.** The fan-out layer is a broadcast channel with
  no per-subscriber backlog state.

### Losses (accepted)

- **Reconnect re-renders the conversation.** A client that disconnects
  mid-conversation and reconnects fetches the full snapshot and
  re-renders. On localhost with a JSONL file of typical size this is
  visually a brief flash.
- **A new client joining mid-turn misses the start of the in-flight
  assistant message.** It sees the conversation up to the last completed
  turn (from the snapshot) and then catches the live tail from the
  moment of attach. The final `assistant.message` event at `turn.end`
  carries the complete text, so the client can render the full message
  once the turn finishes; until then the in-flight reply may render
  partially.
- **No "fill the gap" UX on agentd restart.** Clients reconnect and
  re-fetch a snapshot, same as any other attach.

### Future option (not built in v1)

If the "joined mid-turn" UX becomes a real complaint, a tiny per-session
**current-turn accumulator** can be added: the actor appends each
`assistant.delta` to a string for the active turn and clears it on
`turn.end`. The snapshot includes the partial string. This is one string
per session (kilobytes), no DB, no sweeper. It is intentionally not in
v1 to keep the model "snapshot is the JSONL, full stop."

## Contract changes vs ADR 0012

- `data-model.md §2`: `events` table removed; `last_seq`, `queue_depth`,
  `in_flight` columns removed from `sessions` (live state lives in the
  actor). `idx_sessions_status_in_flight` index removed.
- `data-model.md §4`: `events.ndjson` removed from per-session on-disk
  layout.
- `api.md §1, §3.2`: `SnapshotSession` removed as a distinct op;
  `/v1/sessions/{id}/snapshot` HTTP endpoint removed.
- `api.md §2.4`: `AttachStream` request shape simplified — no
  `since_event`, no `include_history`. Always emits `session.snapshot`
  as the first frame.
- `api.md §2.3`: `buffer_overflow` error removed; `snapshot_failed`
  added.
- `api.md §5`: `session.snapshot` event now carries the conversation in
  its `data`. `seq` removed from the wire envelope; ordering is
  on-the-wire.
- `api.md §6`: cross-restart sequence numbers removed; ordering is
  on-the-wire and per subscription.
- `agentd.md §1.1`: actor state no longer includes `event_seq` or
  `event_buffer`.
- `agentd.md §3`: fan-out rewritten as stateless broadcast.
- `agentd.md §5`: `events_prune` sweeper removed; idle-stop sweeper now
  asks the actor for live `in_flight` / `queue_depth` instead of reading
  from the DB.
- `observability.md §5.1`: `agentd_event_buffer_overflows_total` removed;
  `agentd_snapshot_failed_total` added.

## Alternatives considered

- **Keep the in-memory ring, drop only the sqlite tier.** Cheaper than
  the original but still pays the conceptual cost of "buffer +
  fallback." Rejected — once you accept "re-render on reconnect is OK,"
  the ring earns its keep only for in-turn delta replay to a *new*
  attaching client, which is rare enough to defer entirely.
- **Keep the sqlite tier, drop only the ring.** Worst of both worlds:
  every read hits disk, write amplification still present.
- **Stream JSONL writes from the shim to agentd as they happen.** Would
  let agentd serve snapshots without a control-sock round-trip. Adds a
  second persistence path; the snapshot read on attach is fast enough
  that the round-trip is not the bottleneck.

## References

- requirements.md R6.
- api.md §2.4, §3.2, §5, §6, §4.3.
- data-model.md §2, §4.
- agentd.md §1.1, §3, §5.
- overview.md §6.4, §9 (clarification 4).
- ADR 0012 (superseded).
