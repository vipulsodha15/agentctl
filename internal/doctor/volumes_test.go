package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mkSession(t *testing.T, sessionsDir, name string, body []byte) {
	t.Helper()
	dir := filepath.Join(sessionsDir, name, "volume")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data.bin"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCheckVolumesDiskCountsSessions(t *testing.T) {
	dir := t.TempDir()
	mkSession(t, dir, "sess_a", make([]byte, 1024))
	mkSession(t, dir, "sess_b", make([]byte, 2048))
	c := checkVolumesDisk(dir)
	if c.Status == StatusFail {
		t.Fatalf("unexpected fail: %s", c.Message)
	}
	if !strings.Contains(c.Message, "2 session") {
		t.Errorf("expected 2 sessions in message; got %q", c.Message)
	}
}

func TestCheckVolumesDiskMissingDirOK(t *testing.T) {
	c := checkVolumesDisk(filepath.Join(t.TempDir(), "absent"))
	if c.Status != StatusOK {
		t.Errorf("expected ok, got %s: %s", c.Status, c.Message)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0B"},
		{500, "500B"},
		{2048, "2.0KB"},
		{int64(1.5 * 1024 * 1024), "1.5MB"},
	}
	for _, c := range cases {
		got := humanBytes(c.in)
		if got != c.want {
			t.Errorf("humanBytes(%d): got %q want %q", c.in, got, c.want)
		}
	}
}

func TestTopNUsage(t *testing.T) {
	in := []sessionUsage{{Name: "a", Bytes: 5}, {Name: "b", Bytes: 100}, {Name: "c", Bytes: 50}}
	out := topNUsage(in, 2)
	if len(out) != 2 || out[0].Name != "b" || out[1].Name != "c" {
		t.Errorf("unexpected: %+v", out)
	}
}
