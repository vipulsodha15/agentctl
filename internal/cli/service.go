package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/agentctl/agentctl/internal/service"
)

func runService(_ context.Context, env *Env, args []string) int {
	if len(args) == 0 {
		serviceUsage(env)
		return ExitUsage
	}
	sub := args[0]
	switch sub {
	case "status":
		return runServiceStatus(env)
	case "start":
		return runServiceStart(env)
	case "stop":
		return runServiceStop(env)
	case "restart":
		return runServiceRestart(env)
	case "-h", "--help", "help":
		serviceUsage(env)
		return ExitOK
	default:
		fmt.Fprintf(env.Stderr, "agentctl service: unknown subcommand %q\n\n", sub)
		serviceUsage(env)
		return ExitUsage
	}
}

func serviceUsage(env *Env) {
	fmt.Fprintln(env.Stderr, "Usage: agentctl service <subcommand>")
	fmt.Fprintln(env.Stderr, "")
	fmt.Fprintln(env.Stderr, "Control the agentd OS service unit (launchd on macOS, systemd --user on Linux).")
	fmt.Fprintln(env.Stderr, "")
	fmt.Fprintln(env.Stderr, "Subcommands:")
	fmt.Fprintln(env.Stderr, "  status     Show whether agentd is running and where its unit file lives.")
	fmt.Fprintln(env.Stderr, "  start      Start the agentd service.")
	fmt.Fprintln(env.Stderr, "  stop       Stop the agentd service.")
	fmt.Fprintln(env.Stderr, "  restart    Restart the agentd service (e.g. after rebuilding the binary).")
}

func runServiceStatus(env *Env) int {
	mgr := service.New(env.Layout.Home)
	active, err := mgr.IsActive()
	if err != nil && !errors.Is(err, service.ErrUnsupportedPlatform) {
		fmt.Fprintf(env.Stderr, "service: status: %v\n", err)
		return ExitRuntime
	}
	state := "stopped"
	if active {
		state = "running"
	}
	fmt.Fprintf(env.Stdout, "platform: %s\n", mgr.Platform())
	fmt.Fprintf(env.Stdout, "state:    %s\n", state)
	if p := mgr.UnitPath(); p != "" {
		fmt.Fprintf(env.Stdout, "unit:     %s\n", p)
	}
	if errors.Is(err, service.ErrUnsupportedPlatform) {
		fmt.Fprintln(env.Stderr, "note: service control is not supported on this platform.")
		return ExitEnvironment
	}
	return ExitOK
}

func runServiceStart(env *Env) int {
	mgr := service.New(env.Layout.Home)
	if err := mgr.Start(); err != nil {
		fmt.Fprintf(env.Stderr, "service: start: %v\n", err)
		return ExitRuntime
	}
	fmt.Fprintln(env.Stdout, "agentd started")
	return ExitOK
}

func runServiceStop(env *Env) int {
	mgr := service.New(env.Layout.Home)
	if err := mgr.Stop(); err != nil {
		fmt.Fprintf(env.Stderr, "service: stop: %v\n", err)
		return ExitRuntime
	}
	fmt.Fprintln(env.Stdout, "agentd stopped")
	return ExitOK
}

func runServiceRestart(env *Env) int {
	mgr := service.New(env.Layout.Home)
	// On darwin, Manager.Start() is `launchctl kickstart -k` which already
	// kills any running instance and starts a fresh one — that's restart
	// semantics in a single call. On linux, `systemctl start` is start-only,
	// so we need an explicit stop+start; ignore the stop error so a restart
	// from a stopped state still works.
	if mgr.Platform() != "darwin" {
		_ = mgr.Stop()
	}
	if err := mgr.Start(); err != nil {
		fmt.Fprintf(env.Stderr, "service: restart: %v\n", err)
		return ExitRuntime
	}
	fmt.Fprintln(env.Stdout, "agentd restarted")
	return ExitOK
}
