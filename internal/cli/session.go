package cli

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/agentctl/agentctl/internal/cliclient"
	"github.com/agentctl/agentctl/internal/proto"
)

// runSession dispatches `agentctl session <subcommand>` — today only
// `set-model` exists (the scripting surface for ADR 0020 §2's mid-session
// model switch). Per the ADR's UX principles, this command is intentionally
// terse and not advertised in `--help` examples: web users discover the
// dropdown, keyboard users discover `/model`, and only scripters reach for
// this entry point. Tucked under a `session` group so we don't pollute the
// top-level namespace with one-off per-attribute setters as the surface
// grows.
func runSession(ctx context.Context, env *Env, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(env.Stderr, "Usage: agentctl session <subcommand>")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "Subcommands:")
		fmt.Fprintln(env.Stderr, "  set-model <session-id> <model>   Swap the model id mid-session (ADR 0020).")
		return ExitUsage
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "set-model":
		return runSessionSetModel(ctx, env, rest)
	default:
		fmt.Fprintf(env.Stderr, "session: unknown subcommand %q\n", sub)
		return ExitUsage
	}
}

func runSessionSetModel(_ context.Context, env *Env, args []string) int {
	fs := flag.NewFlagSet("session set-model", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: agentctl session set-model <session-id> <model>")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "Switches a running session's model id. The runtime swaps clients in")
		fmt.Fprintln(env.Stderr, "place — conversation history is preserved. Cost rows on either side")
		fmt.Fprintln(env.Stderr, "of the switch attribute to the model that produced them.")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "Scripting surface: web users see a dropdown in the session header;")
		fmt.Fprintln(env.Stderr, "keyboard users type /model <name> in the chat input.")
	}
	if err := fs.Parse(reorderArgs(args)); err != nil {
		return ExitUsage
	}
	pos := fs.Args()
	if len(pos) != 2 {
		fs.Usage()
		return ExitUsage
	}
	sessionID, model := pos[0], pos[1]

	c, err := cliclient.Dial(env.Layout.SocketFile, 3*time.Second)
	if err != nil {
		fmt.Fprintf(env.Stderr, "set-model: %v\n", err)
		return ExitEnvironment
	}
	defer func() { _ = c.Close() }()

	var resp proto.UpdateSessionResponse
	if err := c.Call(proto.OpUpdateSession, proto.UpdateSessionRequest{
		SessionID: sessionID,
		Model:     &model,
	}, &resp, 10*time.Second); err != nil {
		// Bad-request (unknown model, immutable provider) prints to stderr
		// with a non-zero exit so scripts can detect it; the message body
		// already comes through the APIError's Message field.
		fmt.Fprintf(env.Stderr, "set-model: %v\n", err)
		return ExitGeneric
	}
	fmt.Fprintf(env.Stdout, "session %s model=%s\n", resp.Session.ID, resp.Session.Model)
	return ExitOK
}
