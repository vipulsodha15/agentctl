package sm

// Provider catalog — single source of truth for "what models can a session
// switch to mid-conversation?" Per ADR 0020 §UX principles ("one source for
// the model catalog") the catalog reads straight out of
// `[pricing.tables.models]` in config.toml; there is no parallel hardcoded
// list to keep in sync.
//
// Per ADR 0020 §1 the catalog is scoped per-provider so cross-provider
// switches (e.g. swapping a Claude session to gpt-5.5) are rejected with
// ErrModelInvalid. The session's immutable `provider` field selects which
// list HasModel validates against.

// ProviderCatalog snapshots the per-provider model lists the daemon serves
// from /v1/providers and validates PATCH /v1/sessions/<id> against. Lifetime
// matches the daemon process — refresh by rebuilding from config.
type ProviderCatalog struct {
	// ModelsByProvider lists the valid model ids for each provider. Key is
	// the provider name ("anthropic", "openai"); value is the model ids
	// that provider exposes. An empty list (or missing key) means the
	// provider has no known models, which fails every HasModel check —
	// the correct behaviour for an unconfigured provider.
	ModelsByProvider map[string][]string
}

// HasModel reports whether model is valid for provider. Empty model and
// empty provider are always invalid: an empty PATCH body is a client bug
// (not "keep current"), and a session without a provider can't have its
// model validated.
func (c ProviderCatalog) HasModel(provider, model string) bool {
	if model == "" || provider == "" {
		return false
	}
	for _, m := range c.ModelsByProvider[provider] {
		if m == model {
			return true
		}
	}
	return false
}
