package mcp

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/agentctl/agentctl/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(store.Options{Path: filepath.Join(dir, "test.db")})
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestRegistryAddListGet(t *testing.T) {
	st := newTestStore(t)
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	reg := NewRegistry(Options{Store: st, Now: func() time.Time { return now }})
	ctx := context.Background()

	if err := reg.Add(ctx, Entry{Name: "github", URL: "https://example/", Transport: "http", Kind: "github_pat", DefaultEnabled: true, Description: "desc"}); err != nil {
		t.Fatal(err)
	}
	got, err := reg.Get(ctx, "github")
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "https://example/" || got.Kind != "github_pat" || !got.DefaultEnabled {
		t.Fatalf("get mismatch: %+v", got)
	}
	list, err := reg.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "github" {
		t.Fatalf("list mismatch: %+v", list)
	}
}

func TestRegistryAddRejectsDuplicate(t *testing.T) {
	st := newTestStore(t)
	reg := NewRegistry(Options{Store: st})
	ctx := context.Background()
	if err := reg.Add(ctx, Entry{Name: "n", URL: "u", Transport: "http", Kind: "none"}); err != nil {
		t.Fatal(err)
	}
	err := reg.Add(ctx, Entry{Name: "n", URL: "u2", Transport: "http", Kind: "none"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestRegistryUpdatePartial(t *testing.T) {
	st := newTestStore(t)
	reg := NewRegistry(Options{Store: st})
	ctx := context.Background()
	if err := reg.Add(ctx, Entry{Name: "x", URL: "u1", Transport: "http", Kind: "none", Description: "old"}); err != nil {
		t.Fatal(err)
	}
	newURL := "u2"
	if err := reg.Update(ctx, "x", EntryUpdate{URL: &newURL}); err != nil {
		t.Fatal(err)
	}
	got, _ := reg.Get(ctx, "x")
	if got.URL != "u2" || got.Description != "old" {
		t.Fatalf("partial update lost description: %+v", got)
	}
	desc := ""
	if err := reg.Update(ctx, "x", EntryUpdate{Description: &desc}); err != nil {
		t.Fatal(err)
	}
	got, _ = reg.Get(ctx, "x")
	if got.Description != "" {
		t.Fatalf("expected description cleared, got %q", got.Description)
	}
}

func TestRegistrySetDefault(t *testing.T) {
	st := newTestStore(t)
	reg := NewRegistry(Options{Store: st})
	ctx := context.Background()
	if err := reg.Add(ctx, Entry{Name: "y", URL: "u", Transport: "http", Kind: "none", DefaultEnabled: false}); err != nil {
		t.Fatal(err)
	}
	if err := reg.SetDefault(ctx, "y", true); err != nil {
		t.Fatal(err)
	}
	got, _ := reg.Get(ctx, "y")
	if !got.DefaultEnabled {
		t.Fatal("expected default_enabled=true")
	}
	if err := reg.SetDefault(ctx, "missing", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestRegistryRemove(t *testing.T) {
	st := newTestStore(t)
	reg := NewRegistry(Options{Store: st})
	ctx := context.Background()
	_ = reg.Add(ctx, Entry{Name: "z", URL: "u", Transport: "http", Kind: "none"})
	if err := reg.Remove(ctx, "z", false); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Get(ctx, "z"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found after remove, got %v", err)
	}
	if err := reg.Remove(ctx, "z", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("remove of missing entry should return ErrNotFound, got %v", err)
	}
}
