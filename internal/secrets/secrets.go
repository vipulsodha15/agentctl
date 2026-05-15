package secrets

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	FilePerm = 0o600
	DirPerm  = 0o700
)

// Anthropic auth modes. AuthModeAPIKey is the historical default — agentctl
// injects ANTHROPIC_API_KEY into the session container. AuthModeOAuth means
// the user ran `agentctl auth login`; the session container instead gets a
// bind-mount of the host's Claude credentials directory at /home/agent/.claude
// so the bundled claude CLI (spawned by the Agent SDK) authenticates with
// the user's subscription.
const (
	AuthModeAPIKey = "api_key"
	AuthModeOAuth  = "oauth"
)

type Secrets struct {
	V                  int    `json:"v"`
	AnthropicAPIKey    string `json:"anthropic_api_key,omitempty"`
	AnthropicAuthMode  string `json:"anthropic_auth_mode,omitempty"`
	AnthropicBaseURL   string `json:"anthropic_base_url,omitempty"`
	AnthropicAuthToken string `json:"anthropic_auth_token,omitempty"`
	GitHubPAT          string `json:"github_pat,omitempty"`
	GitHubPATKind      string `json:"github_pat_kind,omitempty"`
}

// ResolvedAuthMode returns the effective mode: explicit AnthropicAuthMode if
// set, otherwise AuthModeAPIKey for backwards compatibility (any pre-existing
// secrets.json that just has anthropic_api_key keeps working unchanged).
func (s Secrets) ResolvedAuthMode() string {
	if s.AnthropicAuthMode == AuthModeOAuth {
		return AuthModeOAuth
	}
	return AuthModeAPIKey
}

func Load(path string) (Secrets, error) {
	var s Secrets
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, err
		}
		return s, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("parse %s: %w", path, err)
	}
	return s, nil
}

func Save(path string, s Secrets) error {
	if s.V == 0 {
		s.V = 1
	}
	if err := os.MkdirAll(filepath.Dir(path), DirPerm); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data, FilePerm)
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".sec-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func GenerateWebToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func WriteWebToken(path, token string) error {
	if err := os.MkdirAll(filepath.Dir(path), DirPerm); err != nil {
		return err
	}
	return writeFileAtomic(path, []byte(token), FilePerm)
}

func ReadWebToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// InferGitHubPATKind classifies a GitHub PAT by its well-known prefix.
func InferGitHubPATKind(pat string) string {
	switch {
	case strings.HasPrefix(pat, "github_pat_"):
		return "fine-grained"
	case strings.HasPrefix(pat, "ghp_"), strings.HasPrefix(pat, "gho_"), strings.HasPrefix(pat, "ghs_"):
		return "classic"
	default:
		return "unknown"
	}
}

// ValidateGitHubPAT calls GET /user against the GitHub API to confirm the
// token is accepted. Returns nil on success, a descriptive error otherwise.
func ValidateGitHubPAT(ctx context.Context, pat string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+pat)
	req.Header.Set("Accept", "application/vnd.github+json")
	client := http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("github api: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == 401 {
		return fmt.Errorf("github PAT rejected (401)")
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("github api status %d", resp.StatusCode)
	}
	return nil
}
