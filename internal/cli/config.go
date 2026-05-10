package cli

import (
	"context"
	"fmt"

	"github.com/agentctl/agentctl/internal/config"
)

func runConfig(_ context.Context, env *Env, args []string) int {
	if len(args) >= 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		printConfigHelp(env)
		return ExitOK
	}
	if len(args) < 2 {
		printConfigHelp(env)
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

func printConfigHelp(env *Env) {
	fmt.Fprintln(env.Stderr, "Usage: agentctl config get <key>")
	fmt.Fprintln(env.Stderr, "       agentctl config set <key> <value>")
	fmt.Fprintln(env.Stderr, "")
	fmt.Fprintln(env.Stderr, "Reads or writes a single key in ~/.config/agentctl/config.toml.")
	fmt.Fprintln(env.Stderr, "Known keys:")
	fmt.Fprintln(env.Stderr, "  agentd.web_addr            web server bind addr (default 127.0.0.1:7777)")
	fmt.Fprintln(env.Stderr, "  agentd.log_level           debug|info|warn|error (SIGHUP to apply)")
	fmt.Fprintln(env.Stderr, "  session.idle_timeout       Go duration (e.g. 15m)")
	fmt.Fprintln(env.Stderr, "  session.max_idle           Go duration (e.g. 24h)")
	fmt.Fprintln(env.Stderr, "  session.mem_limit          memory cap (e.g. 4GiB)")
	fmt.Fprintln(env.Stderr, "  session.cpu_limit          decimal cores (e.g. 2.0)")
	fmt.Fprintln(env.Stderr, "  session.queue_policy       queue|reject")
	fmt.Fprintln(env.Stderr, "  image.local_tag            local docker tag")
	fmt.Fprintln(env.Stderr, "  image.build_context_path   path to docker build context")
	fmt.Fprintln(env.Stderr, "  image.pinned_id            sha256 id of the pinned image (set by init/update)")
	fmt.Fprintln(env.Stderr, "  image.previous_id          sha256 id of the previous image (for --rollback)")
	fmt.Fprintln(env.Stderr, "  model.default              default model name (e.g. claude-sonnet-4-6)")
}
