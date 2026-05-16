package sm

import (
	"context"
	"testing"
	"time"

	"github.com/agentctl/agentctl/internal/fan"
	"github.com/agentctl/agentctl/internal/store"
)

// TestCreatePersistsLastUsedProvider locks in the sticky-per-workspace
// behaviour the resolver depends on (ADR 0020 §3 / §UX principles
// "adaptive default, not config"). Each Create writes the provider into
// workspace_state so the next Create on the same workspace — when the
// caller doesn't pass --provider explicitly — can pick it up via the
// resolver's last_used_provider fallback. Without the write, multi-
// provider users would see the resolver swap providers between sessions
// for no reason.
func TestCreatePersistsLastUsedProvider(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(store.Options{Path: dir + "/db.sqlite"})
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	fc := newFakeControl()
	mgr := New(Options{
		Store:           st,
		SessionsDir:     dir,
		Hub:             fan.NewHub(),
		Control:         fc,
		DefaultModel:    "claude-sonnet-4-6",
		SnapshotTimeout: 100 * time.Millisecond,
	})

	ctx := context.Background()

	if _, err := mgr.Create(ctx, CreateRequest{Name: "first", Provider: "anthropic"}); err != nil {
		t.Fatalf("create anthropic: %v", err)
	}
	got, err := st.WorkspaceState("last_used_provider")
	if err != nil {
		t.Fatalf("read last_used_provider: %v", err)
	}
	if got != "anthropic" {
		t.Errorf("last_used_provider after anthropic Create = %q, want anthropic", got)
	}

	// A second Create with a different provider must overwrite — the
	// resolver's fallback chain assumes the slot always reflects the
	// most recent choice, not the first.
	if _, err := mgr.Create(ctx, CreateRequest{Name: "second", Provider: "openai"}); err != nil {
		t.Fatalf("create openai: %v", err)
	}
	got, err = st.WorkspaceState("last_used_provider")
	if err != nil {
		t.Fatalf("read last_used_provider after switch: %v", err)
	}
	if got != "openai" {
		t.Errorf("last_used_provider after openai Create = %q, want openai", got)
	}
}
