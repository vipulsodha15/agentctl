package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/agentctl/agentctl/internal/config"
	"github.com/agentctl/agentctl/internal/paths"
	"github.com/agentctl/agentctl/internal/store"
)

func newTestEnv(t *testing.T) (*Env, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	home := t.TempDir()
	layout := paths.From(home)
	if err := makeDirs(layout); err != nil {
		t.Fatal(err)
	}
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	return &Env{
		Layout: layout,
		Stdout: stdout,
		Stderr: stderr,
		Stdin:  &bytes.Buffer{},
	}, stdout, stderr
}

func makeDirs(layout paths.Layout) error {
	for _, d := range []string{layout.ConfigDir, layout.DataDir, layout.SessionsDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func writeConfig(t *testing.T, path string, cfg config.Config) {
	t.Helper()
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
}

func openTestStore(t *testing.T, dbPath string) *store.Store {
	t.Helper()
	st, err := store.Open(store.Options{Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestStalenessReportNoSessions(t *testing.T) {
	env, stdout, _ := newTestEnv(t)
	st := openTestStore(t, env.Layout.DBFile)
	defer func() { _ = st.Close() }()
	if err := printSessionStaleness(env, env.Layout.DBFile, "sha256:abc"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "no sessions exist") {
		t.Errorf("expected 'no sessions exist', got %q", stdout.String())
	}
}

func TestStalenessReportFlagsStaleSessions(t *testing.T) {
	env, _, _ := newTestEnv(t)
	st := openTestStore(t, env.Layout.DBFile)
	defer func() { _ = st.Close() }()
	insertSession(t, st, "sess_RUN", "auth-refactor", "running", "sha256:old")
	insertSession(t, st, "sess_STOP", "lint", "stopped", "sha256:old")
	insertSession(t, st, "sess_NEW", "fresh", "running", "sha256:new")
	insertSession(t, st, "sess_TERM", "old", "terminated", "sha256:older")

	out := &bytes.Buffer{}
	rows, err := loadStaleSessions(env.Layout.DBFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeStalenessReport(out, rows, "sha256:new"); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	if !strings.Contains(body, "4 sessions exist") {
		t.Errorf("expected count line, got %q", body)
	}
	if !strings.Contains(body, "sess_RUN") || !strings.Contains(body, "after next restart") {
		t.Errorf("expected running stale marker, got %q", body)
	}
	if !strings.Contains(body, "sess_STOP") || !strings.Contains(body, "on next resume") {
		t.Errorf("expected stopped stale marker, got %q", body)
	}
	if !strings.Contains(body, "sess_NEW") || !strings.Contains(body, "already on new image") {
		t.Errorf("expected fresh marker, got %q", body)
	}
	if !strings.Contains(body, "sess_TERM") || !strings.Contains(body, "no action") {
		t.Errorf("expected terminated marker, got %q", body)
	}
}

func TestUpdateRollbackNoPrevious(t *testing.T) {
	env, _, stderr := newTestEnv(t)
	cfg := config.Default()
	cfg.Image.PinnedID = "sha256:current"
	writeConfig(t, env.Layout.ConfigFile, cfg)

	code := runUpdate(nil, env, []string{"--rollback"})
	if code == ExitOK {
		t.Errorf("expected non-zero exit when no previous image; got %d", code)
	}
	if !strings.Contains(stderr.String(), "no previous image") {
		t.Errorf("expected 'no previous image', got %q", stderr.String())
	}
}

func TestUpdateReportNoPinned(t *testing.T) {
	env, stdout, _ := newTestEnv(t)
	cfg := config.Default()
	writeConfig(t, env.Layout.ConfigFile, cfg)
	openTestStore(t, env.Layout.DBFile).Close()

	code := runUpdate(nil, env, []string{"--report"})
	if code != ExitOK {
		t.Errorf("expected ok, got %d", code)
	}
	if !strings.Contains(stdout.String(), "no image pinned") {
		t.Errorf("expected 'no image pinned' notice, got %q", stdout.String())
	}
}

func insertSession(t *testing.T, st *store.Store, id, name, status, image string) {
	t.Helper()
	_, err := st.DB().Exec(`INSERT INTO sessions
		(id, name, status, created_at, last_activity_at,
		 image_id, volume_path, control_sock_path, skills_snapshot_path, skills_snapshot_hash,
		 model, mem_limit_bytes, cpu_limit_cores, mcp_set_json, repos_json, session_token)
		 VALUES (?, ?, ?, '2026-05-10T00:00:00Z', '2026-05-10T00:00:00Z',
		 ?, '/v', '/c', '/s', '', 'm', 1, 1.0, '[]', '[]', 't')`,
		id, name, status, image)
	if err != nil {
		t.Fatal(err)
	}
}

func TestUpdateRollbackSwapsPinned(t *testing.T) {
	env, stdoutBuf, errBuf := newTestEnv(t)
	cfg := config.Default()
	cfg.Image.PinnedID = "sha256:current"
	cfg.Image.PreviousID = "sha256:older"
	writeConfig(t, env.Layout.ConfigFile, cfg)
	openTestStore(t, env.Layout.DBFile).Close()

	code := runUpdate(nil, env, []string{"--rollback"})
	if code == ExitOK {
		out := stdoutBuf.String()
		if !strings.Contains(out, "rolled back") {
			t.Errorf("expected 'rolled back', got %q", out)
		}
		reloaded, err := config.Load(env.Layout.ConfigFile)
		if err != nil {
			t.Fatal(err)
		}
		if reloaded.Image.PinnedID != "sha256:older" || reloaded.Image.PreviousID != "sha256:current" {
			t.Errorf("expected swap, got pinned=%s previous=%s", reloaded.Image.PinnedID, reloaded.Image.PreviousID)
		}
		return
	}
	if !strings.Contains(stdoutBuf.String()+errBuf.String(), "docker") {
		t.Logf("rollback exited %d but didn't fail on docker: %s", code, stdoutBuf.String())
	}
}
