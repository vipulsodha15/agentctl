package sm

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/agentctl/agentctl/internal/fan"
	"github.com/agentctl/agentctl/internal/mcp"
	"github.com/agentctl/agentctl/internal/proto"
	"github.com/agentctl/agentctl/internal/store"
)

type fakeMCPRegistry struct {
	entries []mcp.Entry
}

func (f *fakeMCPRegistry) List(_ context.Context) ([]mcp.Entry, error) {
	return f.entries, nil
}
func (f *fakeMCPRegistry) Get(_ context.Context, name string) (mcp.Entry, error) {
	for _, e := range f.entries {
		if e.Name == name {
			return e, nil
		}
	}
	return mcp.Entry{}, mcp.ErrNotFound
}
func (f *fakeMCPRegistry) Add(_ context.Context, _ mcp.Entry) error                    { return nil }
func (f *fakeMCPRegistry) Update(_ context.Context, _ string, _ mcp.EntryUpdate) error { return nil }
func (f *fakeMCPRegistry) Remove(_ context.Context, _ string, _ bool) error            { return nil }
func (f *fakeMCPRegistry) SetDefault(_ context.Context, _ string, _ bool) error        { return nil }

func TestProbeFailureBroadcastsOnFirstAttach(t *testing.T) {
	dir := t.TempDir()
	// Reserve and immediately close a port so the probe sees a refused connection.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	_ = ln.Close()

	st, err := store.Open(store.Options{Path: filepath.Join(dir, "db")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}

	reg := &fakeMCPRegistry{entries: []mcp.Entry{
		{Name: "down", URL: "http://" + addr + "/", Transport: "http", Kind: "none", DefaultEnabled: true},
	}}
	mgr := New(Options{
		Store:           st,
		SessionsDir:     dir,
		Hub:             fan.NewHub(),
		MCPs:            reg,
		DefaultModel:    "claude-sonnet-4-6",
		SnapshotTimeout: 100 * time.Millisecond,
	}).(*manager)

	ctx := context.Background()
	res, err := mgr.Create(ctx, CreateRequest{Name: "p", Provider: "anthropic"})
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		a := mgr.actorFor(res.SessionID)
		a.mu.RLock()
		failures := len(a.mcpFailures)
		statusReady := len(a.mcpStatus) == 1
		a.mu.RUnlock()
		if failures > 0 && statusReady {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	stream, err := mgr.Attach(ctx, res.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	mustEvent(t, stream, proto.EventSessionSnapshot)
	ev := mustEvent(t, stream, proto.EventMCPUnreachable)
	if ev.Kind != proto.EventMCPUnreachable {
		t.Fatalf("expected mcp.unreachable, got %s", ev.Kind)
	}
}

func TestRenderUnknownTransportEmitsSkipped(t *testing.T) {
	r := mcp.Render(mcp.RenderInputs{
		Entries: []mcp.Entry{
			{Name: "good", URL: "http://x/", Transport: "http", Kind: "none"},
			{Name: "bad", URL: "x://y/", Transport: "smb", Kind: "none"},
		},
	})
	if len(r.Configs) != 1 || r.Configs[0].Name != "good" {
		t.Errorf("configs=%+v", r.Configs)
	}
	if len(r.Skipped) != 1 || r.Skipped[0].Name != "bad" {
		t.Errorf("skipped=%+v", r.Skipped)
	}
}
