package tm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
	if got.oauth {
		t.Errorf("custom-endpoint bearer must NOT be flagged oauth=true")
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
	if !got.oauth {
		t.Errorf("OAuth credentials file must set oauth=true (drives anthropic-beta + system prefix); got %+v", got)
	}
}

// TestCallAnthropic_OAuthHeadersAndSystemPrefix is a regression test for the
// 429 the task-chat path returned when authenticating via `agentctl auth
// login`. Anthropic gates Claude OAuth subscription tokens on the
// `anthropic-beta: oauth-2025-04-20` header AND a system prompt that begins
// with the Claude Code identity string; without either, the API returned
// 429 rate_limit_error with a generic "Error" message. Session chat avoided
// this because it shells out to the bundled `claude` CLI, which sets those
// itself.
func TestCallAnthropic_OAuthHeadersAndSystemPrefix(t *testing.T) {
	var gotAuth, gotBeta, gotAPIKey, gotVersion string
	var gotSystemBlocks []systemBlock
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotBeta = r.Header.Get("anthropic-beta")
		gotAPIKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		raw, _ := io.ReadAll(r.Body)
		var payload struct {
			System []systemBlock `json:"system"`
		}
		_ = json.Unmarshal(raw, &payload)
		gotSystemBlocks = payload.System
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"ok"}]}`)
	}))
	defer srv.Close()

	dir := t.TempDir()
	secretsPath := filepath.Join(dir, "secrets.json")
	if err := os.WriteFile(secretsPath, []byte(`{"v":1,"anthropic_auth_mode":"oauth"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	credsPath := filepath.Join(dir, ".credentials.json")
	if err := os.WriteFile(credsPath, []byte(`{"claudeAiOauth":{"accessToken":"sk-ant-oat-xyz"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	r := NewSimRuntimeWithOptions(SimRuntimeOptions{
		Auth: &AuthSource{SecretsPath: secretsPath, CredsFile: credsPath},
	})
	// resolve() returns baseURL=defaultURL for the OAuth path; point that at
	// the test server.
	r.defaultURL = srv.URL

	got, err := r.callAnthropic(context.Background(), "agent system prompt", []apiMessage{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("callAnthropic: %v", err)
	}
	if got != "ok" {
		t.Errorf("unexpected reply: %q", got)
	}
	if gotAuth != "Bearer sk-ant-oat-xyz" {
		t.Errorf("Authorization header wrong: %q", gotAuth)
	}
	if gotAPIKey != "" {
		t.Errorf("x-api-key must not be set when using bearer: %q", gotAPIKey)
	}
	if gotVersion != "2023-06-01" {
		t.Errorf("anthropic-version wrong: %q", gotVersion)
	}
	if gotBeta != "oauth-2025-04-20" {
		t.Errorf("anthropic-beta missing or wrong: %q (this is the headline 429 fix)", gotBeta)
	}
	// OAuth requires system as an array with the identity at index 0 and
	// the caller's framing as a separate block — matching what the bundled
	// `claude` CLI sends.
	if len(gotSystemBlocks) != 2 {
		t.Fatalf("expected 2 system blocks (identity + agent prompt); got %d: %+v", len(gotSystemBlocks), gotSystemBlocks)
	}
	if gotSystemBlocks[0].Type != "text" || gotSystemBlocks[0].Text != claudeCodeSystemPrefix {
		t.Errorf("system[0] must be the Claude Code identity block; got %+v", gotSystemBlocks[0])
	}
	if gotSystemBlocks[1].Type != "text" || gotSystemBlocks[1].Text != "agent system prompt" {
		t.Errorf("system[1] must carry the caller's prompt verbatim; got %+v", gotSystemBlocks[1])
	}
}

// TestCallAnthropic_CustomBearerSkipsOAuthHeader guards against accidentally
// sending the anthropic-beta header to a custom LLM gateway that authenticates
// with a Bearer token but is NOT Anthropic OAuth — that gateway has no reason
// to receive the OAuth beta header, and adding the Claude Code system prefix
// could disturb the gateway's own prompt accounting.
func TestCallAnthropic_CustomBearerSkipsOAuthHeader(t *testing.T) {
	var gotBeta, gotSystem string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBeta = r.Header.Get("anthropic-beta")
		raw, _ := io.ReadAll(r.Body)
		// Non-OAuth requests keep the plain-string system shape; a content-
		// block array would change wire compat with custom LLM gateways.
		var payload struct {
			System string `json:"system"`
		}
		_ = json.Unmarshal(raw, &payload)
		gotSystem = payload.System
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"ok"}]}`)
	}))
	defer srv.Close()

	dir := t.TempDir()
	secretsPath := filepath.Join(dir, "secrets.json")
	body := `{"v":1,"anthropic_auth_token":"proxy-tok","anthropic_base_url":"` + srv.URL + `"}`
	if err := os.WriteFile(secretsPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	r := NewSimRuntimeWithOptions(SimRuntimeOptions{
		Auth: &AuthSource{SecretsPath: secretsPath},
	})

	if _, err := r.callAnthropic(context.Background(), "agent system prompt", []apiMessage{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatalf("callAnthropic: %v", err)
	}
	if gotBeta != "" {
		t.Errorf("anthropic-beta must NOT be set for a custom-endpoint bearer; got %q", gotBeta)
	}
	if strings.HasPrefix(gotSystem, claudeCodeSystemPrefix) {
		t.Errorf("Claude Code identity prefix must NOT be prepended for a custom-endpoint bearer; got %q", gotSystem)
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
