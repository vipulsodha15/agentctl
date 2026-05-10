package store

import (
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	st, err := Open(Options{Path: filepath.Join(dir, "agentd.db")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestMigrateFromEmpty(t *testing.T) {
	st := openTestStore(t)
	if err := st.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	v, err := st.SchemaVersion()
	if err != nil {
		t.Fatalf("schema version: %v", err)
	}
	if v != SchemaMaxVersion {
		t.Errorf("schema version = %d, want %d", v, SchemaMaxVersion)
	}
}

func TestMigrateIdempotent(t *testing.T) {
	st := openTestStore(t)
	for i := 0; i < 3; i++ {
		if err := st.Migrate(); err != nil {
			t.Fatalf("migrate iter %d: %v", i, err)
		}
	}
}

func TestApplyMCPSeedInsertOrIgnore(t *testing.T) {
	st := openTestStore(t)
	if err := st.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	rows := []MCPSeedRow{{
		Name:           "github",
		URL:            "https://api.githubcopilot.com/mcp/",
		Transport:      "http",
		Kind:           "github_pat",
		DefaultEnabled: true,
		Description:    "GitHub MCP server",
	}}
	inserted, err := st.ApplyMCPSeed(rows)
	if err != nil {
		t.Fatalf("apply seed: %v", err)
	}
	if inserted != 1 {
		t.Errorf("inserted = %d, want 1", inserted)
	}
	insertedAgain, err := st.ApplyMCPSeed(rows)
	if err != nil {
		t.Fatalf("apply seed again: %v", err)
	}
	if insertedAgain != 0 {
		t.Errorf("inserted again = %d, want 0 (insert-or-ignore)", insertedAgain)
	}
	count, err := st.CountMCPs()
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}
}

func TestIntegrityCheck(t *testing.T) {
	st := openTestStore(t)
	if err := st.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	got, err := st.IntegrityCheck()
	if err != nil {
		t.Fatalf("integrity check: %v", err)
	}
	if got != "ok" {
		t.Errorf("integrity = %q, want %q", got, "ok")
	}
}
