package cli

import (
	"context"
	"fmt"

	"github.com/agentctl/agentctl/internal/config"
)

func runConfig(_ context.Context, env *Env, args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(env.Stderr, "Usage: agentctl config get <key>")
		fmt.Fprintln(env.Stderr, "       agentctl config set <key> <value>")
		return ExitUsage
	}
	cfg, err := config.Load(env.Layout.ConfigFile)
	if err != nil {
		fmt.Fprintf(env.Stderr, "config: %v\n", err)
		return ExitEnvironment
	}
	switch args[0] {
	case "get":
		key := args[1]
		v, ok := config.Get(cfg, key)
		if !ok {
			fmt.Fprintf(env.Stderr, "unknown key: %s\n", key)
			return ExitUsage
		}
		fmt.Fprintln(env.Stdout, v)
		return ExitOK
	case "set":
		if len(args) < 3 {
			fmt.Fprintln(env.Stderr, "config set requires KEY VALUE")
			return ExitUsage
		}
		if err := config.Set(&cfg, args[1], args[2]); err != nil {
			fmt.Fprintf(env.Stderr, "config: %v\n", err)
			return ExitUsage
		}
		if err := config.Save(env.Layout.ConfigFile, cfg); err != nil {
			fmt.Fprintf(env.Stderr, "config: %v\n", err)
			return ExitGeneric
		}
		fmt.Fprintln(env.Stdout, "ok")
		return ExitOK
	}
	fmt.Fprintf(env.Stderr, "config: unknown subcommand %q\n", args[0])
	return ExitUsage
}
