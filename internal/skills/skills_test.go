package skills

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeSkill(t *testing.T, dir, name, desc string) {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(full, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"name":"` + name + `","description":"` + desc + `"}`)
	if err := os.WriteFile(filepath.Join(full, "manifest.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func newTestManager(t *testing.T) (Manager, string, string) {
	t.Helper()
	root := t.TempDir()
	builtin := filepath.Join(root, "builtin")
	custom := filepath.Join(root, "custom")
	_ = os.MkdirAll(builtin, 0o755)
	_ = os.MkdirAll(custom, 0o755)
	return NewManager(Options{BuiltinDir: builtin, CustomDir: custom}), builtin, custom
}

func TestListInstalledMergesAndDetectsOverride(t *testing.T) {
	mgr, builtin, custom := newTestManager(t)
	writeSkill(t, builtin, "refactor", "built-in refactor")
	writeSkill(t, builtin, "docs", "built-in docs")
	writeSkill(t, custom, "refactor", "custom refactor")
	writeSkill(t, custom, "x", "custom x")
	list, err := mgr.ListInstalled()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]InstalledSkill{}
	for _, s := range list {
		got[s.Name] = s
	}
	if got["refactor"].Source != SourceCustom || !got["refactor"].Overrides {
		t.Errorf("refactor expected to override built-in, got %+v", got["refactor"])
	}
	if got["docs"].Source != SourceBuiltin {
		t.Errorf("docs source: %+v", got["docs"])
	}
	if got["x"].Source != SourceCustom {
		t.Errorf("x source: %+v", got["x"])
	}
}

func TestImportIdempotentSkipsExisting(t *testing.T) {
	mgr, _, custom := newTestManager(t)
	src := t.TempDir()
	writeSkill(t, src, "foo", "imported foo")
	res, err := mgr.Import(filepath.Join(src, "foo"), "foo", ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Imported) != 1 {
		t.Fatalf("expected import, got %+v", res)
	}
	if _, err := os.Stat(filepath.Join(custom, "foo", "manifest.json")); err != nil {
		t.Fatalf("foo not copied: %v", err)
	}
	res2, err := mgr.Import(filepath.Join(src, "foo"), "foo", ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Imported) != 0 || len(res2.Skipped) != 1 {
		t.Fatalf("expected skip, got %+v", res2)
	}
}

func TestImportShadowsBuiltinOnlyWithForce(t *testing.T) {
	mgr, builtin, _ := newTestManager(t)
	writeSkill(t, builtin, "ours", "built-in ours")
	src := t.TempDir()
	writeSkill(t, src, "ours", "user ours")
	res, _ := mgr.Import(filepath.Join(src, "ours"), "ours", ImportOptions{})
	if len(res.Imported) != 0 {
		t.Fatal("expected no import without --force")
	}
	res, err := mgr.Import(filepath.Join(src, "ours"), "ours", ImportOptions{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Imported) != 1 || len(res.ShadowedBuiltins) != 1 {
		t.Fatalf("expected forced shadow, got %+v", res)
	}
}

func TestRemoveRefusesBuiltin(t *testing.T) {
	mgr, builtin, _ := newTestManager(t)
	writeSkill(t, builtin, "b", "x")
	if err := mgr.Remove("b"); !errors.Is(err, ErrBuiltinReadOnly) {
		t.Fatalf("expected ErrBuiltinReadOnly, got %v", err)
	}
}

func TestValidateRejectsEmptyDescription(t *testing.T) {
	mgr, _, custom := newTestManager(t)
	d := filepath.Join(custom, "z")
	_ = os.MkdirAll(d, 0o755)
	_ = os.WriteFile(filepath.Join(d, "manifest.json"), []byte(`{"name":"z","description":""}`), 0o644)
	res, err := mgr.Validate(ValidateSource{Name: "z", Path: d})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || len(res.Issues) == 0 {
		t.Fatalf("expected validate to fail, got %+v", res)
	}
}

func TestScaffoldAndShow(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	path, err := mgr.Scaffold("newone")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(path, "manifest.json")); err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
	s, err := mgr.Show("newone")
	if err != nil {
		t.Fatal(err)
	}
	if s.Source != SourceCustom {
		t.Errorf("source: %+v", s)
	}
}

func TestImportDirectoryHandlesMultiple(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	src := t.TempDir()
	writeSkill(t, src, "a", "aa")
	writeSkill(t, src, "b", "bb")
	imported, skipped, err := mgr.ImportDirectory(src, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(imported) != 2 {
		t.Errorf("expected 2 imported, got %v (skipped=%v)", imported, skipped)
	}
}

func TestSKILLMDFrontMatterParses(t *testing.T) {
	dir := t.TempDir()
	d := filepath.Join(dir, "x")
	_ = os.MkdirAll(d, 0o755)
	body := "---\nname: x\ndescription: hello\n---\n# x\n"
	_ = os.WriteFile(filepath.Join(d, "SKILL.md"), []byte(body), 0o644)
	mf, err := readManifest(d)
	if err != nil {
		t.Fatal(err)
	}
	if mf.Name != "x" || mf.Description != "hello" {
		t.Fatalf("mf=%+v", mf)
	}
}
