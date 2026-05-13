package tm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// resolveAuth exposes resolve so the test can drive it without exporting
// the type.
func (a *AuthSource) testResolve(t *testing.T, defaultURL string) resolvedAuth {
	t.Helper()
	r, err := a.resolve(defaultURL)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return r
}

func TestAuthSource_PrefersBearerOverAPIKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.json")
	if err := os.WriteFile(path, []byte(`{
        "v": 1,
        "anthropic_api_key": "sk-ant-key-123",
        "anthropic_auth_token": "tok-456",
        "anthropic_base_url": "https://proxy.example.com"
    }`), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &AuthSource{SecretsPath: path}
	got := a.testResolve(t, "https://api.anthropic.com")
	if got.kind != "bearer" {
		t.Errorf("expected bearer, got %q", got.kind)
	}
	if got.value != "tok-456" {
		t.Errorf("wrong token: %q", got.value)
	}
	if got.baseURL != "https://proxy.example.com" {
		t.Errorf("bearer must use AnthropicBaseURL, got %q", got.baseURL)
	}
}

func TestAuthSource_APIKeyWhenNoToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.json")
	if err := os.WriteFile(path, []byte(`{"v":1,"anthropic_api_key":"sk-ant-key-only"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &AuthSource{SecretsPath: path}
	got := a.testResolve(t, "https://api.anthropic.com")
	if got.kind != "x-api-key" || got.value != "sk-ant-key-only" {
		t.Errorf("unexpected resolution: %+v", got)
	}
}

func TestAuthSource_OAuthFromCredentialsFile(t *testing.T) {
	dir := t.TempDir()
	secretsPath := filepath.Join(dir, "secrets.json")
	if err := os.WriteFile(secretsPath, []byte(`{"v":1,"anthropic_auth_mode":"oauth"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	credsPath := filepath.Join(dir, ".credentials.json")
	if err := os.WriteFile(credsPath, []byte(`{
        "claudeAiOauth": {
            "accessToken": "sk-ant-oat-xyz",
            "refreshToken": "rt",
            "expiresAt": 9999999999
        }
    }`), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &AuthSource{SecretsPath: secretsPath, CredsFile: credsPath}
	got := a.testResolve(t, "https://api.anthropic.com")
	if got.kind != "bearer" || got.value != "sk-ant-oat-xyz" {
		t.Errorf("oauth resolution failed: %+v", got)
	}
}

func TestAuthSource_FallsBackToEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "env-key")
	t.Setenv("ANTHROPIC_BASE_URL", "https://env.example.com")
	a := &AuthSource{} // empty — no disk paths
	got := a.testResolve(t, "https://default.example.com")
	if got.kind != "x-api-key" || got.value != "env-key" {
		t.Errorf("env fallback failed: %+v", got)
	}
	if got.baseURL != "https://env.example.com" {
		t.Errorf("env ANTHROPIC_BASE_URL not honored: %q", got.baseURL)
	}
}

func TestAuthSource_ReturnsActionableErrorWhenUnconfigured(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_BASE_URL", "")
	a := &AuthSource{}
	_, err := a.resolve("https://default.example.com")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "agentctl init") {
		t.Errorf("error message must mention `agentctl init`: %v", err)
	}
	if !isAuthConfigError(err) {
		t.Errorf("isAuthConfigError should classify this as an auth-config error: %v", err)
	}
}
