package socksrv_test

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/agentctl/agentctl/internal/api"
	"github.com/agentctl/agentctl/internal/cliclient"
	"github.com/agentctl/agentctl/internal/fan"
	"github.com/agentctl/agentctl/internal/proto"
	"github.com/agentctl/agentctl/internal/sm"
	"github.com/agentctl/agentctl/internal/socksrv"
)

type stubDocker struct{}

func (stubDocker) Info(_ context.Context) (proto.DockerHealth, error) {
	return proto.DockerHealth{OK: true, Version: "stub"}, nil
}

func TestSocketDispatchesNewOps(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "agentd.sock")
	apiSrv := api.New(api.Options{Docker: stubDocker{}})
	mgr := sm.New(sm.Options{
		SessionsDir:     filepath.Join(dir, "sessions"),
		Hub:             fan.NewHub(),
		DefaultModel:    "claude-sonnet-4-6",
		SnapshotTimeout: 50 * time.Millisecond,
	})
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	srv := socksrv.New(socksrv.Options{
		SocketPath: sockPath,
		API:        apiSrv,
		Manager:    mgr,
		Logger:     logger,
	})
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()

	c, err := cliclient.Dial(sockPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	var hr proto.HealthResponse
	if err := c.Call(proto.OpHealth, proto.HealthRequest{}, &hr, time.Second); err != nil {
		t.Fatalf("health: %v", err)
	}
	if !hr.OK || !hr.Docker.OK {
		t.Fatalf("health=%+v", hr)
	}

	var resp proto.CreateSessionResponse
	if err := c.Call(proto.OpCreateSession, proto.CreateSessionRequest{Name: "s", Provider: "anthropic"}, &resp, 5*time.Second); err != nil {
		t.Fatalf("create: %v", err)
	}
	if resp.SessionID == "" {
		t.Fatalf("missing session id: %+v", resp)
	}

	var listResp proto.ListSessionsResponse
	if err := c.Call(proto.OpListSessions, proto.ListSessionsRequest{}, &listResp, time.Second); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listResp.Sessions) != 1 || listResp.Sessions[0].ID != resp.SessionID {
		t.Fatalf("list=%+v", listResp)
	}

	if err := c.Call(proto.OpInterrupt, proto.InterruptRequest{SessionID: resp.SessionID}, nil, time.Second); err == nil {
		t.Fatal("expected precondition_failed for interrupt with no in-flight turn")
	}
}
