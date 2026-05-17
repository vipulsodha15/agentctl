# ADR 0020 — OpenAI Codex as a second agent provider

- **Status:** Proposed.
- **Date:** 2026-05-16.
- **Deciders:** Product owner.
- **Supersedes (in part):** 0003 — "Live model-switching mid-session is
  not supported" is reversed: the model becomes mutable for the life of
  a session (the *provider* takes over as the immutable-at-create field
  that 0003 assigned to the model).

## Context

agentctl ships a single agent runtime today: the Python `claude-agent-sdk`
driving the bundled `claude` CLI inside every session container. Auth is
Anthropic-only (`ANTHROPIC_API_KEY` injected into the container, or the
`~/.config/agentctl/claude/.credentials.json` OAuth bundle bind-mounted
at `/home/agent/.claude` when the user has run `agentctl auth login`).
Per ADR 0003 the model is per-session and frozen for the session's
lifetime; cost rows in `usage` are tagged by `sessions.model`.

OpenAI now ships an analogous stack — an open-source `codex` CLI plus a
TypeScript SDK (`@openai/codex-sdk`) and an experimental Python SDK
(`openai-codex` / `codex_app_server`). Both SDKs spawn the `codex` CLI
as a subprocess and exchange JSONL (TS) or JSON-RPC v2 (Python) over
stdio. Auth mirrors Claude's: an `OPENAI_API_KEY` env var, or a
ChatGPT-subscription OAuth flow (`codex login`, with a `--device-auth`
variant for headless environments) that writes tokens to
`~/.codex/auth.json`. Session history persists to `~/.codex/sessions/`.

Users have asked for the ability to run sessions on OpenAI without
abandoning the Anthropic workflow they already have, and to mix providers
across the agents that make up an assembly line (e.g. investigate on
Claude, execute on Codex). The challenge is doing this without
double-walking the auth+runtime path that took us a release to harden
for Anthropic.

The symmetry between the two stacks is high enough that the integration
is mostly a generalization, not a parallel implementation.

## Decision

**Provider becomes a first-class dimension on sessions and agents.**
Concretely:

### 1. `provider` is a session-set-once, agent-optional field.

- `sm.CreateRequest` gains `Provider string`. The `sessions` table gains
  `provider TEXT NOT NULL`. The session manager always writes it at
  create; it is never mutated after.
- `ttl.Agent` gains an optional `Provider string` (`provider:` in YAML).
  When unset, the resolver below picks one at session-create time —
  this is what makes built-in agents portable across providers without
  per-provider YAML.
- The runtime constraint that the provider is immutable for a session's
  lifetime is enforced both at the API (`PATCH /api/sessions/<id>`
  rejects any `provider` field) and structurally — the shim's driver
  is selected once at container boot from the `agentd.greet` frame.

### 2. The *model* becomes mutable post-create.

Surfaces ship in phase 4 (see §Phasing); the design is fixed here so
the wire/schema work in phases 1–2 doesn't need to be reopened.

- `sessions.model` becomes writable. ADR 0003's "frozen for the
  session's lifetime" rule is reversed; in exchange, the provider takes
  over as the set-once field, which is what 0003 was actually trying to
  protect (immutable runtime identity for the duration of a
  conversation).
- **Primary UX:** a model dropdown in the session header (web),
  filtered to the models available under the session's provider — this
  is where the user is already looking at the current model and where
  the change feels in-place rather than a new command to remember.
- **Secondary UX:** `/model <name>` inside the chat input, intercepted
  by the renderer before the message reaches the driver. Fuzzy match
  against the provider's enabled models (e.g. `/model opus` resolves to
  the current Anthropic Opus id).
- **Scripting surface:** `agentctl session set-model <session-id>
  <new-model>` exists for automation but is not surfaced in `--help`
  examples — users discover the dropdown and the slash command first.
- New control frame `agentd.set_model { model }` carries the change to
  the shim. The driver re-instantiates its underlying client with the
  new model, preserving `resume=<sdk_session_id>` (Claude) or simply
  passing `--model` on the next `codex exec` call (Codex).
- `usage` rows continue to be tagged by the runtime-reported model id
  (already the rule per 0003), so cost attribution remains correct
  across mid-session switches.
- Switching to a model that doesn't belong to the session's provider is
  rejected at the API layer with a typed error.

### 3. Resolution algorithm — single source of truth.

When opening a session (from CLI flags, an agent, or an assembly-line
stage), `(provider, model)` are resolved by one helper:

```
provider := agent.provider
         OR cli.provider
         OR workspace.last_used_provider      // sticky per workspace
         OR sole_enabled_provider              // when exactly one is configured
         OR fail("multiple providers enabled; pass --provider or pick one in the UI")

if provider not in secrets.EnabledProviders():
    fail("provider %s not configured; run agentctl auth login --provider %s
         or agentctl init --<provider>-key …", provider, provider)

model := agent.model
      OR cli.model
      OR config.model[provider+"_default"]   // openai_default, anthropic_default
```

A provider is "enabled" if its credentials resolve: an API key set, OR
a custom-endpoint+token pair, OR an OAuth `auth.json` /
`.credentials.json` present and non-empty. The check lives once in
`secrets.EnabledProviders()`.

The `workspace.last_used_provider` slot is persisted in the workspace's
session store (not `config.toml`) and updated every time a session is
created. New users have one provider and never see the choice; power
users with both providers configured get sticky behavior tied to the
work they were last doing. See §UX principles for why this replaces a
config-file tiebreak.

This makes built-in agents portable: the shipped `bug-investigator` /
`bug-planner` / `bug-executor` YAMLs drop their hardcoded
`model: claude-opus-4-7` and don't set `provider:`. They inherit per-
provider defaults, so the same built-in runs on whichever provider the
user has configured.

### 4. `config.toml` gains per-provider model defaults.

```toml
[model]
anthropic_default = "claude-sonnet-4-6"
openai_default    = "gpt-5.5"            # placeholder; verify exact id at impl time
```

No `default_provider` tiebreak. Provider ambiguity is resolved
adaptively (sticky last-used-provider, see §3 and §UX principles) rather
than via a config knob users won't think to set. When zero providers are
enabled, session create fails with a hint pointing at `agentctl init` /
`agentctl auth login`.

### 5. Secrets, paths, and auth helper mirror the Anthropic structure.

`internal/secrets/secrets.go`:

```go
type Secrets struct {
    // existing Anthropic fields …
    OpenAIAPIKey    string
    OpenAIAuthMode  string   // "api_key" | "oauth"
    OpenAIBaseURL   string   // optional, openai-compatible gateways — phase 5
    OpenAIAuthToken string   // bearer for custom endpoint — phase 5
}

func (s Secrets) ResolvedOpenAIAuthMode() string
func (s Secrets) EnabledProviders() []string
```

The Anthropic and OpenAI modes are deliberately *not* unified: users
will commonly run Anthropic OAuth alongside an OpenAI API key, and the
modes belong to different credential lifecycles.

`OpenAIBaseURL` / `OpenAIAuthToken` (and the matching
`--openai-base-url` / `--openai-auth-token` flags below) ship in phase
5 with the rest of the gateway story. Phases 1–2 leave those fields
zero and target `api.openai.com` directly. The struct shape lands in
phase 1 so the gateway phase doesn't require a secrets migration.

`internal/paths/paths.go` adds `CodexCredsDir =
~/.config/agentctl/codex/` and `CodexCredsFile = .../auth.json`.

The existing `image/auth.Dockerfile` (node:20-slim, runs as uid 1000,
writes to a host-bound `/creds`) gains `@openai/codex` alongside
`@anthropic-ai/claude-code` and a `PROVIDER` build/run arg that selects
which CLI's login flow to launch. For `anthropic` it runs the current
`claude auth login` flow against `CLAUDE_CONFIG_DIR=/creds`; for
`openai` it runs `codex login --device-auth` with `CODEX_HOME=/creds`
so `auth.json` lands in the host-bound `~/.config/agentctl/codex/`.
Device-auth (print code + URL, user enters on host browser) is the
Codex equivalent of Claude's paste-fallback flow and the right
primitive for a no-callback-port container. One image, one entrypoint,
parametric per provider.

`agentctl auth login` grows a required `--provider {anthropic,openai}`
flag. `agentctl auth status` prints both providers' state side-by-side.
`agentctl init` grows `--openai-key` / `--openai-base-url` /
`--openai-auth-token` flags mirroring the Anthropic ones; validation
hits `GET https://api.openai.com/v1/models` with `Authorization: Bearer
<key>`.

### 6. The container image installs both runtimes.

`image/Dockerfile`:

```dockerfile
ARG CODEX_CLI_VERSION=<pinned>
RUN npm install -g "@openai/codex@${CODEX_CLI_VERSION}"
# claude-agent-sdk pip install stays as-is
```

This adds ~50–100MB to the image and 1–3 minutes to a from-scratch
build — within the budget ADR 0014 documented. A two-image variant
(one per provider) was rejected: it doubles the pinned-image-id slot,
complicates `agentctl update`, and rules out the assembly-line case
where a single workflow uses both providers across stages.

### 7. Shim picks driver from `agentd.greet.provider`.

```
image/shim/
  __main__.py                  # dispatcher; chooses driver from greet.provider
  runtime/
    __init__.py                # factory
    translate.py               # shared event vocabulary (assistant.delta,
                               # assistant.message, tool.call, tool.result,
                               # usage, turn.start, turn.end)
    claude_driver.py           # current ClaudeSDKClient logic, lightly refactored
    codex_driver.py            # NEW
```

The control-channel event vocabulary in `architecture/api.md` §5 is
already provider-agnostic — the CLI renderer (`internal/cli/render.go`)
and the web SPA never inspect provider. Each driver's job is just to
translate vendor messages into that vocabulary.

**The Codex driver shells out to `codex exec --json` rather than using
the Python SDK.** The Python `openai-codex` package is still labeled
experimental, and the JSON-RPC `app-server` it depends on adds a layer
without earning anything we don't already have via `exec`. One
subprocess per turn:

```
codex exec --json \
  --model <M> \
  --sandbox danger-full-access \
  --ask-for-approval never \
  --cd /work \
  [--resume <sid>] \
  <prompt>
```

Stream the JSONL, map `item.completed`/`turn.completed`/`error` events
to the internal vocabulary, ship session id back on first turn so
agentd persists it for resume. Interrupt = kill subprocess + emit
`turn.cancelled`. If the SDK matures we can swap underneath the driver
interface without touching agentd.

### 8. Container session wiring is provider-aware.

`internal/sm/manager.go`:

- `writeSecretsEnv` branches on `provider`. anthropic → existing
  `ANTHROPIC_*` logic. openai → `OPENAI_API_KEY` (or `OPENAI_BASE_URL +
  OPENAI_API_KEY` for the gateway case) **unless**
  `OpenAIAuthMode == oauth`, in which case inject nothing and bind-mount
  creds.
- `resolveCodexCredsBindSource()` mirrors `resolveClaudeCredsBindSource()`.
- In OAuth mode, `provisionContainer` adds `{Source: in.CodexCredsHost,
  Target: "/home/agent/.codex"}` and `Env: ["CODEX_HOME=/home/agent/.codex"]`.

The existing `/home/agent` tmpfs in `manager.go` (mode 0700, uid=1000)
collides with the bind-mount strategy. Two viable shapes:

- **Default — symlink Codex creds onto the tmpfs.** Mirror the existing
  Claude pattern: keep the `/home/agent` tmpfs, bind-mount the host
  creds dir at a known subpath of the session volume, and symlink
  `~/.codex/` to it the same way the entrypoint already wires
  `/work/.claude` → `/home/agent/.claude`. Preserves the tmpfs's
  `mode=0700,uid=1000` security properties for both providers; smaller
  change; reuses a pattern that has already shipped.
- **Fallback — drop the tmpfs and let `/home/agent` come from the
  session volume.** Larger blast radius (changes existing Claude
  sessions too), but gives `/home/agent` durable backing without
  symlinks. Adopt this only if a concrete blocker for the symlink
  approach surfaces during implementation.

### 9. Agent-creation UIs gate on enabled providers.

- New daemon endpoint `GET /api/providers` returns:
  ```json
  {
    "anthropic": {"enabled": true,  "default_model": "claude-sonnet-4-6", "models": [...]},
    "openai":    {"enabled": false, "default_model": "gpt-5.5",           "models": [...]}
  }
  ```
  The `models` array is read directly from `[pricing.tables.models]` in
  the active pricing table (already version-keyed per ADR 0003 / data-
  model §5). Adding a new model = adding a pricing row + bumping the
  image; no separate catalog to keep in sync. The response shape
  intentionally leaves room for a future `recommended` field per
  provider (e.g. `{ "investigator": "claude-opus-4-7", "executor":
  "claude-sonnet-4-6" }`) that phase 3's orchestration UX will populate
  — adding it later is a non-breaking change.
- Web "Create agent" form filters the provider dropdown to enabled
  providers; if only one is enabled the provider control is hidden
  entirely (see §UX principles — provider invisibility).
- CLI agent-write paths and the web `POST /api/agents` reject `provider`
  values that aren't enabled with the same hint message as session
  create.
- Session-create surfaces (CLI `agentctl start`, web UI) apply the same
  filter.

## Consequences

### Wins

- One mental model: provider is to model as agent is to session — set
  once, frozen, immutable. The same resolution algorithm runs whether
  the entry point is a CLI flag, an agent definition, or an assembly-
  line stage.
- Built-in agents become portable across providers without per-provider
  YAML duplication. The same `bug-investigator` runs on whichever
  provider the user has configured.
- Assembly lines can mix providers naturally — each stage's agent
  carries its own provider, and each stage already spawns a fresh
  container via `tm.SessionRuntime`.
- The shim driver split makes adding a third provider later (e.g.
  Google) a matter of adding `gemini_driver.py` plus mirroring the
  secrets / auth-helper plumbing, not redesigning the runtime.
- Mid-session model switch is a frequently-requested capability we
  punted on in ADR 0003. The provider-immutable constraint preserves
  the safety property 0003 cared about (no mid-flight runtime identity
  swap) while loosening the model-immutable constraint that was
  collateral damage.

### Losses

- Larger session image (~50–100MB) and longer first-run build
  (~1–3 min added). Within ADR 0014's budget but worth noting.
- More auth surface to maintain: two providers, three modes each
  (API key, OAuth, custom endpoint), four `agentctl auth` /
  `agentctl init` flag pairs.
- The mid-session model switch requires the Claude driver to tear down
  and reconnect `ClaudeSDKClient` with new options. Need to verify the
  SDK's `resume=<sid>` reliably picks up the same JSONL on the new
  connection (it does today on idle-resume; this is the same path).
- Built-in agents lose their per-agent model pinning. A user who wants
  `bug-investigator` always on Opus will need to either set their
  per-provider default to Opus or fork the agent into a custom one with
  an explicit `model:`.

### Items to verify during the first phase (not blocking the design)

- Exact `gpt-5.5` model id and OpenAI list pricing for the
  `[pricing.tables.models]` table. The OpenAI dev docs currently
  reference `gpt-5.4` / `gpt-5.3-codex`; the user has indicated
  `gpt-5.5` is the target. Easy to change in `config.toml` once
  confirmed.
- That `--device-auth` ships stably in the pinned `@openai/codex`
  version. If not, phase 2 (OAuth) falls back to "API-key only for
  OpenAI" until the upstream stabilizes.
- ~~That `--sandbox workspace-write --ask-for-approval never` gives
  Claude-`bypassPermissions`-equivalent behaviour with no agent stalls
  on approval.~~ **Resolved (fallback adopted):** codex 0.130.0 on
  Linux requires `bwrap` (or its bundled namespacer) for any sandbox
  mode other than `danger-full-access`, and the session container's
  `--cap-drop ALL` + `no-new-privileges` profile blocks the
  `unshare(CLONE_NEWUSER)` both code paths need. Every tool call
  aborted with `bwrap: No permissions to create a new namespace`. The
  driver now ships `--sandbox danger-full-access` since the container
  is itself the sandbox, per this bullet's pre-authorized fallback.
- Codex JSONL event-schema stability — pin a CLI version, watch for
  breaking changes the same way we pin `claude-agent-sdk`.

## Alternatives considered

- **Infer provider purely from model prefix.** Simplest diff, brittle
  for gateway/custom-base-url cases and any future model whose name
  doesn't fit the `gpt-*` / `claude-*` convention. Rejected — explicit
  `--provider` plus per-provider model defaults covers the common cases
  without coupling the resolver to a model catalog.
- **One unified `AuthMode` field.** Forces a single global "I'm in
  OAuth mode" or "I'm in API-key mode" toggle. Rejected because the
  common case is "Anthropic OAuth + OpenAI API key" — different
  providers, different credential lifecycles, different rotation
  policies.
- **Two images, one per provider.** Doubles the pinned-image-id slot,
  complicates `agentctl update`, and rules out the mixed-provider
  assembly-line case for the marginal benefit of a slightly smaller
  per-image footprint. Rejected.
- **Use the experimental `openai-codex` Python SDK instead of shelling
  out to `codex exec --json`.** SDK is still labeled experimental,
  depends on the JSON-RPC `app-server`, and offers no capability we
  can't get more simply by parsing `exec --json` output. If/when the
  SDK stabilizes, swapping it in behind the driver interface is a
  contained refactor.
- **Keep ADR 0003's model-immutable rule and add a session-clone
  primitive for "switch model" UX.** Workable but heavier — every
  switch costs a container spawn and a fresh JSONL. The driver-level
  re-instantiate is cheap and preserves history.

## UX principles

These rules cross-cut the decision sections above and shape the
surfaces users actually touch. They apply across all phases.

- **Provider invisibility when only one is enabled.** The word
  "provider" never appears in CLI help, prompts, or the web UI while a
  user has exactly one provider configured — the existing Anthropic-
  only workflow is byte-for-byte unchanged after upgrade. The moment a
  second provider is configured, provider chips, dropdowns, and the
  `--provider` flag appear everywhere they're relevant. The cost of
  adding the second provider should be visible; the cost of having
  configured only one should be zero.
- **Adaptive default, not config.** Provider ambiguity (two providers
  enabled, nothing in the call site says which) is resolved by sticky
  last-used-provider per workspace — not a `default_provider` config
  knob. New users have one provider and never see the choice; power
  users get behavior tied to the work they were last doing. Override
  is always `--provider` or the chip in the UI; no one edits TOML for
  this.
- **Multi-surface model switch with the web header as primary.** The
  in-session model dropdown is the affordance users will reach for —
  it's where their attention already is. `/model <name>` inside the
  chat input is the keyboard-driven secondary. The CLI
  `agentctl session set-model` subcommand exists for scripting and is
  not surfaced in `--help` examples. Discoverability follows where
  users actually look.
- **One source for the model catalog.** The `models` array under
  `GET /api/providers` is read directly from
  `[pricing.tables.models]` (already version-keyed). Adding a model =
  adding a pricing row + bumping the image. No parallel catalog file
  to keep in sync, no hardcoded list in the daemon.
- **Orchestration is the destination, not provider count.** The
  headline reading of this ADR is not "agentctl now supports OpenAI" —
  it's "agentctl orchestrates agents across providers." Phasing (next
  section) reflects that: the mixed-provider assembly-line UX ships
  before the mid-session model switch, because the former is what
  earns the "orchestrator" label.

## Phasing

Five cuts, each shippable on its own. Order is chosen so each phase
delivers a coherent, user-visible step toward the agent-orchestrator
end state.

1. **Codex end-to-end with API keys.** Secrets + env injection +
   `--openai-key` flag, Codex CLI in the image, Codex driver in the
   shim, `provider` plumbed through session create, `EnabledProviders()`
   + the resolution algorithm, built-in YAMLs drop their hardcoded
   model, agent-create gating on the resolved provider set. The
   `OpenAIBaseURL` / `OpenAIAuthToken` fields land in the Secrets
   struct (so phase 5 doesn't need a migration) but are not yet
   surfaced via flags. Acceptance: the built-in `bug-investigator`
   runs on either provider depending on what the user has configured —
   no YAML edits required — and completes one full turn including a
   tool call against `https://api.openai.com` with `OPENAI_API_KEY`
   from `secrets.json`.
2. **OAuth helper for Codex.** Parametric `image/auth.Dockerfile` with
   a `PROVIDER` arg, `agentctl auth login --provider openai`, OAuth
   bind-mount path (preferring the symlink shape from §8 unless
   blocked). Acceptance: a full subscription-billed Codex session
   works after `auth login`, with no `OPENAI_API_KEY` ever set.
3. **Orchestration as the headline.** Ship at least one built-in
   assembly line that explicitly mixes providers (e.g. investigator on
   Claude, executor on Codex), plus the run-view surface that shows
   each stage's provider+model as a first-class visual rather than
   buried metadata. Docs and the `agentctl start --line` UX get a
   pass. The infra is already there (`tm.SessionRuntime` spawns a
   fresh container per stage); this phase is mostly built-in YAMLs,
   render polish, and documentation. Acceptance: a built-in mixed-
   provider line completes a bug-fix end-to-end with correct
   per-stage usage attribution, and the run view makes the
   provider/model of each stage obvious without drilling in.
4. **Mid-session model switch.** Control frame, driver re-instantiate,
   web header dropdown (primary), `/model` slash command (secondary),
   `agentctl session set-model` (scripting). Acceptance: switch
   `claude-sonnet` → `claude-opus` mid-conversation; switch `gpt-5.5`
   → `gpt-5.3-codex` mid-conversation; usage rows are correctly tagged
   across the transition; resume from idle continues to work on both
   sides of a switch.
5. **Custom endpoints / gateways.** Surface `OpenAIBaseURL` and
   `OpenAIAuthToken` via `--openai-base-url` / `--openai-auth-token`
   on `agentctl init` (mirroring the existing Anthropic flags) and
   wire `writeSecretsEnv` to inject them when set. Validation in
   `init` accommodates the gateway case (best-effort `GET /v1/models`
   against the custom base URL; tolerate gateways that don't expose
   it). Acceptance: a Codex session against an OpenAI-compatible
   gateway (Azure OpenAI, vLLM, or similar) completes one full turn
   with no traffic to `api.openai.com`.

## References

- ADR 0003 — Default model and per-session model selection (superseded
  in part by §2 of this ADR).
- ADR 0014 — Local image build and bind-mounted skills (image-size and
  build-time budget context).
- `architecture/container-and-image.md` §2.5–§2.6 (shim ↔ runtime
  contract; this is the layer the driver split lives in).
- `architecture/api.md` §5 (event vocabulary the drivers translate
  into).
- `architecture/data-model.md` §5 (`sessions.model`, `usage.model`).
- OpenAI Codex docs: `developers.openai.com/codex/auth`,
  `/codex/cli/reference`, `/codex/noninteractive`, `/codex/sdk`.
- `github.com/openai/codex` (CLI source; pin a release tag at impl
  time).
