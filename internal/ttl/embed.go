package ttl

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"strings"
	"time"
)

//go:embed builtins/agents/*.yaml builtins/workflows/*.yaml
var builtinsFS embed.FS

// Materialize upserts the embedded built-in agents and workflows into the
// agents/workflows tables. Existing rows for built-in names are refreshed
// on every boot so a binary update can ship corrected prompts; custom
// rows are untouched (the source column distinguishes them).
//
// Returns the number of rows written (insert + update).
func Materialize(ctx context.Context, db DB) (int, error) {
	written := 0
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, kind := range []struct {
		sub, table string
	}{
		{"builtins/agents", "agents"},
		{"builtins/workflows", "workflows"},
	} {
		entries, err := fs.ReadDir(builtinsFS, kind.sub)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
				continue
			}
			body, err := fs.ReadFile(builtinsFS, kind.sub+"/"+e.Name())
			if err != nil {
				return written, fmt.Errorf("read embedded %s: %w", e.Name(), err)
			}
			name := strings.TrimSuffix(e.Name(), ".yaml")
			// Validate before upserting so a malformed built-in fails loudly
			// at boot rather than silently filling the index with garbage.
			switch kind.table {
			case "agents":
				a, perr := ParseAgentYAML(body)
				if perr != nil {
					return written, fmt.Errorf("parse builtin agent %s: %w", name, perr)
				}
				if verr := validateAgent(a); verr != nil {
					return written, fmt.Errorf("validate builtin agent %s: %w", name, verr)
				}
			case "workflows":
				w, perr := ParseWorkflowYAML(body)
				if perr != nil {
					return written, fmt.Errorf("parse builtin workflow %s: %w", name, perr)
				}
				if verr := validateWorkflow(w); verr != nil {
					return written, fmt.Errorf("validate builtin workflow %s: %w", name, verr)
				}
			}
			q := fmt.Sprintf(`INSERT INTO %s (name, source, yaml_body, created_at, updated_at)
                VALUES (?, 'builtin', ?, ?, ?)
                ON CONFLICT(name) DO UPDATE SET
                    source='builtin',
                    yaml_body=excluded.yaml_body,
                    updated_at=excluded.updated_at
                WHERE %s.source='builtin'`, kind.table, kind.table)
			res, err := db.ExecContext(ctx, q, name, string(body), now, now)
			if err != nil {
				return written, fmt.Errorf("upsert builtin %s/%s: %w", kind.table, name, err)
			}
			n, _ := res.RowsAffected()
			written += int(n)
		}
	}
	return written, nil
}
