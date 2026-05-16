package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/agentctl/agentctl/internal/paths"
	"github.com/agentctl/agentctl/internal/secrets"
)

func newAuthTestEnv(t *testing.T) (*Env, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	home := t.TempDir()
	layout := paths.From(home)
	if err := os.MkdirAll(layout.ConfigDir, 0o700); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	env := &Env{
		Layout: layout,
		Stdout: stdout,
		Stderr: stderr,
		Stdin:  strings.NewReader(""),
	}
	return env, stdout, stderr
}

// TestResolveLoginProviderExplicitFlag locks in the per-provider config
// table the login flow uses: --provider must pick the right credentials
// dir, credentials filename, and PROVIDER env var passed to the helper
// container.
func TestResolveLoginProviderExplicitFlag(t *testing.T) {
	env, _, _ := newAuthTestEnv(t)
	cases := []struct {
		name      string
		requested string
		wantProv  string
		wantDir   string
		wantFile  string
	}{
		{"anthropic", "anthropic", secrets.ProviderAnthropic, env.Layout.ClaudeCredsDir, env.Layout.ClaudeCredsFile},
		{"openai", "openai", secrets.ProviderOpenAI, env.Layout.CodexCredsDir, env.Layout.CodexCredsFile},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := resolveLoginProvider(env, tc.requested)
			if err != nil {
				t.Fatalf("resolveLoginProvider: %v", err)
			}
			if cfg.provider != tc.wantProv {
				t.Errorf("provider: got %q want %q", cfg.provider, tc.wantProv)
			}
			if cfg.credsDir != tc.wantDir {
				t.Errorf("credsDir: got %q want %q", cfg.credsDir, tc.wantDir)
			}
			if cfg.credsFile != tc.wantFile {
				t.Errorf("credsFile: got %q want %q", cfg.credsFile, tc.wantFile)
			}
		})
	}
}

// TestResolveLoginProviderRejectsUnknown enforces the {anthropic|openai}
// vocabulary; any other value is a usage error.
func TestResolveLoginProviderRejectsUnknown(t *testing.T) {
	env, _, _ := newAuthTestEnv(t)
	if _, err := resolveLoginProvider(env, "gemini"); err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

// TestResolveLoginProviderDefaultsAnthropicForFreshInstall preserves the
// historical single-line UX: on a brand-new install with zero providers
// configured, omitting --provider still launches the Anthropic flow
// (matches the pre-phase-2 behaviour byte-for-byte). ADR 0020 §UX.
func TestResolveLoginProviderDefaultsAnthropicForFreshInstall(t *testing.T) {
	env, _, _ := newAuthTestEnv(t)
	cfg, err := resolveLoginProvider(env, "")
	if err != nil {
		t.Fatalf("resolveLoginProvider: %v", err)
	}
	if cfg.provider != secrets.ProviderAnthropic {
		t.Errorf("default provider: got %q want %q", cfg.provider, secrets.ProviderAnthropic)
	}
}

// TestResolveLoginProviderRefusesAmbiguousMultiProvider requires the user
// to be explicit once both providers are configured — picking one
// silently here would be the kind of state-dependent behaviour that
// causes "I logged in but the wrong one updated" support tickets.
func TestResolveLoginProviderRefusesAmbiguousMultiProvider(t *testing.T) {
	env, _, _ := newAuthTestEnv(t)
	if err := secrets.Save(env.Layout.SecretsFile, secrets.Secrets{
		AnthropicAPIKey: "sk-ant",
		OpenAIAPIKey:    "sk-openai",
	}); err != nil {
		t.Fatalf("save secrets: %v", err)
	}
	if _, err := resolveLoginProvider(env, ""); err == nil {
		t.Fatal("expected error when both providers configured and --provider omitted")
	}
}

// TestRunAuthLoginOpenAIHappyPath drives `agentctl auth login --provider
// openai` end-to-end with a stubbed docker runner: we assert the docker
// invocation includes the PROVIDER=openai env, the codex creds dir as
// the bind source, and that on success secrets.json gets flipped to
// OpenAI OAuth mode.
func TestRunAuthLoginOpenAIHappyPath(t *testing.T) {
	env, _, stderr := newAuthTestEnv(t)
	saved := dockerRunner
	t.Cleanup(func() { dockerRunner = saved })

	var capturedArgs []string
	dockerRunner = func(_ context.Context, _ *Env, args []string) error {
		capturedArgs = args
		// Simulate the codex login helper writing auth.json into the
		// bind-mounted /creds — without this, the post-run stat check
		// would short-circuit before persistOAuthMode runs.
		if err := os.MkdirAll(env.Layout.CodexCredsDir, 0o700); err != nil {
			return err
		}
		return os.WriteFile(env.Layout.CodexCredsFile, []byte(`{"oauth":"tok"}`), 0o600)
	}

	code := runAuthLogin(context.Background(), env, []string{"--provider", "openai", "--skip-build"})
	if code != ExitOK {
		t.Fatalf("runAuthLogin: code=%d stderr=%q", code, stderr.String())
	}

	// Docker invocation surface.
	if !containsArgPair(capturedArgs, "-e", "PROVIDER=openai") {
		t.Errorf("docker args missing PROVIDER=openai: %v", capturedArgs)
	}
	if !containsArgPair(capturedArgs, "-v", env.Layout.CodexCredsDir+":/creds") {
		t.Errorf("docker args missing codex creds bind-mount: %v", capturedArgs)
	}
	// The Claude creds dir must NOT be bound for the openai flow — we
	// don't want two vendors' credentials co-resident in the helper.
	for _, a := range capturedArgs {
		if strings.Contains(a, env.Layout.ClaudeCredsDir+":/creds") {
			t.Errorf("docker args wrongly bound claude creds for openai flow: %v", capturedArgs)
		}
	}

	// Secrets file got flipped — OpenAIAuthMode=oauth, Anthropic
	// untouched (we explicitly preserve the other vendor's state).
	sec, err := secrets.Load(env.Layout.SecretsFile)
	if err != nil {
		t.Fatalf("load secrets: %v", err)
	}
	if sec.OpenAIAuthMode != secrets.AuthModeOAuth {
		t.Errorf("OpenAIAuthMode: got %q want oauth", sec.OpenAIAuthMode)
	}
}

// TestRunAuthLoginAnthropicPreservesHistoricalFlow guards the byte-for-
// byte compat property: `agentctl auth login` (no --provider) on a
// fresh install behaves exactly like it did before phase 2.
func TestRunAuthLoginAnthropicPreservesHistoricalFlow(t *testing.T) {
	env, _, stderr := newAuthTestEnv(t)
	saved := dockerRunner
	t.Cleanup(func() { dockerRunner = saved })

	var capturedArgs []string
	dockerRunner = func(_ context.Context, _ *Env, args []string) error {
		capturedArgs = args
		if err := os.MkdirAll(env.Layout.ClaudeCredsDir, 0o700); err != nil {
			return err
		}
		return os.WriteFile(env.Layout.ClaudeCredsFile, []byte(`{"oauth":"tok"}`), 0o600)
	}

	code := runAuthLogin(context.Background(), env, []string{"--skip-build"})
	if code != ExitOK {
		t.Fatalf("runAuthLogin: code=%d stderr=%q", code, stderr.String())
	}
	if !containsArgPair(capturedArgs, "-e", "PROVIDER=anthropic") {
		t.Errorf("docker args missing PROVIDER=anthropic default: %v", capturedArgs)
	}
	if !containsArgPair(capturedArgs, "-v", env.Layout.ClaudeCredsDir+":/creds") {
		t.Errorf("docker args missing claude creds bind-mount: %v", capturedArgs)
	}
	sec, err := secrets.Load(env.Layout.SecretsFile)
	if err != nil {
		t.Fatalf("load secrets: %v", err)
	}
	if sec.AnthropicAuthMode != secrets.AuthModeOAuth {
		t.Errorf("AnthropicAuthMode: got %q want oauth", sec.AnthropicAuthMode)
	}
}

// TestRunAuthLoginNoCredsFileWritten fails closed: if the helper exits
// successfully but never wrote the credentials file, persistOAuthMode
// must NOT run (otherwise a cancelled login would silently leave the
// session manager looking for a missing creds file on next start).
func TestRunAuthLoginNoCredsFileWritten(t *testing.T) {
	env, _, _ := newAuthTestEnv(t)
	saved := dockerRunner
	t.Cleanup(func() { dockerRunner = saved })
	dockerRunner = func(_ context.Context, _ *Env, _ []string) error { return nil }

	code := runAuthLogin(context.Background(), env, []string{"--provider", "openai", "--skip-build"})
	if code == ExitOK {
		t.Fatal("runAuthLogin should fail when creds file is missing")
	}
	sec, err := secrets.Load(env.Layout.SecretsFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("load secrets: %v", err)
	}
	if sec.OpenAIAuthMode == secrets.AuthModeOAuth {
		t.Error("OpenAIAuthMode must not be flipped to oauth on a failed login")
	}
}

// TestRunAuthStatusSingleProviderUnchanged preserves the historical
// single-line output for the Anthropic-only case. ADR 0020 §UX
// principles — provider invisibility while only one is configured.
func TestRunAuthStatusSingleProviderUnchanged(t *testing.T) {
	env, stdout, _ := newAuthTestEnv(t)
	if err := secrets.Save(env.Layout.SecretsFile, secrets.Secrets{
		AnthropicAPIKey: "sk-ant",
	}); err != nil {
		t.Fatalf("save secrets: %v", err)
	}
	code := runAuthStatus(env)
	if code != ExitOK {
		t.Fatalf("runAuthStatus: code=%d", code)
	}
	out := stdout.String()
	if !strings.Contains(out, "anthropic auth mode: api_key") {
		t.Errorf("missing legacy single-line output; got:\n%s", out)
	}
	// Must NOT print the two-provider table on a single-provider install.
	if strings.Contains(out, "openai") {
		t.Errorf("single-provider status leaked openai row; got:\n%s", out)
	}
}

// TestRunAuthStatusTwoProvidersTable verifies the side-by-side table
// surface that fires once both providers are configured (per plan §2.2).
// Each row leads with the provider id so callers can grep one out.
func TestRunAuthStatusTwoProvidersTable(t *testing.T) {
	env, stdout, _ := newAuthTestEnv(t)
	if err := secrets.Save(env.Layout.SecretsFile, secrets.Secrets{
		AnthropicAPIKey: "sk-ant",
		OpenAIAPIKey:    "sk-openai",
	}); err != nil {
		t.Fatalf("save secrets: %v", err)
	}
	code := runAuthStatus(env)
	if code != ExitOK {
		t.Fatalf("runAuthStatus: code=%d", code)
	}
	out := stdout.String()
	if !strings.Contains(out, "anthropic") {
		t.Errorf("table missing anthropic row; got:\n%s", out)
	}
	if !strings.Contains(out, "openai") {
		t.Errorf("table missing openai row; got:\n%s", out)
	}
	if !strings.Contains(out, "api_key") {
		t.Errorf("table missing api_key mode; got:\n%s", out)
	}
}

// TestRunAuthStatusCustomEndpointOAuthWarning enforces the Phase 5 rule:
// custom-endpoint + OAuth on the same provider must surface a warning so
// the user knows the OAuth credentials are being ignored
// (CODEX_PROVIDER_PLAN §5.2).
func TestRunAuthStatusCustomEndpointOAuthWarning(t *testing.T) {
	env, stdout, _ := newAuthTestEnv(t)
	// Stand up an oauth auth.json on disk so EnabledProviders sees both
	// providers and we land in the two-provider table.
	if err := os.MkdirAll(env.Layout.CodexCredsDir, 0o700); err != nil {
		t.Fatalf("mkdir codex creds: %v", err)
	}
	if err := os.WriteFile(env.Layout.CodexCredsFile, []byte(`{"oauth":"x"}`), 0o600); err != nil {
		t.Fatalf("write creds: %v", err)
	}
	if err := secrets.Save(env.Layout.SecretsFile, secrets.Secrets{
		AnthropicAPIKey: "sk-ant",
		OpenAIAuthMode:  secrets.AuthModeOAuth,
		OpenAIBaseURL:   "https://gateway.example.com",
		OpenAIAuthToken: "gw-token",
	}); err != nil {
		t.Fatalf("save secrets: %v", err)
	}
	code := runAuthStatus(env)
	if code != ExitOK {
		t.Fatalf("runAuthStatus: code=%d", code)
	}
	out := stdout.String()
	if !strings.Contains(out, "warning:") {
		t.Errorf("expected warning line for custom-endpoint+oauth combo; got:\n%s", out)
	}
	if !strings.Contains(out, "endpoint") {
		t.Errorf("expected openai mode=endpoint to win precedence; got:\n%s", out)
	}
}

func containsArgPair(args []string, a, b string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == a && args[i+1] == b {
			return true
		}
	}
	return false
}
