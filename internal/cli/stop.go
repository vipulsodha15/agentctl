package cli

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/agentctl/agentctl/internal/cliclient"
	"github.com/agentctl/agentctl/internal/proto"
)

func runStop(_ context.Context, env *Env, args []string) int {
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	force := fs.Bool("force", false, "skip graceful shutdown of the in-flight turn")
	yes := fs.Bool("yes", false, "do not prompt for confirmation")
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: agentctl stop <session> [--force] [--yes]")
		fs.PrintDefaults()
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "Removes the session container, its volume, and marks the session terminated.")
		fmt.Fprintln(env.Stderr, "TODO(M4): also remove the per-session bridge network.")
	}
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() < 1 {
		fs.Usage()
		return ExitUsage
	}
	sessionID := fs.Arg(0)
	if !*yes {
		fmt.Fprintf(env.Stderr, "stop session %s? [y/N] ", sessionID)
		br := bufio.NewReader(env.Stdin)
		ans, _ := br.ReadString('\n')
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(ans)), "y") {
			fmt.Fprintln(env.Stderr, "aborted")
			return ExitOK
		}
	}
	c, err := cliclient.Dial(env.Layout.SocketFile, 3*time.Second)
	if err != nil {
		fmt.Fprintf(env.Stderr, "stop: %v\n", err)
		return ExitEnvironment
	}
	defer func() { _ = c.Close() }()
	var resp proto.TerminateSessionResponse
	if err := c.Call(proto.OpTerminateSession, proto.TerminateSessionRequest{
		SessionID: sessionID, Force: *force,
	}, &resp, 60*time.Second); err != nil {
		fmt.Fprintf(env.Stderr, "stop: %v\n", err)
		return ExitGeneric
	}
	fmt.Fprintf(env.Stdout, "session %s %s\n", resp.SessionID, resp.Status)
	return ExitOK
}
