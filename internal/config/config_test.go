package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	c := Default()
	c.Image.PinnedID = "sha256:abcd"
	if err := Save(path, c); err != nil {
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
	if got.Image.PinnedID != "sha256:abcd" {
		t.Errorf("pinned_id = %q, want sha256:abcd", got.Image.PinnedID)
	}
	if got.Agentd.WebAddr != "127.0.0.1:7777" {
		t.Errorf("web_addr = %q, want default", got.Agentd.WebAddr)
	}
	if got.Pricing.Tables.Version != 1 {
		t.Errorf("pricing version = %d, want 1", got.Pricing.Tables.Version)
	}
}

func TestGetSet(t *testing.T) {
	c := Default()
	if err := Set(&c, "agentd.log_level", "debug"); err != nil {
		t.Fatalf("set log level: %v", err)
	}
	v, ok := Get(c, "agentd.log_level")
	if !ok || v != "debug" {
		t.Errorf("get agentd.log_level = (%q,%v), want (debug,true)", v, ok)
	}
	if err := Set(&c, "session.cpu_limit", "3.5"); err != nil {
		t.Fatalf("set cpu_limit: %v", err)
	}
	if c.Session.CPULimit != 3.5 {
		t.Errorf("cpu_limit = %v, want 3.5", c.Session.CPULimit)
	}
	if err := Set(&c, "nonsense", "x"); err == nil {
		t.Errorf("expected error for unknown key")
	}
}

func TestEnsurePerms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	if err := os.WriteFile(path, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	fixed, err := EnsurePerms(path, 0o600)
	if err != nil {
		t.Fatalf("ensure perms: %v", err)
	}
	if !fixed {
		t.Errorf("expected fix")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("perm = %o, want 0o600", info.Mode().Perm())
	}
}

func TestExpandHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	got := ExpandHome("~/foo/bar")
	want := filepath.Join(home, "foo/bar")
	if got != want {
		t.Errorf("expand = %q, want %q", got, want)
	}
	if ExpandHome("/abs") != "/abs" {
		t.Errorf("expand absolute path changed")
	}
}
