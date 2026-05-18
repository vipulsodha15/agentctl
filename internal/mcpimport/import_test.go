package mcpimport

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

func TestParseClaude(t *testing.T) {
	dir := t.TempDir()
	body := `{
  "mcpServers": {
    "github": { "url": "https://api.githubcopilot.com/mcp/", "type": "http" },
    "linear": { "url": "https://mcp.linear.app/sse", "type": "sse" },
    "fs":     { "command": "node", "args": ["server.js"], "env": {"FOO": "bar"} },
    "broken": { "type": "stdio" }
  }
}`
	p := writeFile(t, dir, "claude.json", body)
	got, err := ParseClaude(p)
	if err != nil {
		t.Fatalf("ParseClaude: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("want 4 entries, got %d", len(got))
	}
	byName := map[string]ParsedEntry{}
	for _, e := range got {
		byName[e.Name] = e
	}

	if e := byName["github"]; e.URL != "https://api.githubcopilot.com/mcp/" || e.Transport != "http" || e.Skip != "" {
		t.Errorf("github: %+v", e)
	}
	if e := byName["linear"]; e.Transport != "sse" || e.Skip != "" {
		t.Errorf("linear: %+v", e)
	}
	if e := byName["fs"]; e.Skip == "" || e.Command != "node" || !reflect.DeepEqual(e.Args, []string{"server.js"}) || e.Env["FOO"] != "bar" {
		t.Errorf("fs: %+v", e)
	}
	if e := byName["broken"]; e.Skip == "" {
		t.Errorf("broken (type=stdio without command) should be skipped: %+v", e)
	}

	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"broken", "fs", "github", "linear"}) {
		t.Errorf("names: %v", names)
	}
}

func TestParseClaudeMissingFile(t *testing.T) {
	_, err := ParseClaude(filepath.Join(t.TempDir(), "nope.json"))
	if !os.IsNotExist(err) {
		t.Fatalf("want os.ErrNotExist, got %v", err)
	}
}

func TestParseCodex(t *testing.T) {
	dir := t.TempDir()
	body := `
[mcp_servers.fs]
command = "node"
args    = ["server.js"]
env     = { FOO = "bar" }

[mcp_servers.remote]
url = "https://example.com/mcp/"
type = "http"
`
	p := writeFile(t, dir, "config.toml", body)
	got, err := ParseCodex(p)
	if err != nil {
		t.Fatalf("ParseCodex: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 entries, got %d", len(got))
	}
	byName := map[string]ParsedEntry{}
	for _, e := range got {
		byName[e.Name] = e
	}
	if e := byName["fs"]; e.Skip == "" || e.Command != "node" {
		t.Errorf("fs: %+v", e)
	}
	if e := byName["remote"]; e.URL == "" || e.Transport != "http" || e.Skip != "" {
		t.Errorf("remote: %+v", e)
	}
}

func TestDefaultPaths(t *testing.T) {
	if got := DefaultClaudePath("/home/u"); got != "/home/u/.claude.json" {
		t.Errorf("claude default: %s", got)
	}
	t.Setenv("CODEX_HOME", "")
	if got := DefaultCodexPath("/home/u"); got != "/home/u/.codex/config.toml" {
		t.Errorf("codex default: %s", got)
	}
	t.Setenv("CODEX_HOME", "/opt/codex")
	if got := DefaultCodexPath("/home/u"); got != "/opt/codex/config.toml" {
		t.Errorf("codex w/ env: %s", got)
	}
}
