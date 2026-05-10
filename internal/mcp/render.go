package mcp

import (
	"encoding/json"
)

type McpConfig struct {
	Name       string            `json:"name"`
	URL        string            `json:"url"`
	Transport  string            `json:"transport"`
	Kind       string            `json:"kind"`
	AuthConfig json.RawMessage   `json:"auth_config,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
}

type SkippedEntry struct {
	Name      string `json:"name"`
	Transport string `json:"transport"`
	Kind      string `json:"kind"`
	Reason    string `json:"reason"`
}

type RenderResult struct {
	Configs []McpConfig
	Skipped []SkippedEntry
}

type RenderInputs struct {
	Entries  []Entry
	Secrets  Secrets
	Statuses map[string]string
}

type Secrets struct {
	GitHubPAT string
}

var (
	knownTransports = map[string]bool{"http": true, "sse": true}
	knownKinds      = map[string]bool{"none": true, "github_pat": true}
)

func Render(in RenderInputs) RenderResult {
	out := RenderResult{}
	for _, e := range in.Entries {
		if !knownTransports[e.Transport] {
			out.Skipped = append(out.Skipped, SkippedEntry{
				Name: e.Name, Transport: e.Transport, Kind: e.Kind,
				Reason: "unsupported transport",
			})
			continue
		}
		if !knownKinds[e.Kind] {
			out.Skipped = append(out.Skipped, SkippedEntry{
				Name: e.Name, Transport: e.Transport, Kind: e.Kind,
				Reason: "unsupported kind",
			})
			continue
		}
		cfg := McpConfig{
			Name:      e.Name,
			URL:       e.URL,
			Transport: e.Transport,
			Kind:      e.Kind,
		}
		if e.AuthConfigJSON != "" {
			cfg.AuthConfig = json.RawMessage(e.AuthConfigJSON)
		}
		if e.Kind == "github_pat" {
			pat := in.Secrets.GitHubPAT
			if pat != "" {
				cfg.Headers = map[string]string{"Authorization": "Bearer " + pat}
			}
		}
		out.Configs = append(out.Configs, cfg)
	}
	return out
}

// Resolve picks the enabled-set for a session given an explicit list (mcps),
// an exclusion list (excludeMCPs), and the registry. When mcps is nil/empty,
// the registry's default_enabled rows are used. Names absent from the registry
// are dropped silently — the caller decides whether to surface that.
func Resolve(entries []Entry, requested, excluded []string) []Entry {
	exSet := stringSet(excluded)
	if len(requested) == 0 {
		out := make([]Entry, 0, len(entries))
		for _, e := range entries {
			if e.DefaultEnabled && !exSet[e.Name] {
				out = append(out, e)
			}
		}
		return out
	}
	byName := make(map[string]Entry, len(entries))
	for _, e := range entries {
		byName[e.Name] = e
	}
	out := make([]Entry, 0, len(requested))
	for _, n := range requested {
		if exSet[n] {
			continue
		}
		if e, ok := byName[n]; ok {
			out = append(out, e)
		}
	}
	return out
}

func stringSet(xs []string) map[string]bool {
	if len(xs) == 0 {
		return nil
	}
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}
