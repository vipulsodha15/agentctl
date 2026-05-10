package service

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWriteIfChanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	wrote, err := writeIfChanged(path, []byte("a"), 0o644)
	if err != nil {
		t.Fatalf("write1: %v", err)
	}
	if !wrote {
		t.Errorf("expected wrote=true on first call")
	}
	wrote, err = writeIfChanged(path, []byte("a"), 0o644)
	if err != nil {
		t.Fatalf("write2: %v", err)
	}
	if wrote {
		t.Errorf("expected wrote=false when content unchanged")
	}
	wrote, err = writeIfChanged(path, []byte("b"), 0o644)
	if err != nil {
		t.Fatalf("write3: %v", err)
	}
	if !wrote {
		t.Errorf("expected wrote=true when content changed")
	}
}

func TestUnitTemplateRender(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("platform-specific")
	}
	dir := t.TempDir()
	mgr := New(dir)
	if mgr.UnitPath() == "" {
		t.Fatalf("unit path empty")
	}
	parent := filepath.Dir(mgr.UnitPath())
	_ = os.MkdirAll(parent, 0o755)
	t.Logf("unit path: %s", mgr.UnitPath())
	if !strings.Contains(mgr.UnitPath(), dir) {
		t.Errorf("unit path %q does not include home %q", mgr.UnitPath(), dir)
	}
}

// TestUnitInvokesAgentdSubcommand guards against a regression where the unit
// file launches the binary by its real name (which routes to the CLI dispatcher)
// instead of the daemon. The unit must always pass `agentd` so main() picks
// daemonMain regardless of how os.Executable() resolves symlinks.
func TestUnitInvokesAgentdSubcommand(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("platform-specific")
	}
	dir := t.TempDir()
	mgr := New(dir)
	parent := filepath.Dir(mgr.UnitPath())
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	binPath := filepath.Join(dir, "bin", "agentctl")
	if _, err := mgr.Install(InstallOptions{BinaryPath: binPath, Home: dir}); err != nil {
		// Install may fail because systemctl/launchctl aren't reachable in
		// the test sandbox; we still want to assert on the rendered file.
		if _, statErr := os.Stat(mgr.UnitPath()); statErr != nil {
			t.Skipf("install failed and unit not written: %v", err)
		}
	}
	body, err := os.ReadFile(mgr.UnitPath())
	if err != nil {
		t.Fatalf("read unit: %v", err)
	}
	got := string(body)
	switch runtime.GOOS {
	case "linux":
		want := "ExecStart=" + binPath + " agentd"
		if !strings.Contains(got, want) {
			t.Errorf("unit missing %q\n--- got ---\n%s", want, got)
		}
	case "darwin":
		if !strings.Contains(got, "<string>"+binPath+"</string>") {
			t.Errorf("plist missing binary path %q\n--- got ---\n%s", binPath, got)
		}
		if !strings.Contains(got, "<string>agentd</string>") {
			t.Errorf("plist missing the agentd subcommand arg\n--- got ---\n%s", got)
		}
	}
}
