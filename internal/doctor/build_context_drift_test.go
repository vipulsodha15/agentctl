package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeMeta drops an install_metadata.json under home with the given
// source_url. Mirrors what install.sh / `agentctl init` lay down.
func writeMeta(t *testing.T, home, sourceURL string) {
	t.Helper()
	dir := filepath.Join(home, ".local", "share", "agentctl")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := map[string]any{
		"version":      "0.1.0",
		"source_url":   sourceURL,
		"installed_at": "2026-05-17T04:00:00Z",
	}
	data, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "install_metadata.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// seedShim drops a minimal shim tree (a couple of files in nested
// dirs) under <root>/image/shim/. Returns the shim dir path.
func seedShim(t *testing.T, root string, files map[string]string) string {
	t.Helper()
	shim := filepath.Join(root, "image", "shim")
	for rel, content := range files {
		p := filepath.Join(shim, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return shim
}

func TestCheckBuildContextDriftOKWhenSynced(t *testing.T) {
	home := t.TempDir()
	source := t.TempDir()
	files := map[string]string{
		"__main__.py":              "main",
		"runtime/codex_driver.py":  "codex",
		"runtime/claude_driver.py": "claude",
	}
	seedShim(t, source, files)
	// Copy the same content into the build-context location.
	seedShim(t, filepath.Join(home, ".local", "share", "agentctl"), files)
	writeMeta(t, home, source)

	c := checkBuildContextDrift(home)
	if c.Status != StatusOK {
		t.Fatalf("want ok, got %s: %s / %s", c.Status, c.Message, c.Detail)
	}
}

func TestCheckBuildContextDriftWarnsOnDivergence(t *testing.T) {
	home := t.TempDir()
	source := t.TempDir()
	seedShim(t, source, map[string]string{
		"runtime/codex_driver.py":  "new-fixed-shim",
		"runtime/claude_driver.py": "same",
	})
	// Build context lags behind: codex_driver differs, plus an extra
	// stale file the source doesn't carry.
	seedShim(t, filepath.Join(home, ".local", "share", "agentctl"), map[string]string{
		"runtime/codex_driver.py":  "old-broken-shim",
		"runtime/claude_driver.py": "same",
		"runtime/stale_extra.py":   "stale",
	})
	writeMeta(t, home, source)

	c := checkBuildContextDrift(home)
	if c.Status != StatusWarn {
		t.Fatalf("want warn on divergence, got %s: %s", c.Status, c.Message)
	}
	if !strings.Contains(c.Detail, "codex_driver.py (different)") {
		t.Errorf("expected codex_driver.py to be flagged different; detail=%q", c.Detail)
	}
	if !strings.Contains(c.Detail, "stale_extra.py (extra in build context)") {
		t.Errorf("expected stale_extra.py to be flagged extra; detail=%q", c.Detail)
	}
	if !strings.Contains(c.Message, "agentctl doctor --fix") {
		t.Errorf("message should point at the fix command; got %q", c.Message)
	}
}

func TestCheckBuildContextDriftSkipsWithoutSourceURL(t *testing.T) {
	home := t.TempDir()
	// install_metadata.json present but no source_url — e.g. a tarball
	// install. We have nothing to compare against; skip silently.
	dir := filepath.Join(home, ".local", "share", "agentctl")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "install_metadata.json"),
		[]byte(`{"version":"0.1.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	c := checkBuildContextDrift(home)
	if c.Status != StatusSkip {
		t.Fatalf("want skip when source_url missing, got %s: %s", c.Status, c.Message)
	}
}

func TestCheckBuildContextDriftIgnoresPycache(t *testing.T) {
	// A __pycache__ directory in the build context (left over from a
	// local test run) should NOT trip the drift check.
	home := t.TempDir()
	source := t.TempDir()
	files := map[string]string{"runtime/codex_driver.py": "x"}
	seedShim(t, source, files)
	dstShim := seedShim(t, filepath.Join(home, ".local", "share", "agentctl"), files)
	// Drop a pycache artifact only in the build context.
	pyc := filepath.Join(dstShim, "runtime", "__pycache__", "codex_driver.cpython-313.pyc")
	if err := os.MkdirAll(filepath.Dir(pyc), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pyc, []byte("compiled"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeMeta(t, home, source)

	c := checkBuildContextDrift(home)
	if c.Status != StatusOK {
		t.Fatalf("want ok (pycache should be ignored), got %s: %s", c.Status, c.Detail)
	}
}
