package doctor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/agentctl/agentctl/internal/store"
)

func TestCheckFSPermsDetectsBadPerms(t *testing.T) {
	home := t.TempDir()
	cfgDir := filepath.Join(home, ".config", "agentctl")
	if err := os.MkdirAll(cfgDir, 0o777); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(cfgDir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := DefaultPaths(home)
	c := checkFSPerms(opts)
	if c.Status != StatusFail {
		t.Errorf("expected fail when perms drift, got %s: %s", c.Status, c.Detail)
	}
}

func TestCheckFSPermsPasses(t *testing.T) {
	home := t.TempDir()
	cfgDir := filepath.Join(home, ".config", "agentctl")
	dataDir := filepath.Join(home, ".local", "share", "agentctl")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"config.toml", "secrets.json", "web_token"} {
		p := filepath.Join(cfgDir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	opts := DefaultPaths(home)
	c := checkFSPerms(opts)
	if c.Status != StatusOK {
		t.Errorf("expected ok, got %s: %s", c.Status, c.Detail)
	}
}

func TestCheckDBIntegrityOK(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "agentd.db")
	st, err := store.Open(store.Options{Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	c := checkDBIntegrity(dbPath)
	if c.Status != StatusOK {
		t.Errorf("expected ok, got %s: %s", c.Status, c.Message)
	}
}

func TestCheckDBIntegrityMissing(t *testing.T) {
	c := checkDBIntegrity("/nonexistent/path/db")
	if c.Status != StatusFail {
		t.Errorf("expected fail for missing db, got %s", c.Status)
	}
}
