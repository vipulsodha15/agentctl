package mcp

import (
	"testing"
	"time"
)

func nowUTC() time.Time { return time.Now().UTC() }

func TestRenderHandlesAllKindsAndTransports(t *testing.T) {
	in := RenderInputs{
		Entries: []Entry{
			{Name: "plain", URL: "http://a/", Transport: "http", Kind: "none"},
			{Name: "gh", URL: "https://api.example/", Transport: "sse", Kind: "github_pat"},
			{Name: "weird-kind", URL: "http://x/", Transport: "http", Kind: "oauth2"},
			{Name: "weird-tx", URL: "x://q/", Transport: "smb", Kind: "none"},
		},
		Secrets: Secrets{GitHubPAT: "ghp_secret"},
	}
	r := Render(in)
	if len(r.Configs) != 2 {
		t.Fatalf("expected 2 configs, got %d: %+v", len(r.Configs), r.Configs)
	}
	if len(r.Skipped) != 2 {
		t.Fatalf("expected 2 skipped, got %d: %+v", len(r.Skipped), r.Skipped)
	}
	skipReasons := map[string]string{}
	for _, s := range r.Skipped {
		skipReasons[s.Name] = s.Reason
	}
	if skipReasons["weird-kind"] != "unsupported kind" {
		t.Errorf("weird-kind reason: %v", skipReasons)
	}
	if skipReasons["weird-tx"] != "unsupported transport" {
		t.Errorf("weird-tx reason: %v", skipReasons)
	}
	var gh *McpConfig
	for i := range r.Configs {
		if r.Configs[i].Name == "gh" {
			gh = &r.Configs[i]
		}
	}
	if gh == nil {
		t.Fatal("missing gh config")
	}
	if gh.Headers["Authorization"] != "Bearer ghp_secret" {
		t.Errorf("gh headers: %+v", gh.Headers)
	}
	for _, c := range r.Configs {
		if c.Name == "plain" && len(c.Headers) != 0 {
			t.Errorf("plain should have no headers: %+v", c.Headers)
		}
	}
}

func TestRenderGithubPatWithoutSecretEmitsNoHeader(t *testing.T) {
	r := Render(RenderInputs{
		Entries: []Entry{{Name: "gh", URL: "https://x/", Transport: "http", Kind: "github_pat"}},
	})
	if len(r.Configs) != 1 {
		t.Fatalf("expected 1 config, got %d", len(r.Configs))
	}
	if len(r.Configs[0].Headers) != 0 {
		t.Errorf("expected no headers when PAT missing: %+v", r.Configs[0].Headers)
	}
}

func TestResolveDefaults(t *testing.T) {
	entries := []Entry{
		{Name: "a", DefaultEnabled: true},
		{Name: "b", DefaultEnabled: false},
		{Name: "c", DefaultEnabled: true},
	}
	out := Resolve(entries, nil, nil)
	if len(out) != 2 || out[0].Name != "a" || out[1].Name != "c" {
		t.Errorf("default resolve: %+v", out)
	}
	out = Resolve(entries, []string{"b", "c"}, nil)
	if len(out) != 2 || out[0].Name != "b" || out[1].Name != "c" {
		t.Errorf("explicit resolve: %+v", out)
	}
	out = Resolve(entries, nil, []string{"a"})
	if len(out) != 1 || out[0].Name != "c" {
		t.Errorf("exclude resolve: %+v", out)
	}
	out = Resolve(entries, []string{"a", "missing"}, nil)
	if len(out) != 1 || out[0].Name != "a" {
		t.Errorf("missing-name should drop: %+v", out)
	}
}
