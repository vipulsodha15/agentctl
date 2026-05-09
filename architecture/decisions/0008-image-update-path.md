# ADR 0008 — Image and skill update path (§15.8)

- **Status:** Accepted.
- **Date:** 2026-05-09.
- **Deciders:** Architect.

## Context

The base image carries the agent runtime, dev tooling, and skills. When
any of these change, the developer's sessions need a new container to
pick them up. The product question:

- Auto-pull on a schedule?
- Auto-pull on each `agentctl start`?
- Explicit command?
- Pinned and never auto?

R3 OOS includes "Live updates to the base image while a session is
running" — running sessions never get a new image without explicit
restart.

## Decision

Explicit, never automatic.

- `agentctl update` is the developer's entry point. It:
  1. Pulls the configured ref (`config.toml` `[image].ref`).
  2. Verifies cosign signature.
  3. Updates `[image].pinned_digest` to the new digest;
     `[image].previous_digest` retains the old one.
  4. Prints a per-session staleness report:
     - `running` sessions: "will keep current image until restart."
     - `stopped` sessions: "will pick up new image on next resume."
     - `terminated` sessions: "no action."
  5. Returns. **Does not** restart anything.
- `agentctl update --report` prints the same report without pulling.
- `agentctl update --rollback` swaps `pinned_digest` and
  `previous_digest`.
- `agentctl update --restart-stopped` runs `RestartSession` on every
  `stopped` row after the pull.
- `agentctl restart <session>` is the developer's manual upgrade
  trigger for a `running` session.
- Sessions store `image_digest` per-session (data-model.md §2). The
  next resume creates a new container from the **current** pinned
  digest, which may differ from `image_digest` if `update` happened in
  between. After resume, `image_digest` is updated to match.

Skills ride along inside the image. The runtime's manifest is fetched
fresh after each container create; clients refresh `/help` on
`skills.changed`.

`agentctl` and `agentd` binary upgrades are out of `agentctl update`'s
scope in v1; package managers handle them. After a binary upgrade, the
developer runs `agentctl init --repair`.

## Consequences

- Developers control when their working agent flips to a new image. No
  surprise mid-debug environment changes.
- Cost attribution stays clean (a session's `usage` rows all use the
  same model + image until restart).
- Rollback is a one-flag affair; we keep the previous digest.
- A team that wants to nudge developers to update can ship a
  `agentctl doctor` plugin (post-v1) that warns when the install's
  pinned digest is older than a configured threshold.

## Alternatives considered

- **Auto-pull on a schedule.** Surprises mid-task; complicates "what
  image is running" mental model. Rejected.
- **Auto-pull on `agentctl start` only (new sessions).** Tempting but
  unpredictable for a developer who always has a new session at hand.
  Rejected.
- **Auto-restart on update.** Big footgun: an in-progress turn would
  vanish without warning. We require explicit restart per session.

## References

- requirements.md R3 OOS, R5, R9, §15.8, §16.
- install-and-update.md §4.
- data-model.md §5 (`[image]` block in config).
