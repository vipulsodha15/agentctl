package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

func runLogs(ctx context.Context, env *Env, args []string) int {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	daemon := fs.Bool("daemon", false, "tail the daemon log")
	follow := fs.Bool("f", false, "follow log output")
	asJSON := fs.Bool("json", false, "raw NDJSON output (journalctl -o json)")
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: agentctl logs --daemon [-f] [--json]")
		fmt.Fprintln(env.Stderr, "       agentctl logs <session>          (M2)")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if !*daemon {
		fmt.Fprintln(env.Stderr, "logs: per-session logs land in M2; pass --daemon to view daemon logs")
		return ExitUsage
	}
	switch runtime.GOOS {
	case "linux":
		return tailJournal(ctx, env, *follow, *asJSON)
	default:
		return tailDarwinFile(ctx, env, *follow)
	}
}

func tailJournal(ctx context.Context, env *Env, follow, asJSON bool) int {
	if _, err := exec.LookPath("journalctl"); err != nil {
		fmt.Fprintf(env.Stderr, "logs: journalctl not on PATH\n")
		return ExitEnvironment
	}
	args := []string{"--user", "-u", "agentd"}
	if follow {
		args = append(args, "-f")
	}
	if asJSON {
		args = append(args, "-o", "json")
	}
	cmd := exec.CommandContext(ctx, "journalctl", args...)
	cmd.Stdout = env.Stdout
	cmd.Stderr = env.Stderr
	if err := cmd.Run(); err != nil {
		return ExitGeneric
	}
	return ExitOK
}

func tailDarwinFile(ctx context.Context, env *Env, follow bool) int {
	logPath := filepath.Join(env.Layout.Home, "Library", "Logs", "agentctl", "agentd.log")
	f, err := os.Open(logPath)
	if err != nil {
		fmt.Fprintf(env.Stderr, "logs: open %s: %v\n", logPath, err)
		return ExitEnvironment
	}
	defer func() { _ = f.Close() }()
	if _, err := io.Copy(env.Stdout, f); err != nil {
		return ExitGeneric
	}
	if !follow {
		return ExitOK
	}
	for {
		select {
		case <-ctx.Done():
			return ExitOK
		case <-time.After(500 * time.Millisecond):
		}
		if _, err := io.Copy(env.Stdout, f); err != nil {
			return ExitGeneric
		}
	}
}
