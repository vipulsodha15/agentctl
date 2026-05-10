package api

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"time"

	"github.com/agentctl/agentctl/internal/proto"
)

type CLIDockerProbe struct {
	Timeout time.Duration
}

type dockerVersionOutput struct {
	Server struct {
		Version string `json:"Version"`
	} `json:"Server"`
}

func (p *CLIDockerProbe) Info(ctx context.Context) (proto.DockerHealth, error) {
	timeout := p.Timeout
	if timeout == 0 {
		timeout = 3 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if _, err := exec.LookPath("docker"); err != nil {
		return proto.DockerHealth{OK: false, Error: "docker binary not on PATH"}, nil
	}
	cmd := exec.CommandContext(cctx, "docker", "info", "--format", "{{json .}}")
	if err := cmd.Run(); err != nil {
		var msg string
		if errors.Is(cctx.Err(), context.DeadlineExceeded) {
			msg = "docker info timed out"
		} else {
			msg = "docker info failed: " + err.Error()
		}
		return proto.DockerHealth{OK: false, Error: msg}, nil
	}
	verCmd := exec.CommandContext(cctx, "docker", "version", "--format", "{{json .}}")
	out, err := verCmd.Output()
	if err != nil {
		return proto.DockerHealth{OK: true, Version: "unknown"}, nil
	}
	var v dockerVersionOutput
	if err := json.Unmarshal(out, &v); err != nil {
		return proto.DockerHealth{OK: true, Version: strings.TrimSpace(string(out))}, nil
	}
	return proto.DockerHealth{OK: true, Version: v.Server.Version}, nil
}
