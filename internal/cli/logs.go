package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/agentctl/agentctl/internal/cliclient"
	"github.com/agentctl/agentctl/internal/proto"
)

func runLogs(ctx context.Context, env *Env, args []string) int {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	daemon := fs.Bool("daemon", false, "tail the daemon log instead of a session log")
	follow := fs.Bool("f", false, "follow log output")
	asJSON := fs.Bool("json", false, "raw NDJSON / journalctl JSON output")
	raw := fs.Bool("raw", false, "emit per-session log lines unchanged (NDJSON)")
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: agentctl logs --daemon [-f] [--json]")
		fmt.Fprintln(env.Stderr, "       agentctl logs <session> [-f] [--raw]")
		fs.PrintDefaults()
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "TODO(M5): --container will proxy `docker logs` for the session container.")
	}
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if *daemon {
		switch runtime.GOOS {
		case "linux":
			return tailJournal(ctx, env, *follow, *asJSON)
		default:
			return tailDarwinFile(ctx, env, *follow)
		}
	}
	if fs.NArg() < 1 {
		fs.Usage()
		return ExitUsage
	}
	sessionID := fs.Arg(0)
	return streamSessionLog(ctx, env, sessionID, *follow, *raw)
}

func streamSessionLog(ctx context.Context, env *Env, sessionID string, follow, raw bool) int {
	c, err := cliclient.Dial(env.Layout.SocketFile, 3*time.Second)
	if err != nil {
		fmt.Fprintf(env.Stderr, "logs: %v\n", err)
		return ExitEnvironment
	}
	defer func() { _ = c.Close() }()
	stream, err := c.StartStream(proto.OpGetLogs, proto.GetLogsRequest{SessionID: sessionID, Follow: follow})
	if err != nil {
		fmt.Fprintf(env.Stderr, "logs: %v\n", err)
		return ExitGeneric
	}
	for {
		select {
		case <-ctx.Done():
			stream.Close()
			return ExitOK
		default:
		}
		fr := stream.Recv()
		if fr.Err != nil {
			fmt.Fprintf(env.Stderr, "logs: %v\n", fr.Err)
			return ExitGeneric
		}
		if fr.EndCode != "" {
			return ExitOK
		}
		var d proto.LogLineData
		if err := json.Unmarshal(fr.Data, &d); err != nil {
			continue
		}
		if raw {
			_, _ = io.WriteString(env.Stdout, d.Raw)
			continue
		}
		printPrettyLogLine(env.Stdout, d.Raw)
	}
}

func printPrettyLogLine(w io.Writer, raw string) {
	raw = strings.TrimRight(raw, "\n")
	if raw == "" {
		return
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		fmt.Fprintln(w, raw)
		return
	}
	ts, _ := rec["ts"].(string)
	level, _ := rec["level"].(string)
	msg, _ := rec["msg"].(string)
	fmt.Fprintf(w, "%-30s %-5s %s", ts, strings.ToUpper(level), msg)
	for k, v := range rec {
		if k == "ts" || k == "level" || k == "msg" || k == "component" {
			continue
		}
		fmt.Fprintf(w, " %s=%v", k, v)
	}
	fmt.Fprintln(w, "")
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
