package ttl

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed builtins/agents/*.yaml builtins/workflows/*.yaml
var builtinsFS embed.FS

// MaterializeBuiltins writes embedded built-in agent and workflow YAML files
// to the given target directories if they are absent. Existing files are left
// alone so user edits are not clobbered. Returns the count of files written.
func MaterializeBuiltins(agentsDir, workflowsDir string) (int, error) {
	var written int
	for _, kind := range []struct {
		sub, dst string
	}{
		{"builtins/agents", agentsDir},
		{"builtins/workflows", workflowsDir},
	} {
		entries, err := fs.ReadDir(builtinsFS, kind.sub)
		if err != nil {
			continue
		}
		if err := os.MkdirAll(kind.dst, 0o700); err != nil {
			return written, err
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
				continue
			}
			body, err := fs.ReadFile(builtinsFS, kind.sub+"/"+e.Name())
			if err != nil {
				return written, err
			}
			dst := filepath.Join(kind.dst, e.Name())
			// Re-write built-ins unconditionally; user customizations live
			// in the custom/ directories.
			if err := os.WriteFile(dst, body, 0o600); err != nil {
				return written, err
			}
			written++
		}
	}
	return written, nil
}
