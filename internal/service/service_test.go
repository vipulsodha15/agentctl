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
