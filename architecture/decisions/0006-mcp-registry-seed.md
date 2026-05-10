# ADR 0006 — MCP registry seed source (§15.6)

- **Status:** Accepted.
- **Date:** 2026-05-09.
- **Deciders:** Architect.

## Context

The MCP registry is install-wide and seeded at `agentctl init`. The seed
needs to ship with `agentctl` somehow:

- Embedded in the binary?
- Fetched from a known internal URL at install time?
- Manually entered by the developer?

R5's "team's known internal MCPs" implies team-curated content; we want
the team to be able to override the default without forking.

## Decision

A layered shipped config:

1. The `agentctl` binary embeds a default `registry.seed.toml` (the
   open-source baseline: typically just the GitHub MCP entry).
2. Site override: `/etc/agentctl/registry.seed.toml`. If present,
   replaces the embedded default entirely.
3. User override: `~/.config/agentctl/registry.seed.toml`. If present,
   replaces the site default entirely (so a developer can hand-craft
   their personal seed).
4. Resolution: first match wins (user → site → embedded).
5. `agentctl init` and `init --repair` apply the resolved seed via
   `INSERT OR IGNORE` keyed on `name`. Re-running never overwrites
   user edits in the registry table.

Format: TOML (consistent with `config.toml`):

```toml
[[mcp]]
name = "github"
url = "https://api.githubcopilot.com/mcp/"
kind = "github_pat"     # freeform; v1 knows "none" and "github_pat"
default_enabled = true
description = "GitHub MCP server."
# auth_config = { ... }  # optional, kind-specific JSON; v1 kinds need none
```

No network fetch happens during `init`. Reasons:

- Offline-installable (R1 acceptance criterion: clean machine, 2 min).
- Deterministic init runs (a transient network blip on the seed host
  shouldn't change the seed).
- Avoids a "phone home" surface that could confuse the no-telemetry
  promise.

Teams that want to push updated seeds to many users use whatever
mechanism distributes `/etc/agentctl/registry.seed.toml` (config-mgmt
tooling, MDM).

## Consequences

- A team can ship a custom build of `agentctl` with their seed
  embedded, **or** distribute `/etc/agentctl/registry.seed.toml` with
  their config tooling.
- Individual developers can override per machine without touching
  `/etc`.
- Once a registry row exists, no re-seed action will overwrite it.
  Cleanups go through `agentctl mcp remove`.
- A team that wants to *remove* a seed entry from existing installs has
  to do so explicitly (`agentctl mcp remove …` plus an updated seed
  going forward).

## Alternatives considered

- **Internal URL fetched at init.** Rejected per offline / determinism
  / no-telemetry reasoning above.
- **Single embedded seed (no overrides).** Locks teams in; would force
  forks for customization.
- **Manual entry only.** Bad first-run UX. Rejected.

## References

- requirements.md R5, §15.6.
- data-model.md §1, §2 (`mcp_registry`).
- install-and-update.md §2.2 (init step 8).
