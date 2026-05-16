package sm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentctl/agentctl/internal/fan"
	"github.com/agentctl/agentctl/internal/secrets"
)

// resolveCodexCredsBindSourceCase drives a single matrix row for the
// truth-table test below — see CODEX_PROVIDER_PLAN §2.3 / §2.4 for the
// shape we're locking in.
type resolveCodexCredsBindSourceCase struct {
	name       string
	provider   string // CreateRequest.Provider equivalent
	secrets    secrets.Secrets
	writeCreds bool // if true, drop a non-empty auth.json into codexCredsDir
	wantSource bool // true when we expect a non-empty bind source
	wantErr    bool
}

func TestResolveCodexCredsBindSourceMatrix(t *testing.T) {
	cases := []resolveCodexCredsBindSourceCase{
		{
			name:       "anthropic_session_never_mounts_codex_creds",
			provider:   secrets.ProviderAnthropic,
			secrets:    secrets.Secrets{OpenAIAuthMode: secrets.AuthModeOAuth},
			writeCreds: true,
			wantSource: false,
		},
		{
			name:       "openai_api_key_mode_no_mount",
			provider:   secrets.ProviderOpenAI,
			secrets:    secrets.Secrets{OpenAIAPIKey: "sk-x"},
			writeCreds: false,
			wantSource: false,
		},
		{
			name:       "openai_oauth_but_creds_file_missing_errors",
			provider:   secrets.ProviderOpenAI,
			secrets:    secrets.Secrets{OpenAIAuthMode: secrets.AuthModeOAuth},
			writeCreds: false,
			wantSource: false,
			wantErr:    true,
		},
		{
			name:       "openai_oauth_with_creds_returns_dir",
			provider:   secrets.ProviderOpenAI,
			secrets:    secrets.Secrets{OpenAIAuthMode: secrets.AuthModeOAuth},
			writeCreds: true,
			wantSource: true,
		},
		{
			name:     "openai_custom_endpoint_takes_precedence_over_oauth",
			provider: secrets.ProviderOpenAI,
			secrets: secrets.Secrets{
				OpenAIAuthMode:  secrets.AuthModeOAuth,
				OpenAIBaseURL:   "https://gateway.example.com",
				OpenAIAuthToken: "gw-tok",
			},
			writeCreds: true,
			wantSource: false, // gateway wins, bind-mount silenced
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			secretsPath := filepath.Join(dir, "secrets.json")
			if err := secrets.Save(secretsPath, tc.secrets); err != nil {
				t.Fatalf("save secrets: %v", err)
			}
			credsDir := filepath.Join(dir, "codex")
			if err := os.MkdirAll(credsDir, 0o700); err != nil {
				t.Fatalf("mkdir creds: %v", err)
			}
			if tc.writeCreds {
				if err := os.WriteFile(filepath.Join(credsDir, "auth.json"), []byte(`{"tok":"x"}`), 0o600); err != nil {
					t.Fatalf("write creds: %v", err)
				}
			}
			m := New(Options{
				SessionsDir:    filepath.Join(dir, "sessions"),
				Hub:            fan.NewHub(),
				SecretsPath:    secretsPath,
				CodexCredsDir:  credsDir,
				DefaultModel:   "gpt-5.5",
				ImageID:        "sha256:abc",
			}).(*manager)

			got, err := m.resolveCodexCredsBindSource(tc.provider)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (source=%q)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveCodexCredsBindSource: %v", err)
			}
			if tc.wantSource && got != credsDir {
				t.Errorf("expected source=%q, got %q", credsDir, got)
			}
			if !tc.wantSource && got != "" {
				t.Errorf("expected no source, got %q", got)
			}
		})
	}
}

// TestResolveCodexCredsBindSourceMissingCredsDir locks in the
// "configuration drift" error path: OAuth mode is set in secrets.json
// but the daemon was started without CodexCredsDir wired into
// Manager.opts. We surface that loudly rather than silently starting an
// unauthenticated container.
func TestResolveCodexCredsBindSourceMissingCredsDir(t *testing.T) {
	dir := t.TempDir()
	secretsPath := filepath.Join(dir, "secrets.json")
	if err := secrets.Save(secretsPath, secrets.Secrets{
		OpenAIAuthMode: secrets.AuthModeOAuth,
	}); err != nil {
		t.Fatalf("save secrets: %v", err)
	}
	m := New(Options{
		SessionsDir:  dir,
		Hub:          fan.NewHub(),
		SecretsPath:  secretsPath,
		DefaultModel: "gpt-5.5",
		// CodexCredsDir intentionally empty.
	}).(*manager)
	if _, err := m.resolveCodexCredsBindSource(secrets.ProviderOpenAI); err == nil {
		t.Fatal("expected error when CodexCredsDir is unconfigured")
	}
}

// TestCreateOpenAIOAuthWiresMountAndOmitsAPIKey is the E2E-style test
// the plan calls for: spin up a session with Provider=openai under
// OAuth mode, stub auth.json on disk, assert (a) no OPENAI_API_KEY in
// the container env, and (b) the /home/agent/.codex bind-mount is
// wired into the container spec.
func TestCreateOpenAIOAuthWiresMountAndOmitsAPIKey(t *testing.T) {
	base := t.TempDir()
	secretsPath := filepath.Join(base, "secrets.json")
	if err := secrets.Save(secretsPath, secrets.Secrets{
		OpenAIAPIKey:   "sk-stale-must-not-leak",
		OpenAIAuthMode: secrets.AuthModeOAuth,
	}); err != nil {
		t.Fatalf("save secrets: %v", err)
	}
	credsDir := filepath.Join(base, "codex")
	if err := os.MkdirAll(credsDir, 0o700); err != nil {
		t.Fatalf("mkdir creds: %v", err)
	}
	if err := os.WriteFile(filepath.Join(credsDir, "auth.json"), []byte(`{"oauth":"x"}`), 0o600); err != nil {
		t.Fatalf("write creds: %v", err)
	}

	fc := newFakeControl()
	cm := newFakeContainerManager()
	fc.bound = cm
	pinned := "sha256:abc"
	mgr := New(Options{
		SessionsDir:     filepath.Join(base, "sessions"),
		Hub:             fan.NewHub(),
		Containers:      cm,
		Control:         fc,
		DefaultModel:    "gpt-5.5",
		ImageID:         pinned,
		PinnedImageID:   func() string { return pinned },
		SecretsPath:     secretsPath,
		CodexCredsDir:   credsDir,
		SnapshotTimeout: 100 * time.Millisecond,
	})
	defer func() { _ = mgr.Shutdown(context.Background()) }()
	ctx := context.Background()
	r, err := mgr.Create(ctx, CreateRequest{Name: "oai-oauth", Provider: secrets.ProviderOpenAI})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	cm.mu.Lock()
	specs := append([]ContainerSpec(nil), cm.specs...)
	cm.mu.Unlock()
	if len(specs) == 0 {
		t.Fatal("create did not record a container spec")
	}
	spec := specs[len(specs)-1]
	if !hasCodexCredsMount(spec.Mounts, credsDir) {
		t.Errorf("missing /home/agent/.codex bind-mount from %q; got mounts=%+v", credsDir, spec.Mounts)
	}
	if !containsEnv(spec.Env, "CODEX_HOME=/home/agent/.codex") {
		t.Errorf("missing CODEX_HOME env; got %v", spec.Env)
	}

	// Secrets env-file must not carry OPENAI_API_KEY (OAuth precedence).
	envBody, err := os.ReadFile(filepath.Join(base, "sessions", r.SessionID, "secrets.env"))
	if err != nil {
		t.Fatalf("read secrets.env: %v", err)
	}
	got := string(envBody)
	if strings.Contains(got, "OPENAI_API_KEY=") {
		t.Errorf("OAuth mode must not export OPENAI_API_KEY; got:\n%s", got)
	}
}

// TestRestartOpenAIOAuthPreservesMount mirrors the Claude bind-mount
// restart test (TestRestartPreservesOAuthCredsBindMount) — Restart must
// re-resolve and re-mount the Codex creds dir, not silently drop it,
// otherwise a session that auto-restarts on Send would lose its OAuth
// token mid-flight.
func TestRestartOpenAIOAuthPreservesMount(t *testing.T) {
	base := t.TempDir()
	secretsPath := filepath.Join(base, "secrets.json")
	if err := secrets.Save(secretsPath, secrets.Secrets{
		OpenAIAuthMode: secrets.AuthModeOAuth,
	}); err != nil {
		t.Fatalf("save secrets: %v", err)
	}
	credsDir := filepath.Join(base, "codex")
	if err := os.MkdirAll(credsDir, 0o700); err != nil {
		t.Fatalf("mkdir creds: %v", err)
	}
	if err := os.WriteFile(filepath.Join(credsDir, "auth.json"), []byte(`{"oauth":"x"}`), 0o600); err != nil {
		t.Fatalf("write creds: %v", err)
	}

	fc := newFakeControl()
	cm := newFakeContainerManager()
	fc.bound = cm
	pinned := "sha256:abc"
	mgr := New(Options{
		SessionsDir:     filepath.Join(base, "sessions"),
		Hub:             fan.NewHub(),
		Containers:      cm,
		Control:         fc,
		DefaultModel:    "gpt-5.5",
		ImageID:         pinned,
		PinnedImageID:   func() string { return pinned },
		SecretsPath:     secretsPath,
		CodexCredsDir:   credsDir,
		SnapshotTimeout: 100 * time.Millisecond,
	})
	defer func() { _ = mgr.Shutdown(context.Background()) }()
	ctx := context.Background()
	res, err := mgr.Create(ctx, CreateRequest{Name: "oai-restart", Provider: secrets.ProviderOpenAI})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	cm.mu.Lock()
	createdN := len(cm.specs)
	cm.mu.Unlock()

	if _, err := mgr.Restart(ctx, res.SessionID); err != nil {
		t.Fatalf("restart: %v", err)
	}
	cm.mu.Lock()
	specs := append([]ContainerSpec(nil), cm.specs...)
	cm.mu.Unlock()
	if len(specs) <= createdN {
		t.Fatalf("Restart did not record a new spec: created=%d after_restart=%d", createdN, len(specs))
	}
	if !hasCodexCredsMount(specs[len(specs)-1].Mounts, credsDir) {
		t.Errorf("Restart dropped the /home/agent/.codex bind-mount; mounts=%+v", specs[len(specs)-1].Mounts)
	}
}

func hasCodexCredsMount(mounts []ContainerMount, source string) bool {
	for _, m := range mounts {
		if m.Type == MountBind && m.Source == source && m.Target == "/home/agent/.codex" {
			return true
		}
	}
	return false
}

func containsEnv(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}
