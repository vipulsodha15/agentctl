package agentd

import (
	"sort"

	"github.com/agentctl/agentctl/internal/config"
	"github.com/agentctl/agentctl/internal/paths"
	"github.com/agentctl/agentctl/internal/secrets"
	"github.com/agentctl/agentctl/internal/store"
	"github.com/agentctl/agentctl/internal/websrv"
)

// newProviderResolver wires the single resolver from ADR 0020 §3 over the
// daemon's runtime state. Every call re-loads secrets and config from disk
// so a rotated key / edited config.toml takes effect without restarting
// agentd — secrets and config are the slow path, and Create is the only
// caller (per-session, not per-message).
//
// Returns a closure with the func signature both socksrv and websrv accept.
func newProviderResolver(layout paths.Layout, st *store.Store) func(cliProvider, cliModel string) (string, string, error) {
	credsPathFor := func(provider string) string {
		switch provider {
		case secrets.ProviderAnthropic:
			return layout.ClaudeCredsFile
		case secrets.ProviderOpenAI:
			return layout.CodexCredsFile
		}
		return ""
	}
	return func(cliProvider, cliModel string) (string, string, error) {
		sec, _ := secrets.Load(layout.SecretsFile)
		cfg, _ := config.Load(layout.ConfigFile)
		enabled := sec.EnabledProviders(credsPathFor)

		var lastUsed string
		if st != nil {
			lastUsed, _ = st.WorkspaceState("last_used_provider")
		}
		defaults := map[string]string{
			secrets.ProviderAnthropic: cfg.Model.AnthropicDefault,
			secrets.ProviderOpenAI:    cfg.Model.OpenAIDefault,
		}
		res, err := secrets.ResolveProvider(secrets.ResolveInputs{
			CLIProvider:       cliProvider,
			CLIModel:          cliModel,
			WorkspaceLastUsed: lastUsed,
			DefaultModels:     defaults,
			Enabled:           enabled,
		})
		if err != nil {
			return "", "", err
		}
		return res.Provider, res.Model, nil
	}
}

// providerCatalog implements websrv.ProviderCatalog. The models list is
// filtered by name prefix (`claude-*` for anthropic, `gpt-*` for openai)
// because the canonical model registry is `[pricing.tables.models]` in
// config.toml — a single source of truth, ADR 0020 §UX principles.
//
// Filtering by prefix is intentionally simple-minded: it matches the
// historical CLI naming conventions and breaks loudly if a future model id
// doesn't fit (the model wouldn't show up in the dropdown), which is the
// right signal to revisit the registry shape rather than encode a parallel
// catalog.
type providerCatalog struct {
	layout paths.Layout
	st     *store.Store
}

func newProviderCatalog(layout paths.Layout, st *store.Store) *providerCatalog {
	return &providerCatalog{layout: layout, st: st}
}

func (p *providerCatalog) Catalog() websrv.ProvidersResponse {
	sec, _ := secrets.Load(p.layout.SecretsFile)
	cfg, _ := config.Load(p.layout.ConfigFile)
	credsPathFor := func(provider string) string {
		switch provider {
		case secrets.ProviderAnthropic:
			return p.layout.ClaudeCredsFile
		case secrets.ProviderOpenAI:
			return p.layout.CodexCredsFile
		}
		return ""
	}
	enabled := sec.EnabledProviders(credsPathFor)
	enabledSet := map[string]bool{}
	for _, e := range enabled {
		enabledSet[e] = true
	}

	out := websrv.ProvidersResponse{
		secrets.ProviderAnthropic: {
			Enabled:      enabledSet[secrets.ProviderAnthropic],
			DefaultModel: cfg.Model.AnthropicDefault,
			Models:       filterModels(cfg.Pricing.Tables.Models, "claude-"),
		},
		secrets.ProviderOpenAI: {
			Enabled:      enabledSet[secrets.ProviderOpenAI],
			DefaultModel: cfg.Model.OpenAIDefault,
			Models:       filterModels(cfg.Pricing.Tables.Models, "gpt-"),
		},
	}
	return out
}

func filterModels(models map[string]config.PricingEntry, prefix string) []string {
	out := make([]string, 0, len(models))
	for name := range models {
		if len(name) >= len(prefix) && name[:len(prefix)] == prefix {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
