package mcp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEmbeddedSeedHasGithub(t *testing.T) {
	rows, err := ParseSeed(EmbeddedSeed())
	if err != nil {
		t.Fatal(err)
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

func TestResolvePrefersUserOverSiteOverEmbedded(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, "user.toml")
	site := filepath.Join(dir, "site.toml")
	userBody := `[[mcp]]
name="x-from-user"
url="http://u/"
transport="http"
kind="none"
default_enabled=true
`
	siteBody := `[[mcp]]
name="x-from-site"
url="http://s/"
transport="http"
kind="none"
`
	if err := os.WriteFile(user, []byte(userBody), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(site, []byte(siteBody), 0o600); err != nil {
		t.Fatal(err)
	}
	data, src, err := ResolveSeed(user, site)
	if err != nil {
		t.Fatal(err)
	}
	if !src.FromDisk || src.Path != user {
		t.Errorf("expected user path, got %+v", src)
	}
	rows, _ := ParseSeed(data)
	if len(rows) != 1 || rows[0].Name != "x-from-user" {
		t.Errorf("expected user entry, got %+v", rows)
	}

	data, src, err = ResolveSeed("", site)
	if err != nil {
		t.Fatal(err)
	}
	if !src.FromDisk || src.Path != site {
		t.Errorf("expected site path, got %+v", src)
	}
	rows, _ = ParseSeed(data)
	if len(rows) != 1 || rows[0].Name != "x-from-site" {
		t.Errorf("expected site entry, got %+v", rows)
	}

	data, src, err = ResolveSeed("", "")
	if err != nil {
		t.Fatal(err)
	}
	if src.FromDisk {
		t.Errorf("expected embedded fallback, got %+v", src)
	}
	rows, _ = ParseSeed(data)
	if len(rows) == 0 {
		t.Fatal("expected embedded seed to have entries")
	}
}

func TestApplySeedToStore(t *testing.T) {
	st := newTestStore(t)
	rows, err := ParseSeed(EmbeddedSeed())
	if err != nil {
		t.Fatal(err)
	}
	n, err := ApplySeed(st, rows, nowUTC())
	if err != nil {
		t.Fatal(err)
	}
	if n != len(rows) {
		t.Errorf("inserted %d, want %d", n, len(rows))
	}
	again, err := ApplySeed(st, rows, nowUTC())
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Errorf("rerun should be idempotent, got %d", again)
	}
}
