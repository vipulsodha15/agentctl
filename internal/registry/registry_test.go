package registry

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/agentctl/agentctl/internal/store"
)

func TestEmbeddedSeedParses(t *testing.T) {
	data := EmbeddedBytes()
	if len(data) == 0 {
		t.Fatalf("embedded seed empty")
	}
	rows, err := Parse(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rows) == 0 {
		t.Fatalf("expected at least one entry")
	}
	found := false
	for _, r := range rows {
		if r.Name == "github" && r.Kind == "github_pat" && r.Transport == "http" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected github entry in default seed, got %+v", rows)
	}
}

func TestResolvePrefersUser(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, "user.toml")
	site := filepath.Join(dir, "site.toml")
	if err := os.WriteFile(user, []byte(`[[mcp]]
name="x-from-user"
url="http://u/"
transport="http"
kind="none"
default_enabled=true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(site, []byte(`[[mcp]]
name="x-from-site"
url="http://s/"
transport="http"
kind="none"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	data, src, err := Resolve(user, site)
	if err != nil {
		t.Fatal(err)
	}
	if !src.FromDisk || src.Path != user {
		t.Errorf("expected user path, got %+v", src)
	}
	rows, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Name != "x-from-user" {
		t.Errorf("unexpected rows: %+v", rows)
	}
}

func TestApplyToStore(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(store.Options{Path: filepath.Join(dir, "db")})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	rows, err := Parse(EmbeddedBytes())
	if err != nil {
		t.Fatal(err)
	}
	n, err := st.ApplyMCPSeed(rows)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(rows) {
		t.Errorf("inserted %d, want %d", n, len(rows))
	}
}
