package sm

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agentctl/agentctl/internal/fan"
	"github.com/agentctl/agentctl/internal/store"
)

func TestRehydrateRestoresSessionsFromStore(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(store.Options{Path: filepath.Join(dir, "agentd.db")})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()
	if err := st.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	sessionsDir := filepath.Join(dir, "sessions")
	seedSession(t, st, sessionsDir, seedSessionInput{
		id:     "sess_01_running",
		name:   "alpha",
		status: "running",
		model:  "claude-sonnet-4-6",
		image:  "sha256:img",
	})
	seedSession(t, st, sessionsDir, seedSessionInput{
		id:     "sess_02_stopped",
		name:   "beta",
		status: "stopped",
		model:  "claude-sonnet-4-6",
		image:  "sha256:img",
	})
	// Terminated sessions must not come back.
	seedSession(t, st, sessionsDir, seedSessionInput{
		id:     "sess_03_terminated",
		name:   "gamma",
		status: "terminated",
		model:  "claude-sonnet-4-6",
		image:  "sha256:img",
	})

	mgr := New(Options{
		Store:        st,
		SessionsDir:  sessionsDir,
		Hub:          fan.NewHub(),
		Control:      newFakeControl(),
		DefaultModel: "claude-sonnet-4-6",
	})
	defer func() { _ = mgr.Shutdown(context.Background()) }()

	if err := mgr.Rehydrate(context.Background()); err != nil {
		t.Fatalf("rehydrate: %v", err)
	}

	got, err := mgr.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("list len = %d, want 2 (terminated must not rehydrate): %+v", len(got), got)
	}
	for _, s := range got {
		if s.Status != "stopped" {
			t.Errorf("session %s status=%q, want stopped (Send will auto-Restart)", s.ID, s.Status)
		}
	}

	// The previously-running row must have been normalized to 'stopped' in
	// the store so a subsequent Send → Restart sees consistent state.
	var dbStatus string
	if err := st.DB().QueryRow(`SELECT status FROM sessions WHERE id=?`, "sess_01_running").Scan(&dbStatus); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if dbStatus != "stopped" {
		t.Errorf("db status for previously running session = %q, want stopped", dbStatus)
	}
}

func TestRehydrateSkipsMissingSessionDir(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(store.Options{Path: filepath.Join(dir, "agentd.db")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = st.Close() }()
	if err := st.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Seed a row without creating the session dir — recovery's orphan sweep
	// or a manual `rm -rf` can leave this state on disk.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = st.DB().Exec(`INSERT INTO sessions
        (id, name, status, created_at, last_activity_at, image_id, skills_snapshot_hash, model,
         mem_limit_bytes, cpu_limit_cores, mcp_set_json, repos_json, session_token)
         VALUES (?, ?, 'stopped', ?, ?, ?, '', ?, ?, ?, ?, ?, ?)`,
		"sess_orphan", "orphan", now, now, "sha256:img", "claude-sonnet-4-6",
		int64(4*1024*1024*1024), 2.0, "[]", "[]", "tok-orphan")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	mgr := New(Options{
		Store:        st,
		SessionsDir:  filepath.Join(dir, "sessions"),
		Hub:          fan.NewHub(),
		Control:      newFakeControl(),
		DefaultModel: "claude-sonnet-4-6",
	})
	defer func() { _ = mgr.Shutdown(context.Background()) }()
	if err := mgr.Rehydrate(context.Background()); err != nil {
		t.Fatalf("rehydrate: %v", err)
	}
	got, _ := mgr.List(context.Background())
	if len(got) != 0 {
		t.Errorf("expected 0 actors for missing session dir, got %d", len(got))
	}
}

type seedSessionInput struct {
	id, name, status, model, image string
}

func seedSession(t *testing.T, st *store.Store, sessionsDir string, in seedSessionInput) {
	t.Helper()
	dir := filepath.Join(sessionsDir, in.id)
	if err := mkdirAll(dir); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := st.DB().Exec(`INSERT INTO sessions
        (id, name, status, created_at, last_activity_at, image_id, skills_snapshot_hash, model,
         mem_limit_bytes, cpu_limit_cores, mcp_set_json, repos_json, session_token, container_id)
         VALUES (?, ?, ?, ?, ?, ?, '', ?, ?, ?, ?, ?, ?, ?)`,
		in.id, in.name, in.status, now, now, in.image, in.model,
		int64(4*1024*1024*1024), 2.0, "[]", "[]", "tok-"+in.id,
		nullableString("cont-"+in.id))
	if err != nil {
		t.Fatalf("insert %s: %v", in.id, err)
	}
}

func nullableString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

func mkdirAll(p string) error {
	return os.MkdirAll(p, 0o700)
}
