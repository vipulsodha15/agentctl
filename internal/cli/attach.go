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
	send := fs.Bool("send", false, "read messages from stdin while attached (plain mode only)")
	plain := fs.Bool("plain", false, "use line-based streaming output instead of the TUI")
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: agentctl attach <session> [--plain] [--send]")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "Attaches to a session and renders its event stream in a fullscreen TUI.")
		fmt.Fprintln(env.Stderr, "Type a message + Enter to send. Esc interrupts the in-flight turn.")
		fmt.Fprintln(env.Stderr, "Ctrl-D / Ctrl-C detaches without affecting the session.")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "When stdout isn't a TTY (or --plain is set) the older line-based renderer")
		fmt.Fprintln(env.Stderr, "is used. Combine --plain with --send to forward each stdin line as a message.")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "Examples:")
		fmt.Fprintln(env.Stderr, "  agentctl attach sess_01JFZ123ABC")
		fmt.Fprintln(env.Stderr, "  agentctl attach sess_01JFZ123ABC --plain --send")
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

	if *plain || !stdoutIsTTY(env) {
		if *send {
			go func() {
				readStdinAndSend(streamCtx, env.Layout.SocketFile, sessionID, env.Stdin, env.Stderr)
				cancel()
			}()
		}
		return attachAndRender(streamCtx, c, sessionID, env)
	}
	return attachAndRunTUI(streamCtx, c, sessionID, env)
}
