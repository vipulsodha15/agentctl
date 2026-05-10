# ADR 0002 — Repo onboarding flow (§15.2)

- **Status:** Accepted.
- **Date:** 2026-05-09.
- **Deciders:** Architect (delegated by §15).

## Context

A v1 session runs in a container and does coding work. Repos must enter
the container somehow. Options:

1. `agentctl start --repo <git_url>` clones at session start using the
   PAT.
2. The agent runtime running inside the container `git clone`s when
   instructed.
3. Bind-mount an existing host checkout into `/work/<repo>`.

R7's isolation guarantees rest on `/work` being a per-session volume
that ends with `agentctl stop`. Option 3 punctures that: a host checkout
under `/work` would let a runaway agent modify the developer's working
tree without the volume blast-radius cap, and two sessions could share
overlapping mounts.

## Decision

v1 ships with **only** options 1 and 2. Host bind-mounts are explicitly
rejected.

- `agentctl start --repo <url>` clones each repo into
  `/work/<basename>` before `runtime.ready` is announced.
- The agent's `git clone` works because the GitHub PAT is wired into
  both env (`GITHUB_TOKEN`) and the per-session git credential helper
  (`/work/.config/git/credentials`).
- The shim records each cloned repo's branch and SHA to
  `/work/.agentctl/repo-bases.json` for diff-vs-base (R8). (Distinct
  from `/work/.claude/`, which is the SDK's conversation-history
  directory; renamed from the original `/work/.history/` to avoid
  confusion with the SDK's path.)

## Consequences

- Developers with unpushed local edits cannot directly feed them into a
  session. They must `git push` to a temporary branch (or remote) and
  `--repo` it. We accept this friction for v1 in exchange for a clean
  isolation story.
- The `--repo` flag accepts multiple URLs; cloning is parallelized in
  the shim, capped at 4 in flight.
- Per-repo clone failures don't abort the session; they emit
  `repo.clone_failed` events. The user can retry by having the agent
  clone in-session.
- Future "workspace mount with explicit risk acknowledgement" remains
  available as a v2 design call; it would be additive.

## Alternatives considered

- **Bind-mount with `--ro`:** read-only mounts let the agent read but
  not write. Useful for "look at my code" but blocks edits, which is
  the main value-add. Adds a third mode that complicates the data
  model.
- **Copy-on-import:** `agentctl start --import-from <path>` copies the
  host directory into `/work/<basename>` at start. Tempting but
  introduces a one-way data flow that confuses developers ("why isn't
  my edit on disk?"). Defer to v2.

## References

- requirements.md R3, R7, R8, §15.2.
- container-and-image.md §2.4, §3, §6.
