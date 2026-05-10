package cli

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/agentctl/agentctl/internal/cliclient"
	"github.com/agentctl/agentctl/internal/proto"
)

func runInterrupt(_ context.Context, env *Env, args []string) int {
	fs := flag.NewFlagSet("interrupt", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	clear := fs.Bool("clear-queue", false, "drop any messages queued behind the cancelled turn")
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: agentctl interrupt <session> [--clear-queue]")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "Cancels the in-flight turn for a session. The session itself stays running.")
		fmt.Fprintln(env.Stderr, "If --clear-queue is set, any messages queued behind the cancelled turn are dropped.")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "Flags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() < 1 {
		fs.Usage()
		return ExitUsage
	}
	sessionID := fs.Arg(0)
	c, err := cliclient.Dial(env.Layout.SocketFile, 3*time.Second)
	if err != nil {
		fmt.Fprintf(env.Stderr, "interrupt: %v\n", err)
		return ExitEnvironment
	}
	defer func() { _ = c.Close() }()
	var resp proto.InterruptResponse
	if err := c.Call(proto.OpInterrupt, proto.InterruptRequest{
		SessionID: sessionID, ClearQueue: *clear,
	}, &resp, 5*time.Second); err != nil {
		fmt.Fprintf(env.Stderr, "interrupt: %v\n", err)
		if apiErr, ok := err.(*cliclient.APIError); ok && apiErr.Code == proto.ErrPreconditionFailed {
			return ExitSessionState
		}
		return ExitGeneric
	}
	if !resp.Interrupted {
		fmt.Fprintln(env.Stderr, "no in-flight turn to interrupt")
		return ExitSessionState
	}
	fmt.Fprintf(env.Stdout, "interrupted; cleared %d queued message(s)\n", resp.ClearedQueueDepth)
	return ExitOK
}
