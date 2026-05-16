# CODEX_PROVIDER_PLAN — implementation plan for ADR 0020

Implementation plan for `architecture/decisions/0020-openai-codex-provider.md`
(OpenAI Codex as a second agent provider). Five phases, each shippable on
its own; the ADR is the source of truth — if this plan disagrees with the
ADR, the ADR wins.

This doc references file paths and line numbers from the current tree
(integration branch state). Line numbers will drift as code lands; treat
them as anchors, not contracts.

## TL;DR

| Phase | Headline | Key files | Acceptance |
|---|---|---|---|
| 1 | Codex end-to-end with API keys | `secrets.go`, `paths.go`, `sm/manager.go`, `image/Dockerfile`, `image/shim/`, `config.go`, `store/migrations/`, `cmd/agentctl/`, built-in agent YAMLs | Built-in `bug-investigator` runs on either provider, one full turn including a tool call, against `api.openai.com` with `OPENAI_API_KEY` |
| 2 | OAuth helper for Codex | `image/auth.Dockerfile`, `internal/cli/auth.go`, `internal/paths/paths.go`, `internal/sm/manager.go` | Subscription-billed Codex session works after `agentctl auth login --provider openai`, no `OPENAI_API_KEY` set |
| 3 | Orchestration as the headline | Built-in assembly-line YAMLs, web run-view, `internal/cli/render.go`, docs | Built-in mixed-provider line completes a bug-fix end-to-end with correct per-stage usage attribution |
| 4 | Mid-session model switch | `internal/cc` (control frames), `image/shim/runtime/*`, web header, CLI `/model` interceptor, `cmd/agentctl/session`, `store/migrations/` | `claude-sonnet` ↔ `claude-opus` and `gpt-5.5` ↔ `gpt-5.3-codex` mid-conversation; usage rows correctly tagged across the transition; idle-resume works on both sides |
| 5 | Custom endpoints / gateways | `cmd/agentctl/init`, `internal/sm/manager.go` (`writeSecretsEnv`), validation hop | Codex session against an OpenAI-compatible gateway completes one full turn with zero traffic to `api.openai.com` |

## Cross-cutting prerequisites (land in phase 1, used by every later phase)

These are shape decisions whose surface lands in phase 1 so later phases
don't need to reopen the wire/schema work. Track them as a single ordered
sub-list inside phase 1 rather than separate PRs; they're tightly coupled.

1. **`provider` is a first-class field everywhere.**
   - `internal/secrets/secrets.go` (lines 33–51) — `Secrets` struct gains
     `OpenAIAPIKey`, `OpenAIAuthMode`, `OpenAIBaseURL`, `OpenAIAuthToken`
     fields. The struct shape is final at the end of phase 1 even though
     `OpenAIBaseURL` / `OpenAIAuthToken` aren't wired until phase 5 — this
     avoids a secrets-file migration later.
   - New helpers: `ResolvedOpenAIAuthMode() string` (mirror of the existing
     `ResolvedAuthMode()` at line 46) and `EnabledProviders() []string`
     (new; the single check used by the resolver in §3 of the ADR).
   - `internal/sm/manager.go` `CreateRequest` (lines 49–64) gains
     `Provider string`. `writeSecretsEnv` (lines 655–706) branches on it.
   - `internal/store/migrations/` — new migration `0002_provider.sql`
     adds `provider TEXT NOT NULL` to `sessions`. Backfill existing rows
     with `'anthropic'`. The column has no `DEFAULT`; the session manager
     always writes it.
   - `internal/tm/manager.go` `StartStageInput` (lines 66–85) threads
     provider through to session create.

2. **The resolution algorithm has exactly one implementation.**
   - New file `internal/secrets/resolve.go` (or co-located in
     `internal/sm/`) holds the lookup described in ADR §3. Every entry
     point — CLI `agentctl start`, web `POST /api/sessions`,
     `tm.SessionRuntime` stage spawn — calls it. Do not inline the
     fallback chain anywhere else.
   - `workspace.last_used_provider` lives in the workspace's session
     store (sqlite, same db as `sessions`) — not `config.toml`. New
     column on a workspace-level table, or a small `workspace_state`
     key/value table if none exists.

3. **`config.toml` shape change is non-breaking.**
   - `internal/config/config.go` `ModelSection` (lines 46–48) gains
     `AnthropicDefault string` and `OpenAIDefault string`. The existing
     `Default` field is kept as a fallback for one release so existing
     `config.toml` files don't break; on load, if `AnthropicDefault` is
     empty and `Default` is set, copy `Default` into `AnthropicDefault`.
   - `PricingSection` (lines 50–64) is already version-keyed and
     model-id-keyed; no schema change. New pricing rows are added for
     the OpenAI models the v1 ships against (`gpt-5.5`, `gpt-5.3-codex`).
   - Defaults block (lines 83–95) updated accordingly.

4. **Provider invisibility when only one is enabled** (ADR §UX principles).
   - All new CLI surface (`--provider` flag, `auth login --provider`,
     `auth status` provider column) is gated on
     `len(secrets.EnabledProviders()) >= 2`. While only one is enabled,
     the byte-for-byte Anthropic-only flow is preserved.
   - Same for the web — provider chips and dropdowns are conditionally
     rendered.

---

## Phase 1 — Codex end-to-end with API keys

**Goal (from ADR):** built-in `bug-investigator` runs on either provider
depending on what the user has configured — no YAML edits required — and
completes one full turn including a tool call against
`https://api.openai.com` with `OPENAI_API_KEY` from `secrets.json`.

### 1.1 Secrets + paths + config

- `internal/secrets/secrets.go` (lines 33–51): add the four OpenAI fields,
  `ResolvedOpenAIAuthMode()`, `EnabledProviders()`. Bump `V` constant if
  the load path uses it for forward-compat (check lines 53–80).
- `internal/paths/paths.go` (lines 26–27, 56–57): add `CodexCredsDir =
  ~/.config/agentctl/codex/` and `CodexCredsFile = .../auth.json`.
- `internal/config/config.go`:
  - `ModelSection` (lines 46–48) → `AnthropicDefault`, `OpenAIDefault`,
    plus the legacy `Default` fallback.
  - Defaults (lines 83–95) → `anthropic_default = "claude-sonnet-4-6"`,
    `openai_default = "gpt-5.5"` (placeholder — verify exact id at
    implementation time, see ADR §Items to verify).
  - Pricing table v1 (lines 86–95) → add `gpt-5.5` row. Pricing numbers
    are placeholders pending confirmation; the structure is what matters.

### 1.2 Storage migration

- `internal/store/migrations/0002_provider.sql`: `ALTER TABLE sessions
  ADD COLUMN provider TEXT NOT NULL DEFAULT 'anthropic'`; on a second
  statement, drop the default so future inserts must specify it (sqlite
  needs the table-rebuild trick — copy `migrations/0001_initial.sql`
  patterns; if a `DEFAULT` cannot be dropped without rebuild, leave the
  default and rely on the SM always writing the field explicitly).
- New `workspace_state` table (if none exists) or new column on the
  existing workspace table for `last_used_provider`.
- Update the schema doc reference at the top of
  `architecture/data-model.md` §5.

### 1.3 Session manager wiring

- `internal/sm/manager.go`:
  - `CreateRequest` (lines 49–64): add `Provider string`. Validate
    non-empty at the top of `Create`.
  - `writeSecretsEnv` (lines 655–706): branch on `in.Provider`. The
    Anthropic branch is the existing body. The OpenAI branch:
    - API-key mode (default in phase 1): `OPENAI_API_KEY=<key>`.
    - OAuth mode: inject nothing (phase 2 owns the bind-mount).
    - Custom-endpoint mode: inject `OPENAI_BASE_URL` + `OPENAI_API_KEY`
      (the struct field exists in phase 1 but the flag/UX is phase 5).
  - `provisionContainer` (lines 437–607): no mount changes in phase 1
    (no OAuth yet). Just ensure the provider value is threaded into the
    `agentd.greet` payload the shim receives.
- `internal/cc` control frame: extend the existing `greet` payload to
  carry `provider`. Verify the wire format in
  `architecture/container-and-image.md` §2.5–§2.6 — if the frame is
  versioned, bump the version; if it's a free-form map, just add the
  key. The shim treats a missing field as `"anthropic"` for one release
  so a session started before the daemon upgrade still works.

### 1.4 Shim driver split

- `image/shim/` layout after this phase:
  ```
  image/shim/
    __main__.py                  # dispatcher; chooses driver from greet.provider
    runtime/
      __init__.py                # factory: provider -> driver instance
      translate.py               # shared vocabulary (assistant.delta, ...,
                                 # turn.start, turn.end) — extracted from runtime.py
      claude_driver.py           # current ClaudeSDKClient logic, lightly refactored
      codex_driver.py            # NEW
  ```
- Refactor `image/shim/__main__.py` (lines 49–150) to read
  `greet_data.provider` and dispatch into the factory. Existing dispatch
  is Claude-only.
- Extract event constants from `image/shim/runtime.py` (lines 30–37) into
  `runtime/translate.py`. Both drivers import them; no behaviour change
  for Claude.
- `codex_driver.py` — minimal viable implementation:
  - One subprocess per turn:
    ```
    codex exec --json \
      --model <M> \
      --sandbox workspace-write \
      --ask-for-approval never \
      --cd /work \
      [--resume <sid>] \
      <prompt>
    ```
  - Parse stdout JSONL; map Codex event kinds to the internal
    vocabulary (`assistant.delta`, `assistant.message`, `tool.call`,
    `tool.result`, `usage`, `turn.start`, `turn.end`).
  - On first turn, capture the Codex session id from the JSONL and
    return it to agentd so it can be persisted as `sdk_session_id`
    (already a column per `0001_initial.sql` line 13ish — reuse the
    field; it's provider-agnostic, just stores whatever the driver
    gives it).
  - Interrupt = kill subprocess + emit `turn.cancelled`.

### 1.5 Image

- `image/Dockerfile` (lines 1–69):
  - Add `ARG CODEX_CLI_VERSION=<pinned>` near the existing version pins.
  - Install Node 20 + `npm install -g "@openai/codex@${CODEX_CLI_VERSION}"`
    alongside the existing `claude-agent-sdk==0.1.80` install (line
    45–50).
  - Verify final image size delta — target ~50–100 MB per ADR §Losses,
    within the ADR 0014 budget.

### 1.6 CLI surface

- `internal/cli/init.go` (lines 33–46): add `--openai-key` (mirroring
  `--anthropic-key`). `--openai-base-url` / `--openai-auth-token` flags
  are declared but hidden until phase 5; or omit them entirely and add
  in phase 5 (preference: omit, keep `init` flags honest about what
  works today).
- Validation: hit `GET https://api.openai.com/v1/models` with
  `Authorization: Bearer <key>` (mirror of the Anthropic validator).
  Tolerate gateway 404s in phase 5 — not a concern in phase 1.
- Session-create entry points (`internal/cli/start.go` or wherever
  `agentctl start` flags are defined; check `cmd/agentctl/`): add
  `--provider {anthropic|openai}` gated on `EnabledProviders()` count.
  When only one provider is enabled the flag is omitted from `--help`.

### 1.7 Built-in agents

- Drop the hardcoded `model:` from
  `internal/ttl/builtins/agents/bug-investigator.yaml`,
  `bug-planner.yaml`, and `bug-executor.yaml`. Do not add `provider:`
  — the resolver picks one. The same YAML now runs on either provider.
- Quick visual check that the prompts don't bake in Anthropic-specific
  vocabulary (e.g. "use Claude's"); if any do, neutralize them.

### 1.8 Agent-create gating

- New daemon endpoint `GET /api/providers` (sibling to `/v1/agents` in
  `internal/websrv/server.go` lines 140–150). Response shape from ADR §9:
  ```json
  {
    "anthropic": {"enabled": true,  "default_model": "...", "models": [...]},
    "openai":    {"enabled": false, "default_model": "...", "models": [...]}
  }
  ```
  The `models` array is read straight from `[pricing.tables.models]` —
  no parallel catalog.
- CLI agent-write paths and `POST /api/agents` reject `provider` values
  not in `EnabledProviders()`. Hint message matches the session-create
  error (point at `agentctl init` / `agentctl auth login`).

### 1.9 Tests

- `internal/secrets/secrets_test.go`: `EnabledProviders` truth table —
  zero, one (anthropic), one (openai), both.
- `internal/sm/manager_test.go`: `writeSecretsEnv` for the OpenAI
  api-key branch; absent `Provider` rejected at `Create`.
- New `image/shim/runtime/codex_driver_test.py` (or similar): feed it
  a captured `codex exec --json` transcript; assert it emits the
  expected internal event sequence.
- E2E: a Go test (or a shell harness like `test/` already uses) that
  starts a session with `Provider: "openai"` and a stubbed Codex CLI,
  drives one turn, verifies the `usage` row written.

---

## Phase 2 — OAuth helper for Codex

**Goal (from ADR):** a full subscription-billed Codex session works
after `agentctl auth login --provider openai`, with no `OPENAI_API_KEY`
ever set.

### 2.1 Auth helper image

- `image/auth.Dockerfile` (currently lines 1–30, Anthropic-only):
  - Add `ARG PROVIDER` (default `anthropic` for backward compat with
    any external callers; the CLI will always pass it explicitly).
  - Install both `@anthropic-ai/claude-code` and `@openai/codex` so
    the same image can launch either flow.
  - Entrypoint becomes a small `entry.sh` (or inline) that branches:
    - `PROVIDER=anthropic` → existing `CLAUDE_CONFIG_DIR=/creds` +
      `claude auth login`.
    - `PROVIDER=openai` → `CODEX_HOME=/creds` + `codex login
      --device-auth`. The device-auth flow prints a code + URL; the
      user completes it in a host browser. This is the Codex
      equivalent of Claude's paste-fallback.
  - Verify uid stays 1000 and `/creds` perms match the host-bound
    directory layout (the existing helper writes there as 1000).

### 2.2 CLI

- `internal/cli/auth.go`:
  - `auth login` (lines 80–178): add required `--provider {anthropic,
    openai}` flag. Branch on it to choose:
    - the host creds dir to bind-mount (`ClaudeCredsDir` vs
      `CodexCredsDir`),
    - the `PROVIDER` build/run arg for the helper image,
    - the post-login creds-file check (`claude/.credentials.json` vs
      `codex/auth.json`).
  - `auth status` (lines 54–78): print both providers side-by-side, e.g.
    ```
    anthropic   oauth (creds: ~/.config/agentctl/claude/.credentials.json)
    openai      api_key (OPENAI_API_KEY set in secrets.json)
    ```
    Gated per the §UX-principles rule: when only one provider is
    configured, keep the single-line output unchanged.

### 2.3 Session manager — OAuth bind-mount

- `internal/sm/manager.go`:
  - New `resolveCodexCredsBindSource()` mirroring
    `resolveClaudeCredsBindSource()` (lines 389–414). Same shape: load
    secrets, verify `ResolvedOpenAIAuthMode() == oauth`, verify
    `m.opts.CodexCredsDir` is set, verify `auth.json` exists non-empty.
  - `provisionContainer` (lines 437–607):
    - **Default — symlink Codex creds onto the tmpfs (ADR §8 default).**
      Keep `/home/agent` tmpfs at lines 565–568 unchanged. Bind-mount
      the host creds dir at a known subpath of the session volume
      (e.g. `/work/.codex-creds`), and have the entrypoint symlink
      `~/.codex/ → /work/.codex-creds`. This is the same pattern the
      Claude path already uses (`/work/.claude` → `/home/agent/.claude`).
    - **Fallback — drop the tmpfs.** Only adopt if a concrete blocker
      surfaces during implementation. The change touches existing
      Claude sessions, so it carries blast-radius risk.
- `internal/sm/options.go` (or wherever `Manager.opts` is defined):
  add `CodexCredsDir`. Wire from `paths.Layout.CodexCredsDir` at
  manager construction time.

### 2.4 Tests

- `auth login --provider openai` happy path (mock the docker invocation;
  assert the env / bind args).
- `resolveCodexCredsBindSource` truth table: not configured / file
  missing / file empty / good.
- E2E: a session created with `Provider: "openai"` and OAuth mode —
  inject a stub `auth.json`, start session, assert no `OPENAI_API_KEY`
  in container env and the bind-mount is wired.

### 2.5 Open question (verify in this phase, per ADR)

- That `--device-auth` ships stably in the pinned `@openai/codex`
  version. If not, this phase ships as "API-key only for OpenAI" and
  device-auth lands in a follow-up.

---

## Phase 3 — Orchestration as the headline

**Goal (from ADR):** a built-in mixed-provider line completes a
bug-fix end-to-end with correct per-stage usage attribution, and the
run view makes the provider/model of each stage obvious without
drilling in.

The infra is already there: `tm.SessionRuntime` (`internal/tm/manager.go`
lines 37–90) spawns a fresh container per stage; each stage's agent
already resolves independently. This phase is built-in YAMLs, render
polish, and docs.

### 3.1 Built-in mixed-provider assembly line

- Add a new built-in line under `internal/ttl/builtins/lines/`
  (path/name to be confirmed in the existing layout). Stages:
  - `investigator` — agent: `bug-investigator`, `provider: anthropic`,
    model inherits from `anthropic_default` (Opus recommended via the
    future `recommended` field on `/api/providers` — for now hard-pin
    the model on this one built-in if Opus quality is the point).
  - `executor` — agent: `bug-executor`, `provider: openai`, model
    inherits from `openai_default`.
- The agents themselves stay provider-portable (phase 1 dropped their
  hardcoded model); the assembly-line YAML is what pins providers.

### 3.2 Run-view surface

- Web SPA run view: for each stage, render a `provider/model` chip
  inline with the stage status. Source: the stage's session row
  (`provider`, `model` columns). No new endpoint needed.
- `internal/cli/render.go` (lines 32–114, currently provider-agnostic):
  when printing stage transitions in CLI mode, prefix the stage with
  `[provider/model]` so a tail of an assembly-line run shows the
  per-stage runtime identity. Gate behind two-provider visibility
  unless the stage YAMLs explicitly mix.

### 3.3 Docs / `--help`

- `agentctl start --line <name>` help text: one-line note that stages
  may run on different providers; point at the new built-in.
- `architecture/overview.md` and/or `assembly-lines-task-management.md`:
  add a paragraph framing agentctl as "orchestrator across providers"
  (per ADR §UX principles — orchestration is the destination, not
  provider count).
- The built-in line gets a short blurb in `README.md`.

### 3.4 Tests

- E2E shell test (matches `test/` style): run the mixed-provider line
  with both providers' creds configured (or stubs); assert per-stage
  `usage` rows have the right `model` column, and the run view payload
  surfaces per-stage provider+model.

---

## Phase 4 — Mid-session model switch

**Goal (from ADR):** switch `claude-sonnet` → `claude-opus` mid-
conversation; switch `gpt-5.5` → `gpt-5.3-codex` mid-conversation;
usage rows are correctly tagged across the transition; resume from
idle continues to work on both sides of a switch.

This is where ADR 0003's model-immutable rule is reversed (provider
takes over as the immutable field).

### 4.1 Schema

- New migration `internal/store/migrations/0003_mutable_model.sql`:
  there's no DDL change in sqlite for relaxing immutability (the
  column was never declared immutable structurally), but document
  the policy change at the top of `data-model.md` §5 and update the
  reference to ADR 0003 to note the supersession.
- `usage` rows already tag by runtime-reported model id (per ADR 0003
  §rule); confirm no code path overrides this with `sessions.model`
  at write time. Check the usage-writing path in
  `internal/sm/manager.go` or wherever `usage` rows are persisted.

### 4.2 Control frame

- New control frame `agentd.set_model { model }`. Define it in
  `architecture/api.md` §5 alongside the existing event vocabulary
  (this is a control message, not an event — match the existing
  control-frame conventions in §4 or wherever they live).
- Daemon-side handler in `internal/cc` / `internal/sm`:
  - Validate the requested model belongs to the session's provider
    (lookup `[pricing.tables.models]`, cross-reference with provider
    catalog). Reject with a typed error.
  - Update `sessions.model` in storage.
  - Forward the frame to the shim.

### 4.3 Shim drivers

- `image/shim/runtime/claude_driver.py`: implement `set_model(new_model)`:
  - Tear down and re-instantiate `ClaudeSDKClient` with the new model.
  - Preserve `resume=<sdk_session_id>` so the new client picks up the
    same JSONL transcript.
  - Document the assumption: idle-resume already exercises this path,
    so reconnection mid-session is the same code path with new options.
- `image/shim/runtime/codex_driver.py`: implement `set_model(new_model)`:
  - Simply update the model captured by the driver; the next
    `codex exec --json` call passes `--model <new>`. No reconnect
    needed (Codex is one subprocess per turn already).

### 4.4 Web UX (primary)

- Web SPA session header: replace the model display with a dropdown,
  populated from `/api/providers[session.provider].models`, current
  value `session.model`. On change → `PATCH /api/sessions/<id>` with
  `{model: "..."}`. Server validates per §4.2 and emits
  `agentd.set_model`.
- API: extend `PATCH /api/sessions/<id>` (or add it if it doesn't
  exist) to accept `model`. Continue to reject `provider` per ADR §1.

### 4.5 CLI / slash command (secondary)

- `/model <name>` interceptor inside the chat input renderer (CLI and
  web both). Catches the message before it reaches the driver, fuzzy-
  matches against the provider's enabled models, calls the same
  `PATCH /api/sessions/<id>` path. Echo a system line in the
  transcript: `model switched: <old> → <new>`.
- `agentctl session set-model <session-id> <new-model>` subcommand
  under `cmd/agentctl/` — scripting surface, NOT surfaced in `--help`
  examples per ADR §UX principles. Implementation calls the same API.

### 4.6 Tests

- Driver-level unit: feed `set_model`, assert next turn uses the new
  model and history is preserved.
- E2E: open session, send turn on model A, switch to model B, send
  turn, verify both `usage` rows have the right `model` tag.
- Resume test: switch model, detach (idle), reattach — same session
  resumes correctly with the new model.

### 4.7 Open question (per ADR)

- Verify the Claude `resume=<sid>` reliably picks up the same JSONL on
  reconnect after a model change (it does today on idle-resume; this
  is the same path, but worth a smoke test).

---

## Phase 5 — Custom endpoints / gateways

**Goal (from ADR):** a Codex session against an OpenAI-compatible
gateway (Azure OpenAI, vLLM, or similar) completes one full turn with
no traffic to `api.openai.com`.

### 5.1 CLI flags

- `internal/cli/init.go` (lines 33–46): add `--openai-base-url` and
  `--openai-auth-token` mirroring the existing `--anthropic-base-url`
  / `--anthropic-auth-token`. The struct fields landed in phase 1.
- Validation: best-effort `GET <base-url>/v1/models` with the auth
  token. Gateways may not expose `/v1/models`; treat 4xx as
  "configured but unverified", print a hint, and continue. Network
  errors / TLS errors are hard fails.

### 5.2 Session manager — env injection

- `internal/sm/manager.go` `writeSecretsEnv` (lines 655–706, in the
  OpenAI branch added in phase 1):
  - If `OpenAIBaseURL` is set: inject `OPENAI_BASE_URL=<url>` and
    `OPENAI_API_KEY=<OpenAIAuthToken>` (the codex CLI takes the
    bearer as `OPENAI_API_KEY` regardless of whether it's an OpenAI
    key or a gateway token — confirm at impl time against the pinned
    codex CLI version).
  - Otherwise: existing API-key behaviour.
  - OAuth mode + custom endpoint is rejected (or treated as
    API-key-via-token) — the ChatGPT OAuth flow doesn't make sense
    against a third-party gateway. Decide at impl time; the simplest
    rule is "custom-endpoint disables OAuth for that provider, surface
    a warning in `auth status`".

### 5.3 Tests

- Unit: `writeSecretsEnv` truth table for the OpenAI branch — API key,
  OAuth, custom endpoint, custom endpoint + bad combinations.
- E2E (manual / opt-in): point at a real gateway (Azure OpenAI in CI
  secrets, or a vLLM dev box), run one turn, confirm no DNS resolution
  for `api.openai.com` in the container (`tcpdump` in the harness, or
  the existing network-policy probe from ADR 0013).

---

## Verification matrix

| What | Phase | How |
|---|---|---|
| Resolver picks the right `(provider, model)` from each entry point | 1 | `internal/secrets` table-driven tests |
| Built-in `bug-investigator` is portable across providers | 1 | E2E starts it twice, once per provider, asserts no YAML edits required |
| Codex driver emits the internal event vocabulary | 1 | Captured `codex exec --json` transcript replay |
| One image carries both runtimes | 1 | Image build CI, size check vs ADR 0014 budget |
| `auth status` shows both providers when both enabled | 2 | CLI test |
| Codex OAuth bind-mount lands at `~/.codex` with `mode=0700` | 2 | Session-spawn fixture |
| Mixed-provider line: per-stage usage attribution correct | 3 | E2E |
| Mid-session switch preserves history (Claude) | 4 | E2E |
| Mid-session switch updates `--model` flag (Codex) | 4 | Driver unit test |
| Resume from idle works after switch | 4 | E2E |
| Switch to a cross-provider model is rejected | 4 | API test |
| Gateway session: zero traffic to `api.openai.com` | 5 | `tcpdump` in harness |

## Risks and open items (cross-reference ADR §Items to verify)

1. **`gpt-5.5` model id / pricing** — placeholder in `config.toml`
   pricing table. Confirm before phase 1 ships; easy fix in TOML once
   verified.
2. **`--device-auth` stability** — verified in phase 2. Fallback: ship
   phase 2 as "API key only for OpenAI" if upstream isn't ready.
3. **`--sandbox workspace-write --ask-for-approval never` agent stalls**
   — verified in phase 1. Fallback: `--sandbox danger-full-access`
   (the container is itself the sandbox).
4. **Codex JSONL event-schema stability** — pin
   `CODEX_CLI_VERSION` and watch upstream for breakage, same pattern
   as `claude-agent-sdk==0.1.80`.
5. **Claude `resume=<sid>` after model swap** — verify in phase 4
   smoke test before the feature ships.
6. **`/home/agent` tmpfs vs Codex bind-mount** — symlink approach
   (ADR §8 default) is the plan of record. If a concrete blocker
   surfaces in phase 2, fall back to dropping the tmpfs, but that
   change touches existing Claude sessions and needs a separate
   regression pass.
7. **Custom-endpoint + OAuth combination** — rule TBD in phase 5;
   simplest is "custom endpoint disables OAuth for that provider,
   warn in `auth status`".

## References

- `architecture/decisions/0020-openai-codex-provider.md` — the ADR
  this plan implements.
- `architecture/decisions/0003-default-model.md` — superseded in part
  by this ADR (model becomes mutable, provider takes over the
  immutability role).
- `architecture/decisions/0014-local-image-build-and-skill-mounts.md`
  — image-size and build-time budget.
- `architecture/decisions/0018-sm-cm-cc-adapters.md` — sm↔cm/cc
  adapter pattern, relevant to the control-frame work in phase 4.
- `architecture/container-and-image.md` §2.5–§2.6 — shim ↔ runtime
  contract; this is the layer the driver split lives in.
- `architecture/api.md` §5 — event vocabulary the drivers translate
  into; §4 (or wherever) for the control frames phase 4 extends.
- `architecture/data-model.md` §5 — `sessions.model`, `sessions.provider`
  (new), `usage.model`.
