package agentd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/agentctl/agentctl/internal/log"
	"github.com/agentctl/agentctl/internal/secrets"
	"github.com/agentctl/agentctl/internal/websrv"
)

// secretsAdapter implements websrv.SecretsService backed by the on-disk
// secrets.json file. Writes are serialized so concurrent UI submissions can
// not interleave reads/writes against the file.
type secretsAdapter struct {
	path string
	mu   sync.Mutex
}

func newSecretsAdapter(path string) *secretsAdapter {
	return &secretsAdapter{path: path}
}

func (a *secretsAdapter) loadLocked() (secrets.Secrets, error) {
	sec, err := secrets.Load(a.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return secrets.Secrets{}, err
	}
	return sec, nil
}

func (a *secretsAdapter) GetGitHub(_ context.Context) (websrv.GitHubTokenInfo, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	sec, err := a.loadLocked()
	if err != nil {
		return websrv.GitHubTokenInfo{}, err
	}
	return makeGitHubInfo(sec), nil
}

func (a *secretsAdapter) UpdateGitHub(ctx context.Context, token string, validate bool) (websrv.GitHubTokenInfo, error) {
	if validate && os.Getenv("AGENTCTL_SKIP_GITHUB_PAT_CHECK") != "1" {
		if err := secrets.ValidateGitHubPAT(ctx, token); err != nil {
			return websrv.GitHubTokenInfo{}, err
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	sec, err := a.loadLocked()
	if err != nil {
		return websrv.GitHubTokenInfo{}, err
	}
	sec.GitHubPAT = token
	sec.GitHubPATKind = secrets.InferGitHubPATKind(token)
	if err := secrets.Save(a.path, sec); err != nil {
		return websrv.GitHubTokenInfo{}, fmt.Errorf("save secrets: %w", err)
	}
	log.RegisterSecret(token)
	return makeGitHubInfo(sec), nil
}

func makeGitHubInfo(sec secrets.Secrets) websrv.GitHubTokenInfo {
	info := websrv.GitHubTokenInfo{}
	if sec.GitHubPAT == "" {
		return info
	}
	info.HasToken = true
	info.Kind = sec.GitHubPATKind
	if n := len(sec.GitHubPAT); n >= 4 {
		info.Hint = sec.GitHubPAT[n-4:]
	}
	return info
}
