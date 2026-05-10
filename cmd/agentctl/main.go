package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/agentctl/agentctl/internal/agentd"
	"github.com/agentctl/agentctl/internal/cli"
	"github.com/agentctl/agentctl/internal/paths"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	progName := filepath.Base(os.Args[0])
	args := os.Args[1:]
	if isDaemonName(progName) || isDaemonArg(args) {
		if hasHelpFlag(args) {
			printDaemonHelp()
			os.Exit(0)
		}
		os.Exit(daemonMain(ctx))
	}
	os.Exit(cli.Dispatch(ctx, args))
}

// isDaemonArg lets the service unit launch the daemon as `agentctl agentd`
// regardless of the binary's path. The "agentd"-named symlink isn't reliable
// because os.Executable() (used to write the unit) resolves symlinks back to
// the agentctl binary, so the unit ends up exec'ing the binary by its real
// name and main() takes the CLI branch instead of daemonMain.
func isDaemonArg(args []string) bool {
	return len(args) > 0 && args[0] == "agentd"
}

func hasHelpFlag(args []string) bool {
	for _, a := range args {
		if a == "--help" || a == "-h" {
			return true
		}
	}
	return false
}

func printDaemonHelp() {
	fmt.Println("agentd — agentctl daemon (single source of truth for sessions)")
	fmt.Println("")
	fmt.Println("Usage: agentd")
	fmt.Println("")
	fmt.Println("agentd is normally launched by systemd --user (Linux) or launchd (macOS).")
	fmt.Println("Run `agentctl init` to install the service unit and start it.")
	fmt.Println("Set AGENTCTL_HOME to override the user home for paths.")
}

func isDaemonName(name string) bool {
	name = strings.ToLower(name)
	if name == "agentd" {
		return true
	}
	return strings.HasSuffix(name, "/agentd") || strings.HasSuffix(name, "agentd.exe")
}

func daemonMain(ctx context.Context) int {
	layout := paths.Resolve()
	if err := agentd.Run(ctx, agentd.Options{Layout: layout}); err != nil {
		fmt.Fprintf(os.Stderr, "agentd: %v\n", err)
		return 1
	}
	return 0
}
