package cliclient_test

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/agentctl/agentctl/internal/api"
	"github.com/agentctl/agentctl/internal/cliclient"
	"github.com/agentctl/agentctl/internal/proto"
	"github.com/agentctl/agentctl/internal/socksrv"
)

type stubDocker struct{}

func (stubDocker) Info(_ context.Context) (proto.DockerHealth, error) {
	return proto.DockerHealth{OK: true, Version: "27.0.0-test"}, nil
}

func TestSocketHealthRoundTrip(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "agentd.sock")
	apiSrv := api.New(api.Options{Docker: stubDocker{}})
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	srv := socksrv.New(socksrv.Options{SocketPath: sockPath, API: apiSrv, Logger: logger})
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = srv.Close() }()

	c, err := cliclient.Dial(sockPath, time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	hr, err := c.Health()
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if !hr.OK {
		t.Errorf("ok = false; want true")
	}
	if hr.Docker.Version != "27.0.0-test" {
		t.Errorf("docker version = %q, want 27.0.0-test", hr.Docker.Version)
	}
}
