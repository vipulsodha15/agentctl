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
  `provider TEXT NOT NULL DEFAULT 'anthropic'` (back-compat for existing
  rows). It is written at session create and never mutated after.
- `ttl.Agent` gains an optional `Provider string` (`provider:` in YAML).
  Custom (user-authored) agents must set it; built-in agents leave it
  unset and resolve dynamically at session-create time.
- The runtime constraint that the provider is immutable for a session's
  lifetime is enforced both at the API (`PATCH /api/sessions/<id>`
  rejects any `provider` field) and structurally — the shim's driver
  is selected once at container boot from the `agentd.greet` frame.

### 2. The *model* becomes mutable post-create.

- `sessions.model` becomes writable. ADR 0003's "frozen for the
  session's lifetime" rule is reversed; in exchange, the provider takes
  over as the set-once field, which is what 0003 was actually trying to
  protect (immutable runtime identity for the duration of a
  conversation).
- New CLI: `agentctl session set-model <session-id> <new-model>`.
- New web UI: a model dropdown in the session header, filtered to the
  models available under the session's provider.
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
         OR inferFromModel(model)             // gpt-* → openai, claude-* → anthropic
         OR config.model.default_provider     // "openai" by default

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

This makes built-in agents portable: the shipped `bug-investigator` /
`bug-planner` / `bug-executor` YAMLs drop their hardcoded
`model: claude-opus-4-7` and don't set `provider:`. They inherit per-
provider defaults, so the same built-in runs on whichever provider the
user has configured.

### 4. `config.toml` gains per-provider defaults and a tiebreak.

```toml
[model]
default_provider  = "openai"             # used when both providers are enabled
anthropic_default = "claude-sonnet-4-6"
openai_default    = "gpt-5.5"            # placeholder; verify exact id at impl time
default           = "claude-sonnet-4-6"  # legacy fallback (kept for back-compat)
```

When only one provider is enabled, `default_provider` is ignored and the
enabled one wins. When zero are enabled, session create fails with a
hint pointing at `agentctl init` / `agentctl auth login`.

### 5. Secrets, paths, and auth helper mirror the Anthropic structure.

`internal/secrets/secrets.go`:

```go
type Secrets struct {
    // existing Anthropic fields …
    OpenAIAPIKey    string
    OpenAIAuthMode  string   // "api_key" | "oauth"
    OpenAIBaseURL   string   // optional, openai-compatible gateways
    OpenAIAuthToken string   // bearer for custom endpoint
}

func (s Secrets) ResolvedOpenAIAuthMode() string
func (s Secrets) EnabledProviders() []string
```

The Anthropic and OpenAI modes are deliberately *not* unified: users
will commonly run Anthropic OAuth alongside an OpenAI API key, and the
modes belong to different credential lifecycles.

`internal/paths/paths.go` adds `CodexCredsDir =
~/.config/agentctl/codex/` and `CodexCredsFile = .../auth.json`.

A new one-shot login image `image/codex-auth.Dockerfile` (sibling of
`image/auth.Dockerfile`) builds from `node:20-slim` with
`@openai/codex` installed globally, runs `codex login --device-auth`
inside, and points `CODEX_HOME=/creds` so `auth.json` lands in the
host-bound `~/.config/agentctl/codex/`. Device-auth (print code + URL,
user enters on host browser) is the Codex equivalent of Claude's
paste-fallback flow and the right primitive for a no-callback-port
container. The current Anthropic helper at `image/auth.Dockerfile`
is left untouched.

`agentctl auth login` grows a `--provider {anthropic,openai}` flag
(default `anthropic` for back-compat). `agentctl auth status` prints
both providers' state side-by-side. `agentctl init` grows
`--openai-key` / `--openai-base-url` / `--openai-auth-token` flags
mirroring the Anthropic ones; validation hits `GET
https://api.openai.com/v1/models` with `Authorization: Bearer <key>`.

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
  --sandbox workspace-write \
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
collides with the bind-mount strategy. Cleanest fix: drop the tmpfs in
favour of letting `/home/agent` come from the session volume. This also
gives Codex sessions the same idle-resume persistence Claude already has
via the `/work/.claude` symlink.

### 9. Agent-creation UIs gate on enabled providers.

- New daemon endpoint `GET /api/providers` returns:
  ```json
  {
    "anthropic": {"enabled": true,  "default_model": "claude-sonnet-4-6", "models": [...]},
    "openai":    {"enabled": false, "default_model": "gpt-5.5",           "models": [...]}
  }
  ```
- Web "Create agent" form filters the provider dropdown to enabled
  providers; if only one is enabled it's selected and locked.
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

### Items to verify during phase 1 (not blocking the design)

- Exact `gpt-5.5` model id and OpenAI list pricing for the
  `[pricing.tables.models]` table. The OpenAI dev docs currently
  reference `gpt-5.4` / `gpt-5.3-codex`; the user has indicated
  `gpt-5.5` is the target. Easy to change in `config.toml` once
  confirmed.
- That `--device-auth` ships stably in the pinned `@openai/codex`
  version. If not, phase 3 (OAuth) falls back to "API-key only for
  OpenAI" until the upstream stabilizes.
- That `--sandbox workspace-write --ask-for-approval never` gives
  Claude-`bypassPermissions`-equivalent behaviour with no agent stalls
  on approval. If it doesn't, fall back to `--sandbox
  danger-full-access` since the container is itself the sandbox.
- Codex JSONL event-schema stability — pin a CLI version, watch for
  breaking changes the same way we pin `claude-agent-sdk`.

## Alternatives considered

- **Infer provider purely from model prefix.** Simplest diff, brittle
  for gateway/custom-base-url cases and any future model whose name
  doesn't fit the `gpt-*` / `claude-*` convention. Used as a fallback
  in the resolution algorithm but not as the primary mechanism.
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

## Phasing

Each phase is shippable on its own.

1. **Phase 1 — API-key path only.** Secrets + env injection +
   `--openai-key` flag + doctor probe. Acceptance: a session runs
   end-to-end against `https://api.openai.com` with an `OPENAI_API_KEY`
   from `secrets.json`.
2. **Phase 2 — Codex driver + image.** Install Codex CLI in the image,
   land the Codex driver in the shim, plumb `provider` through session
   creation. Acceptance: `agentctl start --provider openai --model
   gpt-5.5` runs one full turn including a tool call.
3. **Phase 3 — OAuth helper.** `codex-auth.Dockerfile`, `agentctl auth
   login --provider openai`, OAuth bind-mount path. Acceptance: a full
   subscription-billed session works after `auth login`, with no
   `OPENAI_API_KEY` ever set.
4. **Phase 4 — Mid-session model switch.** Control frame, CLI/web
   surfaces, driver re-instantiate. Acceptance: switch `claude-sonnet`
   → `claude-opus` mid-conversation; switch `gpt-5.5` → `gpt-5.3-codex`
   mid-conversation; usage rows are correctly tagged across the
   transition.
5. **Phase 5 — Dynamic agent resolution + built-ins.**
   `EnabledProviders()`, resolution algorithm, built-in YAML cleanup,
   agent-create gating. Acceptance: built-in `bug-investigator` runs
   on either provider depending on what the user has enabled, with no
   YAML edits required by the user.

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
