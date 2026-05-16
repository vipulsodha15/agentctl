package sm

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/agentctl/agentctl/internal/fan"
	"github.com/agentctl/agentctl/internal/proto"
)

type fakeRecorder struct {
	mu      sync.Mutex
	calls   []UsageRecord
	costMap map[string]float64
}

func (f *fakeRecorder) OnUsage(_ context.Context, ev UsageRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, ev)
	return nil
}

func (f *fakeRecorder) CostFor(ev UsageRecord) (float64, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.costMap[ev.Model]; ok {
		return v, true
	}
	return 0, false
}

func newFakeRecorder() *fakeRecorder {
	return &fakeRecorder{
		costMap: map[string]float64{"claude-sonnet-4-6": 1.23},
	}
}

func TestActorPersistsUsageOnRuntimeEvent(t *testing.T) {
	dir := t.TempDir()
	fc := newFakeControl()
	rec := newFakeRecorder()
	mgr := New(Options{
		SessionsDir:     dir,
		Hub:             fan.NewHub(),
		Control:         fc,
		Usage:           rec,
		DefaultModel:    "claude-sonnet-4-6",
		SnapshotTimeout: 100 * time.Millisecond,
	})
	ctx := context.Background()
	r, err := mgr.Create(ctx, CreateRequest{Name: "u", Provider: "anthropic"})
	if err != nil {
		t.Fatal(err)
	}
	stream, _ := mgr.Attach(ctx, r.SessionID)
	defer stream.Close()
	mustEvent(t, stream, proto.EventSessionSnapshot)
	conn := fc.attach(t, r.SessionID, mgr)

	usagePayload := `{"turn_id":"turn_x","model":"claude-sonnet-4-6","input_tokens":100,"output_tokens":50,"cache_read_tokens":0,"cache_write_tokens":0}`
	conn.feedRuntimeEvent(t, r.SessionID, proto.EventUsage, json.RawMessage(usagePayload))

	ev := mustEvent(t, stream, proto.EventUsage)
	var u proto.UsageData
	if err := json.Unmarshal(ev.Data, &u); err != nil {
		t.Fatalf("unmarshal usage event: %v", err)
	}
	if u.TurnID != "turn_x" || u.Model != "claude-sonnet-4-6" {
		t.Errorf("event lost fields: %+v", u)
	}
	if u.CostUSD != 1.23 {
		t.Errorf("expected cost 1.23 from fake recorder, got %v", u.CostUSD)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.calls) != 1 {
		t.Fatalf("recorder calls = %d", len(rec.calls))
	}
	got := rec.calls[0]
	if got.SessionID != r.SessionID || got.TurnID != "turn_x" {
		t.Errorf("recorder call wrong: %+v", got)
	}
	if got.InputTokens != 100 || got.OutputTokens != 50 {
		t.Errorf("recorder tokens wrong: %+v", got)
	}
	if got.Model != "claude-sonnet-4-6" {
		t.Errorf("recorder model=%q", got.Model)
	}
}

func TestActorWithoutRecorderForwardsUsage(t *testing.T) {
	dir := t.TempDir()
	fc := newFakeControl()
	mgr := New(Options{
		SessionsDir:     dir,
		Hub:             fan.NewHub(),
		Control:         fc,
		DefaultModel:    "claude-sonnet-4-6",
		SnapshotTimeout: 100 * time.Millisecond,
	})
	ctx := context.Background()
	r, _ := mgr.Create(ctx, CreateRequest{Name: "u2", Provider: "anthropic"})
	stream, _ := mgr.Attach(ctx, r.SessionID)
	defer stream.Close()
	mustEvent(t, stream, proto.EventSessionSnapshot)
	conn := fc.attach(t, r.SessionID, mgr)

	conn.feedRuntimeEvent(t, r.SessionID, proto.EventUsage,
		json.RawMessage(`{"turn_id":"t","model":"claude-sonnet-4-6","input_tokens":1,"output_tokens":2}`))

	ev := mustEvent(t, stream, proto.EventUsage)
	var u proto.UsageData
	_ = json.Unmarshal(ev.Data, &u)
	if u.TurnID != "t" {
		t.Errorf("turn_id lost: %+v", u)
	}
}
