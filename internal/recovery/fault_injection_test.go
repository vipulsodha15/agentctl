package recovery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFault_CrashDuringManagerCreate_RowOnlyNoContainer(t *testing.T) {
	st := newTestStore(t)
	fc := newFakeContainers()
	insertSession(t, st, "s-crash", "starting", "")
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
	status, ctr, lastErr := sessionStatus(t, st, "s-crash")
	if status != "stopped" {
		t.Errorf("status=%q want stopped", status)
	}
	if ctr != "" {
		t.Errorf("container_id=%q want empty", ctr)
	}
	if lastErr == "" {
		t.Errorf("expected last_error set")
	}
	if len(fc.removeCalls) != 0 {
		t.Errorf("no container existed; rm should not run, got %v", fc.removeCalls)
	}
}

func TestFault_CrashDuringContainerCreate_ContainerExistsRowStarting(t *testing.T) {
	st := newTestStore(t)
	fc := newFakeContainers()
	fc.addContainer(ContainerRef{ID: "c-orphaned", SessionID: "s-half", Running: false, State: "created"})
	insertSession(t, st, "s-half", "starting", "")
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
	if len(fc.removeCalls) != 1 || fc.removeCalls[0] != "c-orphaned" {
		t.Errorf("expected half-created container removed, got %v", fc.removeCalls)
	}
	status, _, _ := sessionStatus(t, st, "s-half")
	if status != "stopped" {
		t.Errorf("status=%q want stopped", status)
	}
}

func TestFault_CrashDuringStop_ContainerExitedRowRunning(t *testing.T) {
	st := newTestStore(t)
	fc := newFakeContainers()
	fc.addContainer(ContainerRef{ID: "c-exited", SessionID: "s-stop", Running: false, State: "exited"})
	insertSession(t, st, "s-stop", "running", "c-exited")
	rep, err := Reconcile(context.Background(), Options{
		Store:       st,
		Containers:  fc,
		SessionsDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if rep.StoppedDirty != 1 {
		t.Errorf("StoppedDirty=%d want 1", rep.StoppedDirty)
	}
	status, _, lastErr := sessionStatus(t, st, "s-stop")
	if status != "stopped" {
		t.Errorf("status=%q want stopped", status)
	}
	if lastErr != "container_exited_at_recovery" {
		t.Errorf("last_error=%q want container_exited_at_recovery", lastErr)
	}
	if len(fc.removeCalls) != 1 || fc.removeCalls[0] != "c-exited" {
		t.Errorf("expected exited container removed, got %v", fc.removeCalls)
	}
}

func TestFault_CrashDuringTerminate_TombstoneNotCleaned(t *testing.T) {
	st := newTestStore(t)
	fc := newFakeContainers()
	dir := t.TempDir()
	leftover := filepath.Join(dir, "s-term")
	if err := os.MkdirAll(leftover, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(leftover, "marker"), []byte("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := st.DB().Exec(`INSERT INTO sessions
        (id, name, status, created_at, last_activity_at, terminated_at, image_id, skills_snapshot_hash,
         model, mem_limit_bytes, cpu_limit_cores, mcp_set_json, repos_json, session_token)
        VALUES (?, ?, 'terminated', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"s-term", "n", now, now, now, "sha256:img", "",
		"claude-sonnet-4-6", int64(4<<30), 2.0, "[]", "[]", "tok",
	); err != nil {
		t.Fatalf("insert: %v", err)
	}
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
	if _, err := os.Stat(leftover); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected terminated session dir moved, err=%v", err)
	}
	orphans, err := os.ReadDir(filepath.Join(dir, ".orphans"))
	if err != nil {
		t.Fatalf("orphans: %v", err)
	}
	if len(orphans) != 1 {
		t.Errorf("orphans count=%d want 1", len(orphans))
	}
}

func TestFault_TableDriven_StateTransitionMatrix(t *testing.T) {
	type fixture struct {
		name        string
		rowStatus   string
		container   *ContainerRef
		wantStatus  string
		wantAdopted int
		wantStopped int
		wantAborted int
		wantOrphan  int
	}
	cases := []fixture{
		{
			name:        "starting+no_container",
			rowStatus:   "starting",
			container:   nil,
			wantStatus:  "stopped",
			wantAborted: 1,
		},
		{
			name:        "starting+created",
			rowStatus:   "starting",
			container:   &ContainerRef{ID: "c", SessionID: "s", State: "created"},
			wantStatus:  "stopped",
			wantAborted: 1,
		},
		{
			name:        "starting+running",
			rowStatus:   "starting",
			container:   &ContainerRef{ID: "c", SessionID: "s", Running: true, State: "running"},
			wantStatus:  "stopped",
			wantAborted: 1,
		},
		{
			name:        "running+running_adopt",
			rowStatus:   "running",
			container:   &ContainerRef{ID: "c", SessionID: "s", Running: true, State: "running"},
			wantStatus:  "running",
			wantAdopted: 1,
		},
		{
			name:        "running+exited",
			rowStatus:   "running",
			container:   &ContainerRef{ID: "c", SessionID: "s", Running: false, State: "exited"},
			wantStatus:  "stopped",
			wantStopped: 1,
		},
		{
			name:        "running+missing",
			rowStatus:   "running",
			container:   nil,
			wantStatus:  "stopped",
			wantStopped: 1,
		},
		{
			name:        "stopped+running_unexpected",
			rowStatus:   "stopped",
			container:   &ContainerRef{ID: "c", SessionID: "s", Running: true, State: "running"},
			wantStatus:  "running",
			wantAdopted: 1,
		},
		{
			name:       "stopped+exited",
			rowStatus:  "stopped",
			container:  &ContainerRef{ID: "c", SessionID: "s", Running: false, State: "exited"},
			wantStatus: "stopped",
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
			insertSession(t, st, "s", tc.rowStatus, ctrID)
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
			if rep.StoppedDirty != tc.wantStopped {
				t.Errorf("StoppedDirty=%d want %d", rep.StoppedDirty, tc.wantStopped)
			}
			if rep.AbortedStarts != tc.wantAborted {
				t.Errorf("AbortedStarts=%d want %d", rep.AbortedStarts, tc.wantAborted)
			}
			if rep.OrphanContainers != tc.wantOrphan {
				t.Errorf("OrphanContainers=%d want %d", rep.OrphanContainers, tc.wantOrphan)
			}
			status, _, _ := sessionStatus(t, st, "s")
			if status != tc.wantStatus {
				t.Errorf("status=%q want %q", status, tc.wantStatus)
			}
		})
	}
}
