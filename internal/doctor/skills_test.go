package doctor

import (
	"os"
	"path/filepath"
	"testing"
)

func writeManifest(t *testing.T, dir, name, desc string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "{\n  \"name\": \"" + name + "\",\n  \"description\": \"" + desc + "\"\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCheckSkillsBuiltinPasses(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, filepath.Join(dir, "refactor"), "refactor", "Refactor things.")
	writeManifest(t, filepath.Join(dir, "tests"), "tests", "Generate tests.")
	c := checkSkillsBuiltin(dir)
	if c.Status != StatusOK {
		t.Errorf("expected ok, got %s: %s / %s", c.Status, c.Message, c.Detail)
	}
}

func TestCheckSkillsBuiltinReportsMissing(t *testing.T) {
	c := checkSkillsBuiltin(filepath.Join(t.TempDir(), "absent"))
	if c.Status != StatusFail {
		t.Errorf("expected fail for missing dir, got %s", c.Status)
	}
}

func TestCheckSkillsBuiltinFlagsBadManifest(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "broken")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	c := checkSkillsBuiltin(dir)
	if c.Status != StatusFail {
		t.Errorf("expected fail when skill has no manifest, got %s", c.Status)
	}
}

func TestCheckSkillsCustomReportsOverrides(t *testing.T) {
	root := t.TempDir()
	builtin := filepath.Join(root, "builtin-skills")
	custom := filepath.Join(root, "custom-skills")
	writeManifest(t, filepath.Join(builtin, "shared"), "shared", "built-in.")
	writeManifest(t, filepath.Join(custom, "shared"), "shared", "user override.")
	writeManifest(t, filepath.Join(custom, "extra"), "extra", "user-only.")
	c := checkSkillsCustom(custom, builtin)
	if c.Status != StatusOK {
		t.Fatalf("expected ok, got %s: %s", c.Status, c.Message)
	}
	if !contains(c.Message, "override") {
		t.Errorf("expected override notice in message; got %q", c.Message)
	}
}

func TestCheckSkillsCustomMissingDirIsOK(t *testing.T) {
	c := checkSkillsCustom(filepath.Join(t.TempDir(), "none"), "")
	if c.Status != StatusOK {
		t.Errorf("expected ok when dir absent, got %s", c.Status)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
