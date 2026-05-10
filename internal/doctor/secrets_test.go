package doctor

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentctl/agentctl/internal/secrets"
)

type fakeDoer struct {
	statusByURL map[string]int
	calls       []string
	err         error
}

func (f *fakeDoer) Do(req *http.Request) (*http.Response, error) {
	f.calls = append(f.calls, req.URL.String())
	if f.err != nil {
		return nil, f.err
	}
	status := 200
	if c, ok := f.statusByURL[req.URL.String()]; ok {
		status = c
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewBufferString("{}")),
		Header:     http.Header{},
	}, nil
}

func writeSecretsFile(t *testing.T, key, pat string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.json")
	if err := secrets.Save(path, secrets.Secrets{V: 1, AnthropicAPIKey: key, GitHubPAT: pat, GitHubPATKind: "classic"}); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCheckSecretsFreshOK(t *testing.T) {
	t.Setenv(envSkipAnthropic, "")
	t.Setenv(envSkipGitHub, "")
	path := writeSecretsFile(t, "sk-test", "ghp_test")
	doer := &fakeDoer{statusByURL: map[string]int{
		"https://api.anthropic.com/v1/models": 200,
		"https://api.github.com/user":         200,
	}}
	c := checkSecretsFresh(path, doer)
	if c.Status != StatusOK {
		t.Errorf("expected ok, got %s: %s / %s", c.Status, c.Message, c.Detail)
	}
	if len(doer.calls) != 2 {
		t.Errorf("expected 2 probe calls, got %d", len(doer.calls))
	}
}

func TestCheckSecretsFreshFailAnthropic(t *testing.T) {
	t.Setenv(envSkipAnthropic, "")
	t.Setenv(envSkipGitHub, "")
	path := writeSecretsFile(t, "sk-bad", "ghp_test")
	doer := &fakeDoer{statusByURL: map[string]int{
		"https://api.anthropic.com/v1/models": 401,
		"https://api.github.com/user":         200,
	}}
	c := checkSecretsFresh(path, doer)
	if c.Status != StatusFail {
		t.Fatalf("expected fail, got %s", c.Status)
	}
	if !strings.Contains(c.Detail, "anthropic") {
		t.Errorf("expected anthropic in detail; got %q", c.Detail)
	}
	if !strings.Contains(c.Detail, "--reset-token anthropic") {
		t.Errorf("expected reset-token hint; got %q", c.Detail)
	}
}

func TestCheckSecretsFreshSkippedBoth(t *testing.T) {
	t.Setenv(envSkipAnthropic, "1")
	t.Setenv(envSkipGitHub, "1")
	path := writeSecretsFile(t, "sk-test", "ghp_test")
	c := checkSecretsFresh(path, &fakeDoer{})
	if c.Status != StatusSkip {
		t.Errorf("expected skip, got %s", c.Status)
	}
}

func TestCheckSecretsFreshMissingFile(t *testing.T) {
	t.Setenv(envSkipAnthropic, "")
	t.Setenv(envSkipGitHub, "")
	c := checkSecretsFresh(filepath.Join(t.TempDir(), "absent.json"), &fakeDoer{})
	if c.Status != StatusFail {
		t.Errorf("expected fail, got %s", c.Status)
	}
	_ = os.Stat
}
