package secrets

import (
	"errors"
	"fmt"
	"os"
	"sort"
)

// ErrNoProvidersEnabled is returned by ResolveProvider when no provider has
// credentials configured. The caller should surface a hint pointing at
// `agentctl init` / `agentctl auth login`.
var ErrNoProvidersEnabled = errors.New("no providers configured")

// EnabledProviders returns the sorted set of provider identifiers whose
// credentials currently resolve. A provider is "enabled" if any of the
// following are true (ADR 0020 §3):
//
//   - An API key is set.
//   - A custom-endpoint + bearer-token pair is set (phase 5 surface for
//     OpenAI; already shipped for Anthropic).
//   - An OAuth credentials file is present *and non-empty* at the
//     provider's CredsFile path (when fsLookup is non-nil).
//
// The OAuth-file presence check is deliberately a function parameter rather
// than baked in: secrets has no dependency on paths.Layout, and the caller
// (cli / sm / websrv) already knows the relevant credentials directory.
// Pass nil to skip the file checks entirely — useful in tests that don't
// touch disk.
func (s Secrets) EnabledProviders(fsLookup func(provider string) (path string)) []string {
	set := map[string]bool{}

	// Anthropic.
	if s.AnthropicAPIKey != "" {
		set[ProviderAnthropic] = true
	}
	if s.AnthropicBaseURL != "" && s.AnthropicAuthToken != "" {
		set[ProviderAnthropic] = true
	}
	if s.AnthropicAuthMode == AuthModeOAuth && fsLookup != nil {
		if p := fsLookup(ProviderAnthropic); p != "" {
			if info, err := os.Stat(p); err == nil && info.Size() > 0 {
				set[ProviderAnthropic] = true
			}
		}
	}

	// OpenAI.
	if s.OpenAIAPIKey != "" {
		set[ProviderOpenAI] = true
	}
	if s.OpenAIBaseURL != "" && s.OpenAIAuthToken != "" {
		set[ProviderOpenAI] = true
	}
	if s.OpenAIAuthMode == AuthModeOAuth && fsLookup != nil {
		if p := fsLookup(ProviderOpenAI); p != "" {
			if info, err := os.Stat(p); err == nil && info.Size() > 0 {
				set[ProviderOpenAI] = true
			}
		}
	}

	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// ResolveInputs is the per-call context the resolver needs. Each field is
// optional; the resolver returns the first non-empty winner per the
// precedence rules in ADR 0020 §3.
type ResolveInputs struct {
	// AgentProvider is the agent definition's `provider:` field (ttl.Agent).
	AgentProvider string
	// CLIProvider is the value of `--provider` (or the equivalent in the
	// session-create payload). Empty when the caller didn't specify one.
	CLIProvider string
	// WorkspaceLastUsed is the sticky last-used provider for this workspace
	// (read from the workspace_state table — see ADR 0020 §3, §UX
	// principles). Empty for a fresh workspace.
	WorkspaceLastUsed string

	// AgentModel is the agent definition's `model:` field.
	AgentModel string
	// CLIModel is the value of `--model` (or equivalent in the payload).
	CLIModel string
	// DefaultModels is the per-provider default model map keyed by
	// provider id. Wired from config.toml's [model] section
	// (anthropic_default / openai_default).
	DefaultModels map[string]string

	// Enabled is the set of providers whose credentials resolve. Caller
	// produces it via Secrets.EnabledProviders so the resolver can
	// validate without re-loading secrets.
	Enabled []string
}

// Resolved is what the resolver returns. Provider is always one of the
// enabled providers; Model is the chosen model id for that provider.
type Resolved struct {
	Provider string
	Model    string
}

// ResolveProvider implements the single resolution algorithm from ADR 0020
// §3. Every entry point that picks (provider, model) — CLI `agentctl
// start`, websrv `POST /api/sessions`, tm.SessionRuntime stage spawn —
// calls this. Do not inline the fallback chain anywhere else.
func ResolveProvider(in ResolveInputs) (Resolved, error) {
	if len(in.Enabled) == 0 {
		return Resolved{}, fmt.Errorf("%w; run `agentctl init` or `agentctl auth login`", ErrNoProvidersEnabled)
	}

	provider := firstNonEmpty(in.AgentProvider, in.CLIProvider, in.WorkspaceLastUsed)
	if provider == "" {
		if len(in.Enabled) == 1 {
			provider = in.Enabled[0]
		} else {
			return Resolved{}, fmt.Errorf("multiple providers enabled (%v); pass --provider or pick one in the UI", in.Enabled)
		}
	}

	if !contains(in.Enabled, provider) {
		return Resolved{}, fmt.Errorf("provider %q not configured; run `agentctl auth login --provider %s` or `agentctl init --%s-key`", provider, provider, provider)
	}

	model := firstNonEmpty(in.AgentModel, in.CLIModel)
	if model == "" && in.DefaultModels != nil {
		model = in.DefaultModels[provider]
	}
	return Resolved{Provider: provider, Model: model}, nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

func contains(haystack []string, needle string) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}
