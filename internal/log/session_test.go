package log

import (
	"bytes"
	"compress/gzip"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSessionLoggerWritesNDJSON(t *testing.T) {
	dir := t.TempDir()
	sl, err := NewSessionLogger(SessionLogOptions{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sl.Close() }()
	sl.Logger().Info("session.created", slog.String("session_id", "sess_x"))
	body, err := os.ReadFile(filepath.Join(dir, "agentd.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"session.created"`) {
		t.Fatalf("missing msg: %s", body)
	}
	if !strings.Contains(string(body), `"sess_x"`) {
		t.Fatalf("missing session_id: %s", body)
	}
	info, err := os.Stat(filepath.Join(dir, "agentd.log"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != SessionLogPerm {
		t.Fatalf("perm=%v want=%v", perm, os.FileMode(SessionLogPerm))
	}
}

func TestSessionLoggerRotatesOnSize(t *testing.T) {
	dir := t.TempDir()
	sl, err := NewSessionLogger(SessionLogOptions{Dir: dir, MaxBytes: 256, KeepGen: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sl.Close() }()
	for i := 0; i < 30; i++ {
		sl.Logger().Info("rotate.line", slog.Int("i", i), slog.String("filler", strings.Repeat("x", 32)))
	}
	gz := filepath.Join(dir, "agentd.log.1.gz")
	if _, err := os.Stat(gz); err != nil {
		t.Fatalf("expected rotated file %s: %v", gz, err)
	}
	in, err := os.Open(gz)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close() }()
	gr, err := gzip.NewReader(in)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(gr)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(`"rotate.line"`)) {
		t.Fatalf("missing rotate.line in archive")
	}
}

func TestSessionLoggerRotatesDaily(t *testing.T) {
	dir := t.TempDir()
	day := time.Date(2026, 5, 10, 23, 59, 59, 0, time.UTC)
	clock := &clockStub{t: day}
	sl, err := NewSessionLogger(SessionLogOptions{Dir: dir, Now: clock.now})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sl.Close() }()
	sl.Logger().Info("before.midnight")
	clock.t = day.Add(2 * time.Minute)
	sl.Logger().Info("after.midnight")
	if _, err := os.Stat(filepath.Join(dir, "agentd.log.1.gz")); err != nil {
		t.Fatalf("expected rotated archive after day boundary: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "agentd.log"))
	if !strings.Contains(string(body), "after.midnight") {
		t.Fatalf("expected new file to contain after.midnight: %s", body)
	}
	if strings.Contains(string(body), "before.midnight") {
		t.Fatalf("rotated content leaked into new file: %s", body)
	}
}

func TestSessionLoggerKeepsAtMostNGenerations(t *testing.T) {
	dir := t.TempDir()
	sl, err := NewSessionLogger(SessionLogOptions{Dir: dir, MaxBytes: 64, KeepGen: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sl.Close() }()
	for i := 0; i < 10; i++ {
		sl.Logger().Info("burn", slog.String("filler", strings.Repeat("x", 64)))
		_ = sl.Rotate()
	}
	for i := 1; i <= 2; i++ {
		if _, err := os.Stat(filepath.Join(dir, formatGenName(i))); err != nil {
			t.Fatalf("missing gen %d: %v", i, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, formatGenName(3))); err == nil {
		t.Fatalf("gen 3 should have been pruned")
	}
}

func TestSessionLoggerRedactsSecrets(t *testing.T) {
	dir := t.TempDir()
	sl, err := NewSessionLogger(SessionLogOptions{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sl.Close() }()
	RegisterSecret("session-token-abc-xyz")
	defer ClearDynamicSecrets()
	sl.Logger().Info("control.greet", slog.String("session_token", "session-token-abc-xyz"))
	body, _ := os.ReadFile(filepath.Join(dir, "agentd.log"))
	if strings.Contains(string(body), "session-token-abc-xyz") {
		t.Fatalf("expected redaction; got: %s", body)
	}
}

func formatGenName(i int) string {
	return "agentd.log." + itoa(i) + ".gz"
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [12]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}

type clockStub struct{ t time.Time }

func (c *clockStub) now() time.Time { return c.t }
