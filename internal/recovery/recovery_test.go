package recovery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/agentctl/agentctl/internal/store"
)

type fakeContainers struct {
	mu          sync.Mutex
	containers  map[string]ContainerRef
	networks    map[string]NetworkRef
	adoptErrs   map[string]error
	listErr     error
	netListErr  error
	stopCalls   []string
	removeCalls []string
	netRmCalls  []string
	adoptCalls  []string
}

func newFakeContainers() *fakeContainers {
	return &fakeContainers{
		containers: map[string]ContainerRef{},
		networks:   map[string]NetworkRef{},
		adoptErrs:  map[string]error{},
	}
}

func (f *fakeContainers) addContainer(c ContainerRef) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.containers[c.ID] = c
}

func (f *fakeContainers) addNetwork(n NetworkRef) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.networks[n.ID] = n
}

func (f *fakeContainers) List(_ context.Context, _ string) ([]ContainerRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]ContainerRef, 0, len(f.containers))
	for _, c := range f.containers {
		out = append(out, c)
	}
	return out, nil
}

func (f *fakeContainers) Inspect(_ context.Context, id string) (Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[id]
	if !ok {
		return Status{}, errors.New("not found")
	}
	state := c.State
	if state == "" {
		if c.Running {
			state = "running"
		} else {
			state = "exited"
		}
	}
	return Status{State: state, Running: c.Running}, nil
}

func (f *fakeContainers) Stop(_ context.Context, id string, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopCalls = append(f.stopCalls, id)
	if c, ok := f.containers[id]; ok {
		c.Running = false
		c.State = "exited"
		f.containers[id] = c
	}
	return nil
}

func (f *fakeContainers) Remove(_ context.Context, id string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removeCalls = append(f.removeCalls, id)
	delete(f.containers, id)
	return nil
}

func (f *fakeContainers) NetworkList(_ context.Context, _ string) ([]NetworkRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.netListErr != nil {
		return nil, f.netListErr
	}
	out := make([]NetworkRef, 0, len(f.networks))
	for _, n := range f.networks {
		out = append(out, n)
	}
	return out, nil
}

func (f *fakeContainers) NetworkRemove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.netRmCalls = append(f.netRmCalls, id)
	delete(f.networks, id)
	return nil
}

func (f *fakeContainers) Adopt(_ context.Context, sessionID, _ string, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.adoptCalls = append(f.adoptCalls, sessionID)
	return f.adoptErrs[sessionID]
}

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(store.Options{Path: filepath.Join(dir, "agentd.db")})
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func insertSession(t *testing.T, st *store.Store, id, status, containerID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var ctr any
	if containerID != "" {
		ctr = containerID
	}
	if _, err := st.DB().Exec(`INSERT INTO sessions
        (id, name, status, created_at, last_activity_at,
         container_id, image_id, control_sock_path, skills_snapshot_hash,
         model, mem_limit_bytes, cpu_limit_cores, mcp_set_json, repos_json, session_token)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, "n-"+id, status, now, now,
		ctr, "sha256:img", "/tmp/sock", "",
		"claude-sonnet-4-6", int64(4<<30), 2.0, "[]", "[]", "tok-"+id,
	); err != nil {
		t.Fatalf("insert: %v", err)
	}
}

func sessionStatus(t *testing.T, st *store.Store, id string) (string, string, string) {
	t.Helper()
	var status, lastErr, ctr string
	var ctrN, errN *string
	row := st.DB().QueryRow(`SELECT status, container_id, last_error FROM sessions WHERE id=?`, id)
	if err := row.Scan(&status, &ctrN, &errN); err != nil {
		t.Fatalf("scan %s: %v", id, err)
	}
	if ctrN != nil {
		ctr = *ctrN
	}
	if errN != nil {
		lastErr = *errN
	}
	return status, ctr, lastErr
}

func TestReconcile_StartingRow(t *testing.T) {
	cases := []struct {
		name             string
		container        *ContainerRef
		expectAborted    int
		expectRemoveCall bool
	}{
		{
			name:             "container_present",
			container:        &ContainerRef{ID: "c1", SessionID: "s1", Running: false, State: "created"},
			expectAborted:    1,
			expectRemoveCall: true,
		},
		{
			name:             "container_running",
			container:        &ContainerRef{ID: "c1", SessionID: "s1", Running: true, State: "running"},
			expectAborted:    1,
			expectRemoveCall: true,
		},
		{
			name:             "no_container",
			container:        nil,
			expectAborted:    1,
			expectRemoveCall: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newTestStore(t)
			fc := newFakeContainers()
			ctrID := ""
			if tc.container != nil {
				fc.addContainer(*tc.container)
				ctrID = tc.container.ID
			}
			insertSession(t, st, "s1", "starting", ctrID)
			rep, err := Reconcile(context.Background(), Options{
				Store:       st,
				Containers:  fc,
				SessionsDir: t.TempDir(),
			})
			if err != nil {
				t.Fatalf("reconcile: %v", err)
			}
			if rep.AbortedStarts != tc.expectAborted {
				t.Errorf("AbortedStarts=%d want %d", rep.AbortedStarts, tc.expectAborted)
			}
			st2, ctr, errStr := sessionStatus(t, st, "s1")
			if st2 != "stopped" {
				t.Errorf("status=%q want stopped", st2)
			}
			if ctr != "" {
				t.Errorf("container_id=%q want empty", ctr)
			}
			if errStr == "" {
				t.Errorf("expected last_error set")
			}
			if tc.expectRemoveCall && len(fc.removeCalls) == 0 {
				t.Errorf("expected docker rm call")
			}
		})
	}
}

func TestReconcile_RunningRow(t *testing.T) {
	cases := []struct {
		name           string
		container      *ContainerRef
		adoptErr       error
		wantStatus     string
		wantAdopted    int
		wantStoppedDty int
		wantLastErr    string
	}{
		{
			name:        "adopt_ok",
			container:   &ContainerRef{ID: "c1", SessionID: "s1", Running: true, State: "running"},
			wantStatus:  "running",
			wantAdopted: 1,
		},
		{
			name:           "adopt_fails",
			container:      &ContainerRef{ID: "c1", SessionID: "s1", Running: true, State: "running"},
			adoptErr:       errors.New("ping timeout"),
			wantStatus:     "stopped",
			wantStoppedDty: 1,
			wantLastErr:    "adopt_failed_at_recovery",
		},
		{
			name:           "container_exited",
			container:      &ContainerRef{ID: "c1", SessionID: "s1", Running: false, State: "exited"},
			wantStatus:     "stopped",
			wantStoppedDty: 1,
			wantLastErr:    "container_exited_at_recovery",
		},
		{
			name:           "container_missing",
			container:      nil,
			wantStatus:     "stopped",
			wantStoppedDty: 1,
			wantLastErr:    "container_missing_at_recovery",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newTestStore(t)
			fc := newFakeContainers()
			ctrID := ""
			if tc.container != nil {
				fc.addContainer(*tc.container)
				ctrID = tc.container.ID
			}
			if tc.adoptErr != nil {
				fc.adoptErrs["s1"] = tc.adoptErr
			}
			insertSession(t, st, "s1", "running", ctrID)
			rep, err := Reconcile(context.Background(), Options{
				Store:       st,
				Containers:  fc,
				SessionsDir: t.TempDir(),
			})
			if err != nil {
				t.Fatalf("reconcile: %v", err)
			}
			if rep.Adopted != tc.wantAdopted {
				t.Errorf("Adopted=%d want %d", rep.Adopted, tc.wantAdopted)
			}
			if rep.StoppedDirty != tc.wantStoppedDty {
				t.Errorf("StoppedDirty=%d want %d", rep.StoppedDirty, tc.wantStoppedDty)
			}
			st2, _, errStr := sessionStatus(t, st, "s1")
			if st2 != tc.wantStatus {
				t.Errorf("status=%q want %q", st2, tc.wantStatus)
			}
			if tc.wantLastErr != "" && errStr != tc.wantLastErr {
				t.Errorf("last_error=%q want %q", errStr, tc.wantLastErr)
			}
		})
	}
}

func TestReconcile_StoppedRow_NoContainer(t *testing.T) {
	st := newTestStore(t)
	fc := newFakeContainers()
	insertSession(t, st, "s1", "stopped", "")
	rep, err := Reconcile(context.Background(), Options{
		Store:       st,
		Containers:  fc,
		SessionsDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if rep.Adopted != 0 || rep.StoppedDirty != 0 {
		t.Errorf("expected no-op, got %+v", rep)
	}
	status, _, _ := sessionStatus(t, st, "s1")
	if status != "stopped" {
		t.Errorf("status=%q want stopped", status)
	}
}

func TestReconcile_StoppedRow_RunningContainerReadopted(t *testing.T) {
	st := newTestStore(t)
	fc := newFakeContainers()
	fc.addContainer(ContainerRef{ID: "c1", SessionID: "s1", Running: true, State: "running"})
	insertSession(t, st, "s1", "stopped", "c1")
	rep, err := Reconcile(context.Background(), Options{
		Store:       st,
		Containers:  fc,
		SessionsDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if rep.Adopted != 1 {
		t.Errorf("Adopted=%d want 1", rep.Adopted)
	}
	status, _, _ := sessionStatus(t, st, "s1")
	if status != "running" {
		t.Errorf("status=%q want running", status)
	}
}

func TestReconcile_StoppedRow_ExitedContainerCleaned(t *testing.T) {
	st := newTestStore(t)
	fc := newFakeContainers()
	fc.addContainer(ContainerRef{ID: "c1", SessionID: "s1", Running: false, State: "exited"})
	insertSession(t, st, "s1", "stopped", "c1")
	_, err := Reconcile(context.Background(), Options{
		Store:       st,
		Containers:  fc,
		SessionsDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(fc.removeCalls) != 1 || fc.removeCalls[0] != "c1" {
		t.Errorf("expected rm of exited container, got %v", fc.removeCalls)
	}
}

func TestReconcile_OrphanContainer(t *testing.T) {
	st := newTestStore(t)
	fc := newFakeContainers()
	fc.addContainer(ContainerRef{ID: "c-orphan", SessionID: "s-gone", Running: true})
	rep, err := Reconcile(context.Background(), Options{
		Store:       st,
		Containers:  fc,
		SessionsDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if rep.OrphanContainers != 1 {
		t.Errorf("OrphanContainers=%d want 1", rep.OrphanContainers)
	}
	if len(fc.removeCalls) != 1 || fc.removeCalls[0] != "c-orphan" {
		t.Errorf("expected orphan rm, got %v", fc.removeCalls)
	}
}

func TestReconcile_OrphanContainer_TerminatedRowExcluded(t *testing.T) {
	st := newTestStore(t)
	fc := newFakeContainers()
	fc.addContainer(ContainerRef{ID: "c-orphan", SessionID: "s-term", Running: false})
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := st.DB().Exec(`INSERT INTO sessions
        (id, name, status, created_at, last_activity_at, image_id, skills_snapshot_hash,
         model, mem_limit_bytes, cpu_limit_cores, mcp_set_json, repos_json, session_token)
        VALUES (?, ?, 'terminated', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"s-term", "n", now, now, "sha256:img", "",
		"claude-sonnet-4-6", int64(4<<30), 2.0, "[]", "[]", "tok",
	); err != nil {
		t.Fatalf("insert: %v", err)
	}
	rep, err := Reconcile(context.Background(), Options{
		Store:       st,
		Containers:  fc,
		SessionsDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if rep.OrphanContainers != 1 {
		t.Errorf("OrphanContainers=%d want 1", rep.OrphanContainers)
	}
}

func TestReconcile_OrphanNetwork(t *testing.T) {
	st := newTestStore(t)
	fc := newFakeContainers()
	fc.addNetwork(NetworkRef{ID: "n1", Name: "agentctl-s-gone", SessionID: "s-gone"})
	rep, err := Reconcile(context.Background(), Options{
		Store:       st,
		Containers:  fc,
		SessionsDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if rep.OrphanNetworks != 1 {
		t.Errorf("OrphanNetworks=%d want 1", rep.OrphanNetworks)
	}
	if len(fc.netRmCalls) != 1 {
		t.Errorf("expected netRm, got %v", fc.netRmCalls)
	}
}

func TestReconcile_OrphanNetwork_KeepsKnown(t *testing.T) {
	st := newTestStore(t)
	fc := newFakeContainers()
	fc.addNetwork(NetworkRef{ID: "n1", Name: "agentctl-s1", SessionID: "s1"})
	insertSession(t, st, "s1", "running", "")
	_, err := Reconcile(context.Background(), Options{
		Store:       st,
		Containers:  fc,
		SessionsDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(fc.netRmCalls) != 0 {
		t.Errorf("did not expect network rm for known session, got %v", fc.netRmCalls)
	}
}

func TestReconcile_OrphanDirsMoved(t *testing.T) {
	st := newTestStore(t)
	fc := newFakeContainers()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "s-known"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "s-orphan"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "s-orphan", "volume"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "s-orphan", "volume", "data.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	insertSession(t, st, "s-known", "running", "")
	fc.addContainer(ContainerRef{ID: "c-known", SessionID: "s-known", Running: true})
	rep, err := Reconcile(context.Background(), Options{
		Store:       st,
		Containers:  fc,
		SessionsDir: dir,
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if rep.OrphanDirs != 1 {
		t.Errorf("OrphanDirs=%d want 1", rep.OrphanDirs)
	}
	if _, err := os.Stat(filepath.Join(dir, "s-orphan")); !os.IsNotExist(err) {
		t.Errorf("s-orphan still present at original path: %v", err)
	}
	orphans, err := os.ReadDir(filepath.Join(dir, ".orphans"))
	if err != nil {
		t.Fatalf("orphans dir: %v", err)
	}
	if len(orphans) != 1 {
		t.Errorf("orphans count=%d", len(orphans))
	}
	moved := filepath.Join(dir, ".orphans", orphans[0].Name(), "volume", "data.txt")
	if data, err := os.ReadFile(moved); err != nil || string(data) != "hi" {
		t.Errorf("moved data missing or corrupt: data=%q err=%v", data, err)
	}
}

func TestReconcile_StartingPlusContainerCreatedNotRunning(t *testing.T) {
	st := newTestStore(t)
	fc := newFakeContainers()
	fc.addContainer(ContainerRef{ID: "c-half", SessionID: "s-race", Running: false, State: "created"})
	insertSession(t, st, "s-race", "starting", "c-half")
	rep, err := Reconcile(context.Background(), Options{
		Store:       st,
		Containers:  fc,
		SessionsDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if rep.AbortedStarts != 1 {
		t.Errorf("AbortedStarts=%d want 1", rep.AbortedStarts)
	}
	if len(fc.removeCalls) != 1 {
		t.Errorf("expected the half-created container removed, got %v", fc.removeCalls)
	}
	status, _, _ := sessionStatus(t, st, "s-race")
	if status != "stopped" {
		t.Errorf("status=%q want stopped", status)
	}
}

func TestReconcile_StateTransitionWritesBeforeDocker(t *testing.T) {
	st := newTestStore(t)
	fc := newFakeContainers()
	fc.addContainer(ContainerRef{ID: "c1", SessionID: "s1", Running: true})
	fc.adoptErrs["s1"] = errors.New("simulate adopt fail")
	insertSession(t, st, "s1", "running", "c1")
	if _, err := Reconcile(context.Background(), Options{
		Store:       st,
		Containers:  fc,
		SessionsDir: t.TempDir(),
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	st2, _, errStr := sessionStatus(t, st, "s1")
	if st2 != "stopped" {
		t.Errorf("status=%q want stopped", st2)
	}
	if errStr == "" {
		t.Errorf("expected last_error set")
	}
}
