package cli

import (
	"context"
	"flag"
	"fmt"
)

func runDetach(_ context.Context, env *Env, args []string) int {
	fs := flag.NewFlagSet("detach", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: agentctl detach")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "Detach is a client-side action: from inside `agentctl start` or `agentctl attach`,")
		fmt.Fprintln(env.Stderr, "press Ctrl-D (EOF) or Ctrl-C to detach. The session keeps running.")
	}
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	fs.Usage()
	return ExitOK
}
