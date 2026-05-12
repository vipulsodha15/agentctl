package agentd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentctl/agentctl/internal/skills"
)

func TestSkillsAdapter_AddInline_WithSkillMD(t *testing.T) {
	custom := filepath.Join(t.TempDir(), "custom")
	if err := os.Mkdir(custom, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	a := newSkillsAdapter(skills.NewManager(skills.Options{CustomDir: custom}))

	body := `{"name":"hello","skill_md":"---\nname: hello\ndescription: greets\n---\n\n# hello\n"}`
	out, err := a.Add(context.Background(), "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	var res skills.AddResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if res.Name != "hello" {
		t.Fatalf("name: got %q, want hello", res.Name)
	}
	if _, err := os.Stat(filepath.Join(custom, "hello", skills.SkillMD)); err != nil {
		t.Fatalf("expected SKILL.md on disk: %v", err)
	}
}

func TestSkillsAdapter_AddInline_DescriptionOnlySynthesizesSkillMD(t *testing.T) {
	custom := filepath.Join(t.TempDir(), "custom")
	if err := os.Mkdir(custom, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	a := newSkillsAdapter(skills.NewManager(skills.Options{CustomDir: custom}))

	body := `{"name":"ping","description":"greets the server"}`
	if _, err := a.Add(context.Background(), "application/json", strings.NewReader(body)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(custom, "ping", skills.SkillMD))
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	if !strings.Contains(string(got), "description: greets the server") {
		t.Fatalf("synthesized SKILL.md missing description; got:\n%s", got)
	}
}

func TestSkillsAdapter_AddInline_RejectsMissingName(t *testing.T) {
	a := newSkillsAdapter(skills.NewManager(skills.Options{CustomDir: t.TempDir()}))
	_, err := a.Add(context.Background(), "application/json", strings.NewReader(`{}`))
	if err == nil || !strings.Contains(err.Error(), "name") {
		t.Fatalf("expected name-required error, got %v", err)
	}
}

func TestSkillsAdapter_AddInline_RejectsWrongContentType(t *testing.T) {
	a := newSkillsAdapter(skills.NewManager(skills.Options{CustomDir: t.TempDir()}))
	_, err := a.Add(context.Background(), "text/plain", strings.NewReader(`{"name":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "application/json") {
		t.Fatalf("expected content-type error, got %v", err)
	}
}
