package secrets

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.json")
	s := Secrets{
		AnthropicAPIKey: "sk-ant-api03-XYZ",
		GitHubPAT:       "ghp_AAAAAAAAAAAAAAAAAAAAA",
		GitHubPATKind:   "classic",
	}
	if err := Save(path, s); err != nil {
		t.Fatalf("save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != FilePerm {
		t.Errorf("perm = %o, want %o", info.Mode().Perm(), FilePerm)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.V != 1 {
		t.Errorf("v = %d, want 1", got.V)
	}
	if got.AnthropicAPIKey != s.AnthropicAPIKey {
		t.Errorf("anthropic key mismatch")
	}
}

func TestGenerateAndReadWebToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "web_token")
	tok, err := GenerateWebToken()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(tok) < 20 {
		t.Errorf("token too short: %q", tok)
	}
	if err := WriteWebToken(path, tok); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := ReadWebToken(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != tok {
		t.Errorf("read != written")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != FilePerm {
		t.Errorf("perm = %o, want %o", info.Mode().Perm(), FilePerm)
	}
}
