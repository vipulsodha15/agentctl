package doctor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/agentctl/agentctl/internal/config"
)

func writeConfigWithPin(t *testing.T, dir, pinned string) string {
	t.Helper()
	cfg := config.Default()
	cfg.Image.PinnedID = pinned
	path := filepath.Join(dir, "config.toml")
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCheckImagePresentNoPinFails(t *testing.T) {
	dir := t.TempDir()
	path := writeConfigWithPin(t, dir, "")
	c := checkImagePresent(path)
	if c.Status != StatusFail {
		t.Errorf("expected fail when pinned id empty, got %s", c.Status)
	}
}

func TestCheckImagePresentMissingConfigFile(t *testing.T) {
	c := checkImagePresent(filepath.Join(t.TempDir(), "absent.toml"))
	if c.Status != StatusFail {
		t.Errorf("expected fail for missing config, got %s", c.Status)
	}
}

func TestCheckImagePresentDockerAbsentWarns(t *testing.T) {
	dir := t.TempDir()
	path := writeConfigWithPin(t, dir, "sha256:abcd")
	originalPath := os.Getenv("PATH")
	t.Cleanup(func() { _ = os.Setenv("PATH", originalPath) })
	_ = os.Setenv("PATH", "/this/should/not/contain/docker")
	c := checkImagePresent(path)
	if c.Status != StatusWarn {
		t.Errorf("expected warn when docker absent, got %s: %s", c.Status, c.Message)
	}
}
