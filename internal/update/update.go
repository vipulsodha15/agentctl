package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

type BuildOptions struct {
	Tag         string
	ContextPath string
	NoCache     bool
	Output      io.Writer
}

type BuildResult struct {
	ImageID  string
	Tag      string
	Duration time.Duration
}

func Build(ctx context.Context, opts BuildOptions) (BuildResult, error) {
	if opts.Tag == "" {
		return BuildResult{}, errors.New("tag required")
	}
	if opts.ContextPath == "" {
		return BuildResult{}, errors.New("context path required")
	}
	if _, err := os.Stat(opts.ContextPath); err != nil {
		return BuildResult{}, fmt.Errorf("build context %s: %w", opts.ContextPath, err)
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return BuildResult{}, fmt.Errorf("docker not on PATH: %w", err)
	}
	args := []string{"build", "-t", opts.Tag}
	if opts.NoCache {
		args = append(args, "--no-cache")
	}
	args = append(args, opts.ContextPath)
	cmd := exec.CommandContext(ctx, "docker", args...)
	if opts.Output != nil {
		cmd.Stdout = opts.Output
		cmd.Stderr = opts.Output
	} else {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	start := time.Now()
	if err := cmd.Run(); err != nil {
		return BuildResult{}, fmt.Errorf("docker build: %w", err)
	}
	id, err := Inspect(ctx, opts.Tag)
	if err != nil {
		return BuildResult{}, err
	}
	return BuildResult{ImageID: id, Tag: opts.Tag, Duration: time.Since(start)}, nil
}

func Inspect(ctx context.Context, ref string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{.Id}}", ref)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("docker inspect %s: %w", ref, err)
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return "", fmt.Errorf("empty image id for %s", ref)
	}
	return id, nil
}
