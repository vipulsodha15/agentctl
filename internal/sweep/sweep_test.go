package sweep

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/agentctl/agentctl/internal/store"
)

type fakeManager struct {
	mu           sync.Mutex
	busy         map[string]bool
	known        map[string]struct{}
	stops        []stopCall
	interrupts   []interruptCall
	stopErr      map[string]error
	interruptErr map[string]error
}

type stopCall struct {
	sessionID, reason string
}
type interruptCall struct {
	sessionID  string
	clearQueue bool
}

func newFakeManager() *fakeManager {
	return &fakeManager{
		busy:         map[string]bool{},
		known:        map[string]struct{}{},
		stopErr:      map[string]error{},
		interruptErr: map[string]error{},
	}
}

func (m *fakeManager) addSession(id string, busy bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.known[id] = struct{}{}
	m.busy[id] = busy
}

func (m *fakeManager) Busy(id string) (bool, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.known[id]; !ok {
		return false, false
	}
	return m.busy[id], true
}

func (m *fakeManager) Stop(_ context.Context, id, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.stopErr[id]; ok {
		return e
	}
	m.stops = append(m.stops, stopCall{sessionID: id, reason: reason})
	delete(m.busy, id)
	return nil
}

func (m *fakeManager) Interrupt(_ context.Context, id string, clearQueue bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.interruptErr[id]; ok {
		return e
	}
	m.interrupts = append(m.interrupts, interruptCall{sessionID: id, clearQueue: clearQueue})
	return nil
}

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(store.Options{Path: filepath.Join(dir, "agentd.db")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func insertSession(t *testing.T, st *store.Store, id, status string, lastActivity time.Time) {
	t.Helper()
	if _, err := st.DB().Exec(`INSERT INTO sessions
        (id, name, status, created_at, last_activity_at,
         image_id, skills_snapshot_hash, model, mem_limit_bytes, cpu_limit_cores,
         mcp_set_json, repos_json, session_token)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, "n-"+id, status,
		time.Now().UTC().Format(time.RFC3339Nano),
		lastActivity.UTC().Format(time.RFC3339Nano),
		"sha256:img", "", "claude-sonnet-4-6", int64(4<<30), 2.0,
		"[]", "[]", "tok-"+id,
	); err != nil {
		t.Fatalf("insert %s: %v", id, err)
	}
}

func TestIdleStop_StopsIdleNonBusy(t *testing.T) {
	st := newTestStore(t)
	mgr := newFakeManager()
	now := time.Now().UTC()
	insertSession(t, st, "s-idle", "running", now.Add(-30*time.Minute))
	insertSession(t, st, "s-busy", "running", now.Add(-30*time.Minute))
	insertSession(t, st, "s-fresh", "running", now.Add(-1*time.Minute))
	mgr.addSession("s-idle", false)
	mgr.addSession("s-busy", true)
	mgr.addSession("s-fresh", false)

	sw := New(Options{
		Store: st, Manager: mgr,
		IdleTimeout: 15 * time.Minute,
		Now:         func() time.Time { return now },
	})
	idle := findSweeper(t, sw, "idle_stop")
	n, err := idle.Tick(context.Background())
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if n != 1 {
		t.Errorf("actions=%d want 1", n)
	}
	if len(mgr.stops) != 1 || mgr.stops[0].sessionID != "s-idle" {
		t.Errorf("stops=%v want [s-idle]", mgr.stops)
	}
	if mgr.stops[0].reason != stopReasonIdle {
		t.Errorf("reason=%q want %q", mgr.stops[0].reason, stopReasonIdle)
	}
}

func TestIdleStop_SkipsUnknownActor(t *testing.T) {
	st := newTestStore(t)
	mgr := newFakeManager()
	now := time.Now().UTC()
	insertSession(t, st, "s-orphan-row", "running", now.Add(-30*time.Minute))
	sw := New(Options{
		Store: st, Manager: mgr,
		IdleTimeout: 15 * time.Minute,
		Now:         func() time.Time { return now },
	})
	idle := findSweeper(t, sw, "idle_stop")
	n, err := idle.Tick(context.Background())
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if n != 0 {
		t.Errorf("actions=%d want 0", n)
	}
	if len(mgr.stops) != 0 {
		t.Errorf("stops=%v want []", mgr.stops)
	}
}

func TestHardCutoff_InterruptsThenStopsRunning(t *testing.T) {
	st := newTestStore(t)
	mgr := newFakeManager()
	now := time.Now().UTC()
	insertSession(t, st, "s-old-running", "running", now.Add(-25*time.Hour))
	insertSession(t, st, "s-old-stopped", "stopped", now.Add(-25*time.Hour))
	insertSession(t, st, "s-fresh", "running", now.Add(-1*time.Hour))
	mgr.addSession("s-old-running", true)
	mgr.addSession("s-old-stopped", false)
	mgr.addSession("s-fresh", false)

	sw := New(Options{
		Store: st, Manager: mgr,
		MaxIdle: 24 * time.Hour,
		Now:     func() time.Time { return now },
	})
	hard := findSweeper(t, sw, "hard_cutoff")
	n, err := hard.Tick(context.Background())
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if n != 2 {
		t.Errorf("actions=%d want 2", n)
	}
	if len(mgr.interrupts) != 1 || mgr.interrupts[0].sessionID != "s-old-running" {
		t.Errorf("interrupts=%v want [s-old-running]", mgr.interrupts)
	}
	stopped := stoppedIDs(mgr.stops)
	want := []string{"s-old-running", "s-old-stopped"}
	if !equalStrSet(stopped, want) {
		t.Errorf("stops=%v want %v", stopped, want)
	}
	for _, c := range mgr.stops {
		if c.reason != stopReasonHardCutoff {
			t.Errorf("reason=%q want %q", c.reason, stopReasonHardCutoff)
		}
	}
}

func TestHardCutoff_DoesNotSkipBusy(t *testing.T) {
	st := newTestStore(t)
	mgr := newFakeManager()
	now := time.Now().UTC()
	insertSession(t, st, "s-busy-old", "running", now.Add(-25*time.Hour))
	mgr.addSession("s-busy-old", true)
	sw := New(Options{
		Store: st, Manager: mgr,
		MaxIdle: 24 * time.Hour,
		Now:     func() time.Time { return now },
	})
	hard := findSweeper(t, sw, "hard_cutoff")
	if _, err := hard.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(mgr.stops) != 1 {
		t.Errorf("expected stop even when busy, got %v", mgr.stops)
	}
}

func TestIdemCleanup_DeletesStaleRows(t *testing.T) {
	st := newTestStore(t)
	now := time.Now().UTC()
	insertSession(t, st, "s1", "running", now)
	insertIdem(t, st, "s1", "k-old", "msg-old", now.Add(-10*time.Minute))
	insertIdem(t, st, "s1", "k-fresh", "msg-fresh", now.Add(-1*time.Minute))

	sw := New(Options{
		Store:   st,
		IdemTTL: 5 * time.Minute,
		Now:     func() time.Time { return now },
	})
	idem := findSweeper(t, sw, "idem_cleanup")
	n, err := idem.Tick(context.Background())
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if n != 1 {
		t.Errorf("actions=%d want 1", n)
	}
	var remaining int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM message_idempotency`).Scan(&remaining); err != nil {
		t.Fatalf("count: %v", err)
	}
	if remaining != 1 {
		t.Errorf("remaining=%d want 1", remaining)
	}
}

func TestTombstoneReap_RemovesOldDirs(t *testing.T) {
	dir := t.TempDir()
	tomb := filepath.Join(dir, ".tombstones")
	if err := os.MkdirAll(tomb, 0o700); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(tomb, "s-old")
	fresh := filepath.Join(tomb, "s-fresh")
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fresh, 0o700); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-10 * 24 * time.Hour)
	if err := os.Chtimes(old, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	sw := New(Options{
		SessionsDir:  dir,
		TombstoneAge: 7 * 24 * time.Hour,
		Now:          func() time.Time { return now },
	})
	reap := findSweeper(t, sw, "tombstone_reap")
	n, err := reap.Tick(context.Background())
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if n != 1 {
		t.Errorf("actions=%d want 1", n)
	}
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected old removed, err=%v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("expected fresh kept: %v", err)
	}
}

func TestTombstoneReap_NoDir(t *testing.T) {
	dir := t.TempDir()
	sw := New(Options{
		SessionsDir:  dir,
		TombstoneAge: 7 * 24 * time.Hour,
	})
	reap := findSweeper(t, sw, "tombstone_reap")
	n, err := reap.Tick(context.Background())
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if n != 0 {
		t.Errorf("actions=%d want 0", n)
	}
}

func TestRunAll_StopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	st := newTestStore(t)
	mgr := newFakeManager()
	sw := New(Options{
		Store:        st,
		Manager:      mgr,
		IdleInterval: 10 * time.Millisecond,
		HardInterval: 10 * time.Millisecond,
		IdemInterval: 10 * time.Millisecond,
		ReapInterval: 10 * time.Millisecond,
	})
	RunAll(ctx, sw, nil)
	time.Sleep(30 * time.Millisecond)
	cancel()
	time.Sleep(20 * time.Millisecond)
}

func findSweeper(t *testing.T, sweepers []Sweeper, name string) Sweeper {
	t.Helper()
	for _, s := range sweepers {
		if s.Name() == name {
			return s
		}
	}
	t.Fatalf("sweeper %q not in %v", name, sweepers)
	return nil
}

func stoppedIDs(calls []stopCall) []string {
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, c.sessionID)
	}
	sort.Strings(out)
	return out
}

func equalStrSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ax := append([]string(nil), a...)
	bx := append([]string(nil), b...)
	sort.Strings(ax)
	sort.Strings(bx)
	for i := range ax {
		if ax[i] != bx[i] {
			return false
		}
	}
	return true
}

func insertIdem(t *testing.T, st *store.Store, sessionID, key, msgID string, at time.Time) {
	t.Helper()
	if _, err := st.DB().Exec(
		`INSERT INTO message_idempotency (session_id, idempotency_key, message_id, accepted_at) VALUES (?, ?, ?, ?)`,
		sessionID, key, msgID, at.UTC().Format(time.RFC3339Nano),
	); err != nil {
		t.Fatalf("insert idem: %v", err)
	}
}
