package socksrv_test

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/agentctl/agentctl/internal/api"
	"github.com/agentctl/agentctl/internal/cliclient"
	"github.com/agentctl/agentctl/internal/mcp"
	"github.com/agentctl/agentctl/internal/proto"
	"github.com/agentctl/agentctl/internal/skills"
	"github.com/agentctl/agentctl/internal/socksrv"
	"github.com/agentctl/agentctl/internal/store"
)

func newServerWithMCPs(t *testing.T) (*cliclient.Client, func()) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(store.Options{Path: filepath.Join(dir, "test.db")})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	apiSrv := api.New(api.Options{Docker: stubDocker{}})
	reg := mcp.NewRegistry(mcp.Options{Store: st})
	skMgr := skills.NewManager(skills.Options{
		BuiltinDir: filepath.Join(dir, "builtin"),
		CustomDir:  filepath.Join(dir, "custom"),
	})
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	sockPath := filepath.Join(dir, "agentd.sock")
	srv := socksrv.New(socksrv.Options{
		SocketPath: sockPath,
		API:        apiSrv,
		MCPs:       reg,
		Skills:     skMgr,
		Logger:     logger,
	})
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	c, err := cliclient.Dial(sockPath, time.Second)
	if err != nil {
		_ = srv.Close()
		t.Fatal(err)
	}
	cleanup := func() {
		_ = c.Close()
		_ = srv.Close()
		_ = st.Close()
	}
	return c, cleanup
}

func TestMCPCRUDOverSocket(t *testing.T) {
	c, cleanup := newServerWithMCPs(t)
	defer cleanup()

	var addResp proto.AddMCPResponse
	if err := c.Call(proto.OpAddMCP, proto.AddMCPRequest{
		Name: "team-x", URL: "https://example.com/mcp/", Transport: "http", Kind: "none",
		DefaultEnabled: true, Description: "team",
	}, &addResp, time.Second); err != nil {
		t.Fatalf("add: %v", err)
	}
	if addResp.MCP.Name != "team-x" {
		t.Fatalf("add resp: %+v", addResp)
	}

	if err := c.Call(proto.OpAddMCP, proto.AddMCPRequest{Name: "team-x", URL: "x"}, &proto.AddMCPResponse{}, time.Second); err == nil {
		t.Fatal("expected conflict on duplicate add")
	}

	var list proto.ListMCPsResponse
	if err := c.Call(proto.OpListMCPs, proto.ListMCPsRequest{}, &list, time.Second); err != nil {
		t.Fatal(err)
	}
	if len(list.MCPs) != 1 {
		t.Fatalf("list=%+v", list)
	}

	flag := false
	if err := c.Call(proto.OpSetDefaultMCP, proto.SetDefaultMCPRequest{Name: "team-x", DefaultEnabled: flag}, &proto.SetDefaultMCPResponse{}, time.Second); err != nil {
		t.Fatalf("set-default: %v", err)
	}
	if err := c.Call(proto.OpListMCPs, proto.ListMCPsRequest{}, &list, time.Second); err != nil {
		t.Fatal(err)
	}
	if list.MCPs[0].DefaultEnabled {
		t.Errorf("expected default disabled after set-default, got %+v", list.MCPs[0])
	}

	newURL := "https://team.example/mcp/v2"
	if err := c.Call(proto.OpUpdateMCP, proto.UpdateMCPRequest{Name: "team-x", URL: &newURL}, &proto.UpdateMCPResponse{}, time.Second); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := c.Call(proto.OpListMCPs, proto.ListMCPsRequest{}, &list, time.Second); err != nil {
		t.Fatal(err)
	}
	if list.MCPs[0].URL != newURL {
		t.Errorf("expected url updated, got %+v", list.MCPs[0])
	}

	var rm proto.RemoveMCPResponse
	if err := c.Call(proto.OpRemoveMCP, proto.RemoveMCPRequest{Name: "team-x"}, &rm, time.Second); err != nil {
		t.Fatal(err)
	}
	if !rm.Removed {
		t.Errorf("expected removed=true, got %+v", rm)
	}
	if err := c.Call(proto.OpRemoveMCP, proto.RemoveMCPRequest{Name: "team-x"}, &rm, time.Second); err == nil {
		t.Fatal("expected not_found on second remove")
	}
}
