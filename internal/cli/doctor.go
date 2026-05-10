package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"

	"github.com/agentctl/agentctl/internal/config"
	"github.com/agentctl/agentctl/internal/doctor"
)

func runDoctor(_ context.Context, env *Env, args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	asJSON := fs.Bool("json", false, "emit JSON for scripting")
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: agentctl doctor [--json]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	cfg, _ := config.Load(env.Layout.ConfigFile)
	opts := doctor.DefaultPaths(env.Layout.Home)
	if cfg.Agentd.WebAddr != "" {
		opts.WebAddr = cfg.Agentd.WebAddr
	}
	res := doctor.Run(opts)
	if *asJSON {
		_ = json.NewEncoder(env.Stdout).Encode(res)
	} else {
		printDoctor(env, res)
	}
	if res.HasFailures() {
		return ExitRuntime
	}
	return ExitOK
}

func printDoctor(env *Env, res doctor.Result) {
	fmt.Fprintln(env.Stdout, "agentctl doctor")
	for _, c := range res.Checks {
		marker := "ok  "
		switch c.Status {
		case doctor.StatusFail:
			marker = "FAIL"
		case doctor.StatusWarn:
			marker = "warn"
		case doctor.StatusSkip:
			marker = "skip"
		}
		fmt.Fprintf(env.Stdout, "  %-22s %s  %s\n", c.Name, marker, c.Message)
		if c.Detail != "" {
			fmt.Fprintf(env.Stdout, "                         %s\n", c.Detail)
		}
	}
}
