package cliclient_test

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
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

// TestDialMissingSocketProducesActionableHint guards the message `agentctl
// start` (and every other socket-using command) prints when agentd isn't
// running. We want a "start it with `agentctl init`" hint, not the raw
// "dial unix … no such file or directory" that scared users into thinking
// the install was broken.
func TestDialMissingSocketProducesActionableHint(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "agentd.sock")
	_, err := cliclient.Dial(missing, 250*time.Millisecond)
	if err == nil {
		t.Fatalf("expected dial to fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "agentd unreachable") {
		t.Errorf("expected 'agentd unreachable' prefix, got: %s", msg)
	}
	if !strings.Contains(msg, "agentctl init") {
		t.Errorf("expected hint mentioning `agentctl init`, got: %s", msg)
	}
}
