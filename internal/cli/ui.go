package cli

import (
	"context"
	"fmt"

	"github.com/agentctl/agentctl/internal/config"
	"github.com/agentctl/agentctl/internal/secrets"
	"github.com/agentctl/agentctl/internal/ui"
)

func runUI(_ context.Context, env *Env, args []string) int {
	for _, a := range args {
		if a == "--help" || a == "-h" || a == "help" {
			fmt.Fprintln(env.Stderr, "Usage: agentctl ui")
			fmt.Fprintln(env.Stderr, "")
			fmt.Fprintln(env.Stderr, "Opens the local Web UI in the default browser. The URL includes the")
			fmt.Fprintln(env.Stderr, "agentd web bearer token in the URL fragment so the SPA can authenticate")
			fmt.Fprintln(env.Stderr, "without exposing the token to the network.")
			return ExitOK
		}
	}
	cfg, err := config.Load(env.Layout.ConfigFile)
	if err != nil {
		fmt.Fprintf(env.Stderr, "ui: %v\n", err)
		return ExitEnvironment
	}
	tok, err := secrets.ReadWebToken(env.Layout.WebTokenFile)
	if err != nil {
		fmt.Fprintf(env.Stderr, "ui: web_token missing (run `agentctl init`): %v\n", err)
		return ExitEnvironment
	}
	target := ui.URLForToken(cfg.Agentd.WebAddr, tok)
	fmt.Fprintf(env.Stdout, "Opening %s\n", redactURL(target))
	if err := ui.Open(target); err != nil {
		fmt.Fprintf(env.Stderr, "ui: %v\n", err)
		return ExitEnvironment
	}
	return ExitOK
}

func redactURL(u string) string {
	for i := len(u) - 1; i >= 0; i-- {
		if u[i] == '#' {
			return u[:i+1] + "<token>"
		}
	}
	return u
}
