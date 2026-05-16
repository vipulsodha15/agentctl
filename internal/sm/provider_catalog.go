package sm

// Provider catalog — single source of truth for "what models can a session
// switch to mid-conversation?" Per ADR 0020 §UX principles ("one source for
// the model catalog") the catalog reads straight out of
// `[pricing.tables.models]` in config.toml; there is no parallel hardcoded
// list to keep in sync.
//
// Phase 4 (this code) ships the catalog as a flat single-provider list (the
// pre-Codex world). The phasing in ADR 0020 puts the Codex split in Phase 1,
// which would land a provider_resolver here that scopes the catalog
// per-provider and lets ValidateModelForProvider reject cross-provider
// switches. Until that phase merges, every model in the pricing table is
// treated as valid for every session — the validation surface (ValidateModel)
// already exists, so the upgrade is a tightening, not a redesign.
//
// TODO(adr-0020-phase-1): scope the catalog per-provider once
// internal/agentd/provider_resolver.go lands. Until then, ValidateModel
// rejects only models that aren't in the pricing table at all (the same
// validation `agentctl start --model` does at create time).

// ProviderCatalog snapshots the per-provider model lists the daemon serves
// from /v1/providers and validates PATCH /v1/sessions/<id> against. Lifetime
// matches the daemon process — refresh by rebuilding from config.
type ProviderCatalog struct {
	// Models is the flat union of every known model id. When the Codex phase
	// lands this becomes a per-provider map; the surface UpdateModel uses
	// (validate then dispatch) doesn't change.
	Models []string
}

// HasModel returns true if model is in the catalog. Empty model is always
// invalid: an empty PATCH body is a client bug, not "keep current."
func (c ProviderCatalog) HasModel(model string) bool {
	if model == "" {
		return false
	}
	for _, m := range c.Models {
		if m == model {
			return true
		}
	}
	return false
}
