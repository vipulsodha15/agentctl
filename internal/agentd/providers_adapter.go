package agentd

import (
	"context"
	"sort"

	"github.com/agentctl/agentctl/internal/config"
	"github.com/agentctl/agentctl/internal/sm"
	"github.com/agentctl/agentctl/internal/websrv"
)

// providersAdapter exposes the daemon's per-provider model catalog to
// websrv (GET /v1/providers) and to the session-manager's UpdateModel
// validation (ADR 0020 §9 / §UX principles — "one source for the model
// catalog"). Both surfaces share a single read of config.toml so adding a
// model means adding a pricing row, not editing a parallel catalog file.
//
// Phase 4 ships the adapter with one provider entry — `anthropic` — built
// from `[pricing.tables.models]`. ADR 0020's Phase 1 will introduce a
// per-provider split; the cleanest seam is the `provider` -> `models`
// mapping below, which is what the SPA's dropdown filters on. Until then,
// every model in the pricing table is reported under the single anthropic
// provider, matching the pre-Codex world.
type providersAdapter struct {
	configPath string
}

func newProvidersAdapter(configPath string) *providersAdapter {
	return &providersAdapter{configPath: configPath}
}

// List returns the per-provider catalog. Re-reads config.toml on each
// call so editors of [pricing.tables.models] see changes without
// restarting the daemon; the file is small and the call rate is bounded
// (one dropdown render per session-detail open).
func (p *providersAdapter) List(_ context.Context) (map[string]websrv.ProviderEntry, error) {
	cfg, err := config.Load(p.configPath)
	if err != nil {
		// Treat a missing/unreadable config as "no providers" rather
		// than 500ing — the SPA renders the model display as plain text
		// in that case, which is the historical behavior.
		return map[string]websrv.ProviderEntry{}, nil
	}
	models := modelNames(cfg.Pricing.Tables.Models)
	return map[string]websrv.ProviderEntry{
		"anthropic": {
			Enabled:      true,
			DefaultModel: cfg.Model.Default,
			Models:       models,
		},
	}, nil
}

// Catalog returns the flat catalog passed into sm.Manager for
// UpdateModel validation. It's the same data as List but in the sm
// type so the package boundary is clean.
func (p *providersAdapter) Catalog() sm.ProviderCatalog {
	cfg, err := config.Load(p.configPath)
	if err != nil {
		return sm.ProviderCatalog{}
	}
	return sm.ProviderCatalog{Models: modelNames(cfg.Pricing.Tables.Models)}
}

func modelNames(m map[string]config.PricingEntry) []string {
	out := make([]string, 0, len(m))
	for name := range m {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
