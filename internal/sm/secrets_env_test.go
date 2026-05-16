package sm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentctl/agentctl/internal/secrets"
)

func TestWriteSecretsEnvCustomEndpoint(t *testing.T) {
	dir := t.TempDir()
	secretsPath := filepath.Join(dir, "secrets.json")
	if err := secrets.Save(secretsPath, secrets.Secrets{
		V:                  1,
		AnthropicBaseURL:   "https://gateway.example.com",
		AnthropicAuthToken: "bearer-xyz",
		GitHubPAT:          "ghp_test",
		GitHubPATKind:      "classic",
	}); err != nil {
		t.Fatal(err)
	}
	envPath := filepath.Join(dir, "secrets.env")
	if err := writeSecretsEnv(envPath, secretsPath, secretsEnvInputs{
		SessionID:    "sess_01",
		SessionName:  "demo",
		Model:        "claude-sonnet-4-6",
		SessionToken: "tok",
		Provider:     secrets.ProviderAnthropic,
	}); err != nil {
		t.Fatalf("writeSecretsEnv: %v", err)
	}
	body, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	if !strings.Contains(got, "ANTHROPIC_AUTH_TOKEN=bearer-xyz\n") {
		t.Errorf("expected ANTHROPIC_AUTH_TOKEN in env file; got:\n%s", got)
	}
	if !strings.Contains(got, "ANTHROPIC_BASE_URL=https://gateway.example.com\n") {
		t.Errorf("expected ANTHROPIC_BASE_URL in env file; got:\n%s", got)
	}
	if strings.Contains(got, "ANTHROPIC_API_KEY=") {
		t.Errorf("ANTHROPIC_API_KEY must not be emitted in custom-endpoint mode; got:\n%s", got)
	}
}

func TestWriteSecretsEnvAPIKey(t *testing.T) {
	dir := t.TempDir()
	secretsPath := filepath.Join(dir, "secrets.json")
	if err := secrets.Save(secretsPath, secrets.Secrets{
		V:               1,
		AnthropicAPIKey: "sk-test",
		GitHubPAT:       "ghp_test",
		GitHubPATKind:   "classic",
	}); err != nil {
		t.Fatal(err)
	}
	envPath := filepath.Join(dir, "secrets.env")
	if err := writeSecretsEnv(envPath, secretsPath, secretsEnvInputs{
		SessionID: "sess_01",
		Provider:  secrets.ProviderAnthropic,
	}); err != nil {
		t.Fatalf("writeSecretsEnv: %v", err)
	}
	body, _ := os.ReadFile(envPath)
	got := string(body)
	if !strings.Contains(got, "ANTHROPIC_API_KEY=sk-test\n") {
		t.Errorf("expected ANTHROPIC_API_KEY in env file; got:\n%s", got)
	}
	if strings.Contains(got, "ANTHROPIC_AUTH_TOKEN=") || strings.Contains(got, "ANTHROPIC_BASE_URL=") {
		t.Errorf("custom-endpoint vars must not leak in api-key mode; got:\n%s", got)
	}
	if !strings.Contains(got, "AGENTCTL_PROVIDER=anthropic\n") {
		t.Errorf("AGENTCTL_PROVIDER not threaded through; got:\n%s", got)
	}
}

// TestWriteSecretsEnvOpenAIAPIKey covers the OpenAI api-key branch added
// in phase 1 (ADR 0020 §5, CODEX_PROVIDER_PLAN §1.9). Both vendors'
// credentials live in the same secrets.json; the env file must carry only
// the active provider's vars so a Codex container doesn't see
// ANTHROPIC_API_KEY (which would bill the wrong vendor through any
// future shared MCP that observes both).
func TestWriteSecretsEnvOpenAIAPIKey(t *testing.T) {
	dir := t.TempDir()
	secretsPath := filepath.Join(dir, "secrets.json")
	if err := secrets.Save(secretsPath, secrets.Secrets{
		V:               1,
		AnthropicAPIKey: "sk-ant-x",
		OpenAIAPIKey:    "sk-openai-y",
	}); err != nil {
		t.Fatal(err)
	}
	envPath := filepath.Join(dir, "secrets.env")
	if err := writeSecretsEnv(envPath, secretsPath, secretsEnvInputs{
		SessionID: "sess_oa",
		Provider:  secrets.ProviderOpenAI,
	}); err != nil {
		t.Fatalf("writeSecretsEnv: %v", err)
	}
	body, _ := os.ReadFile(envPath)
	got := string(body)
	if !strings.Contains(got, "OPENAI_API_KEY=sk-openai-y\n") {
		t.Errorf("expected OPENAI_API_KEY; got:\n%s", got)
	}
	if strings.Contains(got, "ANTHROPIC_API_KEY=") {
		t.Errorf("anthropic key must not leak into openai container; got:\n%s", got)
	}
	if !strings.Contains(got, "AGENTCTL_PROVIDER=openai\n") {
		t.Errorf("AGENTCTL_PROVIDER not threaded through; got:\n%s", got)
	}
}

// TestWriteSecretsEnvOpenAIOAuthInjectsNothing locks in the OAuth-mode
// rule: when the user has run `agentctl auth login --provider openai`
// (phase 2), no OPENAI_API_KEY is exported into the container — the
// bind-mounted ~/.codex/auth.json carries the auth. Mirror of the
// Anthropic OAuth contract.
func TestWriteSecretsEnvOpenAIOAuthInjectsNothing(t *testing.T) {
	dir := t.TempDir()
	secretsPath := filepath.Join(dir, "secrets.json")
	if err := secrets.Save(secretsPath, secrets.Secrets{
		V:              1,
		OpenAIAPIKey:   "sk-stale-should-not-leak",
		OpenAIAuthMode: secrets.AuthModeOAuth,
	}); err != nil {
		t.Fatal(err)
	}
	envPath := filepath.Join(dir, "secrets.env")
	if err := writeSecretsEnv(envPath, secretsPath, secretsEnvInputs{
		SessionID: "sess_oa_oauth",
		Provider:  secrets.ProviderOpenAI,
	}); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(envPath)
	got := string(body)
	if strings.Contains(got, "OPENAI_API_KEY=") {
		t.Errorf("oauth mode must not export OPENAI_API_KEY; got:\n%s", got)
	}
}
