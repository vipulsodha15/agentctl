package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/agentctl/agentctl/internal/paths"
	"github.com/agentctl/agentctl/internal/version"
)

type Command struct {
	Name    string
	Summary string
	Run     func(ctx context.Context, env *Env, args []string) int
}

type Env struct {
	Layout paths.Layout
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
}

func DefaultEnv() *Env {
	return &Env{
		Layout: paths.Resolve(),
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Stdin:  os.Stdin,
	}
}

const (
	ExitOK           = 0
	ExitGeneric      = 1
	ExitEnvironment  = 2
	ExitAuth         = 3
	ExitSessionState = 4
	ExitRuntime      = 5
	ExitUsage        = 64
)

func commands() []Command {
	return []Command{
		{Name: "init", Summary: "Set up agentctl on this machine.", Run: runInit},
		{Name: "update", Summary: "Rebuild the session base image and repin its id.", Run: runUpdate},
		{Name: "config", Summary: "Read or write a config.toml key.", Run: runConfig},
		{Name: "doctor", Summary: "Run install + connectivity checks.", Run: runDoctor},
		{Name: "start", Summary: "Create a session and attach to its event stream.", Run: runStart},
		{Name: "attach", Summary: "Attach to a running session's event stream.", Run: runAttach},
		{Name: "detach", Summary: "Help text: detach is a client-side action (Ctrl-D / Ctrl-C).", Run: runDetach},
		{Name: "ls", Summary: "List sessions.", Run: runLs},
		{Name: "stop", Summary: "Terminate a session and remove its container + volume.", Run: runStop},
		{Name: "interrupt", Summary: "Cancel a session's in-flight turn.", Run: runInterrupt},
		{Name: "logs", Summary: "Tail daemon or session logs.", Run: runLogs},
		{Name: "ui", Summary: "Open the local Web UI in a browser.", Run: runUI},
		{Name: "version", Summary: "Print version info.", Run: runVersion},
		{Name: "help", Summary: "Show this help.", Run: runHelp},
	}
}

func Dispatch(ctx context.Context, args []string) int {
	env := DefaultEnv()
	if len(args) == 0 {
		return runHelp(ctx, env, nil)
	}
	name := args[0]
	rest := args[1:]
	switch name {
	case "--help", "-h":
		return runHelp(ctx, env, nil)
	case "--version", "-v":
		return runVersion(ctx, env, nil)
	}
	for _, c := range commands() {
		if c.Name == name {
			return c.Run(ctx, env, rest)
		}
	}
	fmt.Fprintf(env.Stderr, "agentctl: unknown command %q\n\n", name)
	runHelp(ctx, env, nil)
	return ExitUsage
}

func runVersion(_ context.Context, env *Env, _ []string) int {
	fmt.Fprintf(env.Stdout, "agentctl %s (build %s)\n", version.Version, version.Build)
	return ExitOK
}

func runHelp(_ context.Context, env *Env, _ []string) int {
	cmds := commands()
	sort.Slice(cmds, func(i, j int) bool { return cmds[i].Name < cmds[j].Name })
	fmt.Fprintln(env.Stdout, "agentctl — local AI coding-agent sessions")
	fmt.Fprintln(env.Stdout, "")
	fmt.Fprintln(env.Stdout, "Usage: agentctl <command> [flags]")
	fmt.Fprintln(env.Stdout, "")
	fmt.Fprintln(env.Stdout, "Commands:")
	for _, c := range cmds {
		fmt.Fprintf(env.Stdout, "  %-10s  %s\n", c.Name, c.Summary)
	}
	fmt.Fprintln(env.Stdout, "")
	fmt.Fprintln(env.Stdout, "Run `agentctl <command> --help` for command-specific flags.")
	return ExitOK
}
