package cli

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/agentctl/agentctl/internal/cliclient"
)

func runAttach(ctx context.Context, env *Env, args []string) int {
	fs := flag.NewFlagSet("attach", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	send := fs.Bool("send", false, "read messages from stdin while attached")
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: agentctl attach <session> [--send]")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "Subscribes to a session's event stream. The first frame is always a")
		fmt.Fprintln(env.Stderr, "session.snapshot (full conversation + state); subsequent frames are live events.")
		fmt.Fprintln(env.Stderr, "Ctrl-D / Ctrl-C detaches without affecting the session.")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "Examples:")
		fmt.Fprintln(env.Stderr, "  agentctl attach sess_01JFZ123ABC")
		fmt.Fprintln(env.Stderr, "  agentctl attach sess_01JFZ123ABC --send   # also forward stdin as messages")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "Flags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(reorderArgs(args)); err != nil {
		return ExitUsage
	}
	if fs.NArg() < 1 {
		fs.Usage()
		return ExitUsage
	}
	sessionID := fs.Arg(0)
	c, err := cliclient.Dial(env.Layout.SocketFile, 3*time.Second)
	if err != nil {
		fmt.Fprintf(env.Stderr, "attach: %v\n", err)
		return ExitEnvironment
	}
	defer func() { _ = c.Close() }()
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if *send {
		go func() {
			readStdinAndSend(streamCtx, c, sessionID, env.Stdin, env.Stderr)
			cancel()
		}()
	}
	return attachAndRender(streamCtx, c, sessionID, env)
}
