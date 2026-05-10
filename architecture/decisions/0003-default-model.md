# ADR 0003 — Default model and per-session model selection (§15.3)

- **Status:** Accepted.
- **Date:** 2026-05-09.
- **Deciders:** Architect.

## Context

Sessions need a model id to pass to the agent runtime. Options:

- Bake the model into the base image (one image per model).
- Make the model a per-session config baked at start.
- Make the model live-switchable.

The model affects token cost (R10), so we want one row per turn that
records exactly which model produced it.

## Decision

Per-session selection at start, frozen for the session's lifetime:

- Default model is **`claude-sonnet-4-6`** — the latest cost-balanced
  Sonnet at v1 ship. Configurable via `agentctl config set
  model.default <id>`.
- Per-session override: `agentctl start --model <id>`. Recorded on
  `sessions.model`.
- The base image is model-agnostic; the model id is injected as the
  `AGENTCTL_MODEL` env var and the shim passes it on the runtime
  invocation.
- The model id is validated at start against `config.toml`
  `[pricing.tables.models]`. Unknown ids start the session anyway but
  log a warning; `usage` rows for unknown models record `cost_usd =
  NULL` (matches R10).

## Consequences

- A developer can run two sessions side-by-side on different models
  without changing the image.
- Cost rows are accurately tagged because the model is recorded at
  insert time from the runtime's reported model id (which may differ
  from `sessions.model` if the runtime negotiated something else; we
  trust the runtime's `usage` event).
- Live model-switching mid-session is **not** supported; that would
  require either a runtime-level operation we don't control or
  re-creating the container, which would lose in-memory tool state.
  Defer.
- Choosing Sonnet by default keeps everyday spend predictable; users
  who want Opus opt in.

## Alternatives considered

- **Default Opus.** Higher quality per-turn but expensive by default.
  Users who try it casually might be surprised by spend; opt-in is
  safer.
- **Default Haiku.** Cheap but undersized for the kinds of refactor /
  multi-file tasks agentctl is built for; would create a "first
  experience is bad" risk.
- **Bake model into the image.** One image per model variant; infra
  bloat without UX win. Rejected.

## References

- requirements.md R10, §5, §15.3.
- data-model.md §5, §2 (`sessions.model`, `usage.model`).
- container-and-image.md §2.5.
