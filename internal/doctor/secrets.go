package doctor

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/agentctl/agentctl/internal/secrets"
)

const (
	envSkipAnthropic = "AGENTCTL_SKIP_ANTHROPIC_VALIDATE"
	envSkipGitHub    = "AGENTCTL_SKIP_GITHUB_PAT_CHECK"
)

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

func checkSecretsFresh(secretsPath string, client httpDoer) Check {
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	skipAnthropic := os.Getenv(envSkipAnthropic) == "1"
	skipGitHub := os.Getenv(envSkipGitHub) == "1"
	if skipAnthropic && skipGitHub {
		return Check{
			Name:    "secrets.fresh",
			Status:  StatusSkip,
			Message: "skipped (AGENTCTL_SKIP_ANTHROPIC_VALIDATE=1 and AGENTCTL_SKIP_GITHUB_PAT_CHECK=1)",
		}
	}
	sec, err := secrets.Load(secretsPath)
	if err != nil {
		return Check{
			Name:    "secrets.fresh",
			Status:  StatusFail,
			Message: "secrets.json unreadable; run `agentctl init`",
			Detail:  err.Error(),
		}
	}
	var fails []string
	if !skipAnthropic {
		if err := probeAnthropic(client, sec.AnthropicAPIKey); err != nil {
			fails = append(fails, "anthropic: "+err.Error()+"; run `agentctl init --reset-token anthropic`")
		}
	}
	if !skipGitHub {
		if err := probeGitHub(client, sec.GitHubPAT); err != nil {
			fails = append(fails, "github: "+err.Error()+"; run `agentctl init --reset-token github`")
		}
	}
	if len(fails) > 0 {
		return Check{
			Name:    "secrets.fresh",
			Status:  StatusFail,
			Message: "token validation failed",
			Detail:  joinLines(fails),
		}
	}
	parts := []string{}
	if skipAnthropic {
		parts = append(parts, "Anthropic skipped")
	} else {
		parts = append(parts, "Anthropic ok")
	}
	if skipGitHub {
		parts = append(parts, "GitHub skipped")
	} else {
		parts = append(parts, "GitHub ok")
	}
	return Check{
		Name:    "secrets.fresh",
		Status:  StatusOK,
		Message: parts[0] + ", " + parts[1],
	}
}

func probeAnthropic(client httpDoer, key string) error {
	if key == "" {
		return fmt.Errorf("ANTHROPIC_API_KEY not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.anthropic.com/v1/models", nil)
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == 200 {
		return nil
	}
	return fmt.Errorf("status %d", resp.StatusCode)
}

func probeGitHub(client httpDoer, pat string) error {
	if pat == "" {
		return fmt.Errorf("GITHUB_PAT not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+pat)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == 200 {
		return nil
	}
	return fmt.Errorf("status %d", resp.StatusCode)
}
