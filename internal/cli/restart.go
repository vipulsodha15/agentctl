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

func runRestart(_ context.Context, env *Env, args []string) int {
	fs := flag.NewFlagSet("restart", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	yes := fs.Bool("yes", false, "do not prompt for confirmation")
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: agentctl restart <session> [--yes]")
		fs.PrintDefaults()
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "Stops the session container and recreates it from the currently pinned image.")
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
		fmt.Fprintf(env.Stderr, "restart session %s? in-flight turn (if any) will be interrupted [y/N] ", sessionID)
		br := bufio.NewReader(env.Stdin)
		ans, _ := br.ReadString('\n')
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(ans)), "y") {
			fmt.Fprintln(env.Stderr, "aborted")
			return ExitOK
		}
	}
	c, err := cliclient.Dial(env.Layout.SocketFile, 3*time.Second)
	if err != nil {
		fmt.Fprintf(env.Stderr, "restart: %v\n", err)
		return ExitEnvironment
	}
	defer func() { _ = c.Close() }()
	var resp proto.RestartSessionResponse
	if err := c.Call(proto.OpRestartSession, proto.RestartSessionRequest{SessionID: sessionID}, &resp, 90*time.Second); err != nil {
		fmt.Fprintf(env.Stderr, "restart: %v\n", err)
		if apiErr, ok := err.(*cliclient.APIError); ok && apiErr.Code == proto.ErrNotFound {
			return ExitSessionState
		}
		return ExitGeneric
	}
	fmt.Fprintf(env.Stdout, "session %s %s on %s\n", resp.SessionID, resp.Status, resp.ImageID)
	return ExitOK
}
