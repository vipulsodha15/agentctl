# ADR 0010 — CLI ↔ agentd RPC framing

- **Status:** Accepted.
- **Date:** 2026-05-09.
- **Deciders:** Architect.

## Context

The CLI talks to `agentd` over a Unix domain socket (`agentd.sock`).
Three viable framings:

1. **gRPC over Unix.** Strongly typed, mature streaming, codegen.
2. **HTTP+WebSocket over Unix.** Mirrors the browser API exactly.
3. **Length-prefixed JSON / NDJSON.** Hand-rolled, trivial to implement.

We want a transport that is:

- easy to implement in any language a future CLI might be written in;
- easy to debug with `nc -U` and `jq`;
- multiplexed (one socket, many in-flight requests + streams);
- consistent in shape with the WebSocket framing on the browser side
  so handlers can be shared.

## Decision

Length-prefixed JSON over Unix socket (see api.md §2 for full schema).

- Frame: `<u32 BE length><utf8 JSON object>`. Max payload 16 MiB.
- Multiplexing: each frame has an `id` (ULID) shared by all frames in a
  given request/stream.
- `kind` in `{request, response, event, stream_chunk, stream_end,
  error}` mirrors the WS framing.

## Consequences

- No codegen pipeline. Clients and tests can read/write frames with
  the standard library + JSON.
- The same handler code path on the daemon side serves both the CLI
  socket and the HTTP/WS frontend (the API handlers in `agentd.md` §1
  are transport-agnostic; only the transport layer differs).
- We pay marginal CPU on JSON marshal/unmarshal vs gRPC; not the
  bottleneck for a localhost RPC.
- Forward compat is by ignoring unknown fields; explicit `v: 1`
  signals breaking-change boundaries.

## Alternatives considered

- **gRPC over Unix.** Heavier; codegen complicates builds; debugging
  with `grpcurl` adds tooling. Rejected.
- **HTTP+WS over Unix.** Doable but introduces HTTP semantics (status
  codes, headers) without value at this layer; the CLI doesn't need
  them.
- **Custom binary protocol.** No upside vs JSON for our throughput;
  loses easy debugging.

## References

- api.md §2.
- agentd.md §1 (sock module).
