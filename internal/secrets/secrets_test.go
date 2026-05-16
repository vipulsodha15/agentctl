package secrets

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.json")
	s := Secrets{
		AnthropicAPIKey: "sk-ant-api03-XYZ",
		GitHubPAT:       "ghp_AAAAAAAAAAAAAAAAAAAAA",
		GitHubPATKind:   "classic",
	}
	if err := Save(path, s); err != nil {
		t.Fatalf("save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != FilePerm {
		t.Errorf("perm = %o, want %o", info.Mode().Perm(), FilePerm)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.V != 1 {
		t.Errorf("v = %d, want 1", got.V)
	}
	if got.AnthropicAPIKey != s.AnthropicAPIKey {
		t.Errorf("anthropic key mismatch")
	}
}

func TestSaveAndLoadCustomEndpoint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.json")
	s := Secrets{
		AnthropicBaseURL:   "https://gateway.example.com",
		AnthropicAuthToken: "bearer-xyz",
		GitHubPAT:          "ghp_AAAAAAAAAAAAAAAAAAAAA",
		GitHubPATKind:      "classic",
	}
	if err := Save(path, s); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.AnthropicAPIKey != "" {
		t.Errorf("expected empty api key in custom-endpoint mode, got %q", got.AnthropicAPIKey)
	}
	if got.AnthropicBaseURL != s.AnthropicBaseURL || got.AnthropicAuthToken != s.AnthropicAuthToken {
		t.Errorf("base url/auth token round-trip mismatch")
	}
}

// TestEnabledProviders is the truth table from CODEX_PROVIDER_PLAN §1.9 and
// ADR 0020 §3. A provider counts as enabled iff *any* of its credential
// shapes resolve — API key, custom-endpoint pair, or a non-empty OAuth
// creds file at the path the caller supplied.
func TestEnabledProviders(t *testing.T) {
	// Build a working OAuth creds file we can swap in/out per case.
	dir := t.TempDir()
	goodOAuth := filepath.Join(dir, "good.json")
	if err := os.WriteFile(goodOAuth, []byte(`{"access_token":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	emptyOAuth := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(emptyOAuth, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		sec    Secrets
		lookup func(string) string
		want   []string
	}{
		{name: "zero providers", sec: Secrets{}, want: []string{}},
		{
			name: "anthropic api key only",
			sec:  Secrets{AnthropicAPIKey: "sk-ant-x"},
			want: []string{ProviderAnthropic},
		},
		{
			name: "openai api key only",
			sec:  Secrets{OpenAIAPIKey: "sk-x"},
			want: []string{ProviderOpenAI},
		},
		{
			name: "both api keys",
			sec:  Secrets{AnthropicAPIKey: "a", OpenAIAPIKey: "b"},
			want: []string{ProviderAnthropic, ProviderOpenAI},
		},
		{
			name: "anthropic custom endpoint",
			sec:  Secrets{AnthropicBaseURL: "https://gw.example/", AnthropicAuthToken: "tok"},
			want: []string{ProviderAnthropic},
		},
		{
			name: "openai custom endpoint",
			sec:  Secrets{OpenAIBaseURL: "https://gw.example/", OpenAIAuthToken: "tok"},
			want: []string{ProviderOpenAI},
		},
		{
			name: "anthropic oauth, file present",
			sec:  Secrets{AnthropicAuthMode: AuthModeOAuth},
			lookup: func(provider string) string {
				if provider == ProviderAnthropic {
					return goodOAuth
				}
				return ""
			},
			want: []string{ProviderAnthropic},
		},
		{
			name: "anthropic oauth, file empty",
			sec:  Secrets{AnthropicAuthMode: AuthModeOAuth},
			lookup: func(provider string) string {
				if provider == ProviderAnthropic {
					return emptyOAuth
				}
				return ""
			},
			want: []string{},
		},
		{
			name: "openai oauth only, file present",
			sec:  Secrets{OpenAIAuthMode: AuthModeOAuth},
			lookup: func(provider string) string {
				if provider == ProviderOpenAI {
					return goodOAuth
				}
				return ""
			},
			want: []string{ProviderOpenAI},
		},
		{
			name: "anthropic api key plus openai oauth file present",
			sec:  Secrets{AnthropicAPIKey: "a", OpenAIAuthMode: AuthModeOAuth},
			lookup: func(provider string) string {
				if provider == ProviderOpenAI {
					return goodOAuth
				}
				return ""
			},
			want: []string{ProviderAnthropic, ProviderOpenAI},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.sec.EnabledProviders(tc.lookup)
			if !equalSorted(got, tc.want) {
				t.Errorf("EnabledProviders=%v, want %v", got, tc.want)
			}
		})
	}
}

// TestResolveProvider covers every branch of ADR 0020 §3.
func TestResolveProvider(t *testing.T) {
	defaults := map[string]string{
		ProviderAnthropic: "claude-sonnet-4-6",
		ProviderOpenAI:    "gpt-5.5",
	}
	t.Run("no providers enabled", func(t *testing.T) {
		_, err := ResolveProvider(ResolveInputs{Enabled: nil})
		if err == nil {
			t.Fatal("expected error when nothing is enabled")
		}
	})
	t.Run("sole provider, no agent/cli", func(t *testing.T) {
		r, err := ResolveProvider(ResolveInputs{
			Enabled: []string{ProviderAnthropic}, DefaultModels: defaults,
		})
		if err != nil {
			t.Fatal(err)
		}
		if r.Provider != ProviderAnthropic || r.Model != "claude-sonnet-4-6" {
			t.Fatalf("got %+v", r)
		}
	})
	t.Run("agent provider wins over cli and workspace", func(t *testing.T) {
		r, err := ResolveProvider(ResolveInputs{
			AgentProvider:     ProviderOpenAI,
			CLIProvider:       ProviderAnthropic,
			WorkspaceLastUsed: ProviderAnthropic,
			Enabled:           []string{ProviderAnthropic, ProviderOpenAI},
			DefaultModels:     defaults,
		})
		if err != nil {
			t.Fatal(err)
		}
		if r.Provider != ProviderOpenAI {
			t.Fatalf("got %+v", r)
		}
	})
	t.Run("cli provider wins over workspace", func(t *testing.T) {
		r, _ := ResolveProvider(ResolveInputs{
			CLIProvider:       ProviderOpenAI,
			WorkspaceLastUsed: ProviderAnthropic,
			Enabled:           []string{ProviderAnthropic, ProviderOpenAI},
			DefaultModels:     defaults,
		})
		if r.Provider != ProviderOpenAI || r.Model != "gpt-5.5" {
			t.Fatalf("got %+v", r)
		}
	})
	t.Run("workspace sticky used when nothing else set", func(t *testing.T) {
		r, _ := ResolveProvider(ResolveInputs{
			WorkspaceLastUsed: ProviderOpenAI,
			Enabled:           []string{ProviderAnthropic, ProviderOpenAI},
			DefaultModels:     defaults,
		})
		if r.Provider != ProviderOpenAI {
			t.Fatalf("got %+v", r)
		}
	})
	t.Run("ambiguous with multiple enabled, no hint", func(t *testing.T) {
		_, err := ResolveProvider(ResolveInputs{
			Enabled: []string{ProviderAnthropic, ProviderOpenAI},
		})
		if err == nil {
			t.Fatal("expected error for multi-enabled with no tiebreak")
		}
	})
	t.Run("picked provider must be enabled", func(t *testing.T) {
		_, err := ResolveProvider(ResolveInputs{
			CLIProvider: ProviderOpenAI,
			Enabled:     []string{ProviderAnthropic},
		})
		if err == nil {
			t.Fatal("expected error when picking a disabled provider")
		}
	})
	t.Run("agent model wins over cli and default", func(t *testing.T) {
		r, _ := ResolveProvider(ResolveInputs{
			AgentModel:    "claude-opus-4-7",
			CLIModel:      "claude-haiku-4-5",
			Enabled:       []string{ProviderAnthropic},
			DefaultModels: defaults,
		})
		if r.Model != "claude-opus-4-7" {
			t.Fatalf("got %+v", r)
		}
	})
	t.Run("default model used when neither agent nor cli sets one", func(t *testing.T) {
		r, _ := ResolveProvider(ResolveInputs{
			Enabled:       []string{ProviderOpenAI},
			DefaultModels: defaults,
		})
		if r.Model != "gpt-5.5" {
			t.Fatalf("got %+v", r)
		}
	})
}

func TestResolvedOpenAIAuthMode(t *testing.T) {
	if (Secrets{}).ResolvedOpenAIAuthMode() != AuthModeAPIKey {
		t.Errorf("default should be api_key")
	}
	if (Secrets{OpenAIAuthMode: AuthModeOAuth}).ResolvedOpenAIAuthMode() != AuthModeOAuth {
		t.Errorf("oauth should round-trip")
	}
}

func equalSorted(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestGenerateAndReadWebToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "web_token")
	tok, err := GenerateWebToken()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(tok) < 20 {
		t.Errorf("token too short: %q", tok)
	}
	if err := WriteWebToken(path, tok); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := ReadWebToken(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != tok {
		t.Errorf("read != written")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != FilePerm {
		t.Errorf("perm = %o, want %o", info.Mode().Perm(), FilePerm)
	}
}
