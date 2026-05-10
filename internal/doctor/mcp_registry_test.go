package doctor

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/agentctl/agentctl/internal/mcp"
	"github.com/agentctl/agentctl/internal/store"
)

func openSeededStore(t *testing.T, rows []mcp.Entry) string {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "agentd.db")
	st, err := store.Open(store.Options{Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	reg := mcp.NewRegistry(mcp.Options{Store: st, Now: func() time.Time { return time.Now().UTC() }})
	for _, r := range rows {
		if err := reg.Add(context.Background(), r); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	_ = st.Close()
	return dbPath
}

func TestCheckMCPRegistryOK(t *testing.T) {
	dbPath := openSeededStore(t, []mcp.Entry{
		{Name: "github", URL: "https://api.example/mcp/", Transport: "http", Kind: "github_pat"},
		{Name: "internal", URL: "http://10.0.0.5/", Transport: "sse", Kind: "none"},
	})
	c := checkMCPRegistry(dbPath)
	if c.Status != StatusOK {
		t.Errorf("expected ok, got %s: %s / %s", c.Status, c.Message, c.Detail)
	}
}

func TestCheckMCPRegistryWarnsOnUnknown(t *testing.T) {
	dbPath := openSeededStore(t, []mcp.Entry{
		{Name: "weird", URL: "http://x/", Transport: "smb", Kind: "oauth2"},
	})
	c := checkMCPRegistry(dbPath)
	if c.Status != StatusWarn {
		t.Errorf("expected warn, got %s: %s / %s", c.Status, c.Message, c.Detail)
	}
}

func TestCheckMCPRegistryFailsOnMalformedURL(t *testing.T) {
	dbPath := openSeededStore(t, []mcp.Entry{
		{Name: "broken", URL: "not-a-url", Transport: "http", Kind: "none"},
	})
	c := checkMCPRegistry(dbPath)
	if c.Status != StatusFail {
		t.Errorf("expected fail, got %s: %s", c.Status, c.Detail)
	}
}

func TestCheckMCPRegistryMissingDB(t *testing.T) {
	c := checkMCPRegistry(filepath.Join(t.TempDir(), "absent.db"))
	if c.Status != StatusFail {
		t.Errorf("expected fail, got %s", c.Status)
	}
}
