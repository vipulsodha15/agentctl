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
	if err := writeSecretsEnv(envPath, secretsPath, secretsEnvInputs{SessionID: "sess_01"}); err != nil {
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
}
