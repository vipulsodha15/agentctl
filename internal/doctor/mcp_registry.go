package doctor

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"time"

	"github.com/agentctl/agentctl/internal/mcp"
	"github.com/agentctl/agentctl/internal/store"
)

var (
	knownTransports = map[string]bool{"http": true, "sse": true}
	knownKinds      = map[string]bool{"none": true, "github_pat": true}
)

func checkMCPRegistry(dbPath string) Check {
	if _, err := os.Stat(dbPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Check{Name: "mcp.registry", Status: StatusFail, Message: "agentd.db missing"}
		}
		return Check{Name: "mcp.registry", Status: StatusFail, Message: err.Error()}
	}
	st, err := store.Open(store.Options{Path: dbPath})
	if err != nil {
		return Check{Name: "mcp.registry", Status: StatusFail, Message: err.Error()}
	}
	defer func() { _ = st.Close() }()
	reg := mcp.NewRegistry(mcp.Options{Store: st})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	entries, err := reg.List(ctx)
	if err != nil {
		return Check{Name: "mcp.registry", Status: StatusFail, Message: err.Error()}
	}
	var fails []string
	var warns []string
	for _, e := range entries {
		if e.Name == "" {
			fails = append(fails, "row missing name")
		}
		if e.URL == "" {
			fails = append(fails, e.Name+": empty url")
		} else if u, err := url.Parse(e.URL); err != nil || u.Scheme == "" || u.Host == "" {
			fails = append(fails, e.Name+": malformed url "+e.URL)
		}
		if e.Transport == "" {
			fails = append(fails, e.Name+": empty transport")
		} else if !knownTransports[e.Transport] {
			warns = append(warns, fmt.Sprintf("%s: unknown transport %q", e.Name, e.Transport))
		}
		if e.Kind == "" {
			fails = append(fails, e.Name+": empty kind")
		} else if !knownKinds[e.Kind] {
			warns = append(warns, fmt.Sprintf("%s: unknown kind %q", e.Name, e.Kind))
		}
	}
	sort.Strings(fails)
	sort.Strings(warns)
	if len(fails) > 0 {
		return Check{
			Name:    "mcp.registry",
			Status:  StatusFail,
			Message: fmt.Sprintf("%d row(s) malformed", len(fails)),
			Detail:  joinLines(append(fails, warns...)),
		}
	}
	if len(warns) > 0 {
		return Check{
			Name:    "mcp.registry",
			Status:  StatusWarn,
			Message: fmt.Sprintf("%d entries; %d with unknown transport/kind (tolerated)", len(entries), len(warns)),
			Detail:  joinLines(warns),
		}
	}
	return Check{
		Name:    "mcp.registry",
		Status:  StatusOK,
		Message: fmt.Sprintf("%d entries", len(entries)),
	}
}
