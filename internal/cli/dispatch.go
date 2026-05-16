package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/agentctl/agentctl/internal/paths"
	"github.com/agentctl/agentctl/internal/version"
)

type Command struct {
	Name    string
	Group   string
	Summary string
	Run     func(ctx context.Context, env *Env, args []string) int
}

const (
	groupSetup         = "Setup"
	groupSessions      = "Sessions"
	groupMCPs          = "MCPs"
	groupSkills        = "Skills"
	groupAssemblyLines = "Assembly Lines"
	groupDiagnostics   = "Diagnostics"
	groupMisc          = "Misc"
)

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
		{Name: "init", Group: groupSetup, Summary: "Set up agentctl on this machine (build image, prompt for tokens, install service).", Run: runInit},
		{Name: "auth", Group: groupSetup, Summary: "Manage Anthropic credentials (auth login runs `claude auth login` in a one-shot container).", Run: runAuth},
		{Name: "update", Group: groupSetup, Summary: "Rebuild the session base image and repin its id.", Run: runUpdate},
		{Name: "config", Group: groupSetup, Summary: "Read or write a config.toml key.", Run: runConfig},
		{Name: "ui", Group: groupSetup, Summary: "Open the local Web UI in a browser.", Run: runUI},
		{Name: "service", Group: groupSetup, Summary: "Control the agentd OS service (status/start/stop/restart).", Run: runService},

		{Name: "start", Group: groupSessions, Summary: "Create a session and attach to its event stream.", Run: runStart},
		{Name: "attach", Group: groupSessions, Summary: "Attach to a running session's event stream.", Run: runAttach},
		{Name: "detach", Group: groupSessions, Summary: "Help text: detach is a client-side action (Ctrl-D / Ctrl-C).", Run: runDetach},
		{Name: "ls", Group: groupSessions, Summary: "List sessions.", Run: runLs},
		{Name: "stop", Group: groupSessions, Summary: "Terminate a session and remove its container + volume.", Run: runStop},
		{Name: "restart", Group: groupSessions, Summary: "Recreate a session container from the currently pinned image.", Run: runRestart},
		{Name: "interrupt", Group: groupSessions, Summary: "Cancel a session's in-flight turn.", Run: runInterrupt},
		{Name: "logs", Group: groupSessions, Summary: "Tail daemon, session, or container logs.", Run: runLogs},
		{Name: "diff", Group: groupSessions, Summary: "Stream the working-tree diff against the recorded base SHA.", Run: runDiff},
		{Name: "export", Group: groupSessions, Summary: "Export a patch (--patch) or push to a branch (--push <branch>).", Run: runExport},
		// `session` is the scripting-surface subcommand for session mutations
		// that the primary UX (web header / chat-input slash commands) covers
		// for interactive users. Today: `session set-model` (ADR 0020 §2).
		// Per the ADR's UX principles, this is intentionally lower-key than
		// the discoverable surfaces — see internal/cli/session.go.
		{Name: "session", Group: groupSessions, Summary: "Scripting subcommands for session mutations (set-model).", Run: runSession},

		{Name: "mcp", Group: groupMCPs, Summary: "Manage the MCP registry (list/add/update/remove/set-default).", Run: runMCP},

		{Name: "skill", Group: groupSkills, Summary: "Manage skills (list/new/add/edit/remove/validate/show/export/import).", Run: runSkill},

		{Name: "agent", Group: groupAssemblyLines, Summary: "Manage agent definitions (ls/show/add).", Run: runAgent},
		{Name: "assembly-line", Group: groupAssemblyLines, Summary: "Inspect assembly line definitions (ls/show).", Run: runAssemblyLine},
		{Name: "task", Group: groupAssemblyLines, Summary: "Manage tasks (ls/create/show/handoff/complete/abandon).", Run: runTask},

		{Name: "cost", Group: groupDiagnostics, Summary: "Show per-session or aggregate Anthropic API spend.", Run: runCost},
		{Name: "doctor", Group: groupDiagnostics, Summary: "Run install + connectivity checks (--fix, --repair-db, --json).", Run: runDoctor},

		{Name: "version", Group: groupMisc, Summary: "Print version info.", Run: runVersion},
		{Name: "help", Group: groupMisc, Summary: "Show this help.", Run: runHelp},
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
	fmt.Fprintln(env.Stdout, "agentctl - local AI coding-agent sessions")
	fmt.Fprintln(env.Stdout, "")
	fmt.Fprintln(env.Stdout, "Usage: agentctl <command> [flags]")
	fmt.Fprintln(env.Stdout, "")
	groups := []string{groupSetup, groupSessions, groupMCPs, groupSkills, groupAssemblyLines, groupDiagnostics, groupMisc}
	cmds := commands()
	for _, g := range groups {
		fmt.Fprintf(env.Stdout, "%s\n", g)
		for _, c := range cmds {
			if c.Group != g {
				continue
			}
			fmt.Fprintf(env.Stdout, "  %-10s  %s\n", c.Name, c.Summary)
		}
		fmt.Fprintln(env.Stdout, "")
	}
	fmt.Fprintln(env.Stdout, "Run `agentctl <command> --help` for command-specific flags and examples.")
	fmt.Fprintln(env.Stdout, "Run `agentctl version` to print the build version.")
	return ExitOK
}

// reorderArgs moves long/short flag tokens to the front of the slice so Go's
// stdlib flag.Parse (which stops at the first positional) honors flags placed
// after positional arguments. Knowledge-free heuristic:
//
//   - A token starting with "-" is treated as a flag.
//   - "--flag=value" / "-f=v" is one token, kept together.
//   - "--flag value" / "-f v" treats the next token as the flag's value
//     unless `flag` is a bool — bools never consume the next token.
//
// Without a FlagSet to introspect we conservatively assume any flag whose
// next token does not begin with "-" CONSUMES the next token. That matches
// stdlib flag.Parse exactly when the arg looks like `--name value` and the
// flag is non-bool. The two false positives this can produce are:
//
//  1. "--bool positional" → the positional is mistakenly attributed to the
//     bool flag. Mitigated by writing bool flags as "--bool=true" or by
//     placing positionals last (the historical convention).
//  2. "--str -other" → "--str" gets no value; flag.Parse will then complain.
//
// Both are existing flag-package limitations; this helper just enables the
// common "positional first, flags after" usage pattern that ALL other CLIs
// support.
func reorderArgs(args []string) []string {
	flagsOnly := make([]string, 0, len(args))
	positional := make([]string, 0, len(args))
	i := 0
	for i < len(args) {
		a := args[i]
		if len(a) > 1 && a[0] == '-' {
			flagsOnly = append(flagsOnly, a)
			if !containsRune(a, '=') && i+1 < len(args) && !startsWithDash(args[i+1]) {
				flagsOnly = append(flagsOnly, args[i+1])
				i += 2
				continue
			}
			i++
			continue
		}
		positional = append(positional, a)
		i++
	}
	return append(flagsOnly, positional...)
}

func containsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}

func startsWithDash(s string) bool { return len(s) > 0 && s[0] == '-' }
