package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/agentctl/agentctl/internal/config"
	"github.com/agentctl/agentctl/internal/secrets"
)

// authImageTag is the local tag for the one-shot login helper image built
// from image/auth.Dockerfile. Kept separate from cfg.Image.LocalTag so
// updating the session base image doesn't churn this one (and vice versa).
const authImageTag = "agentctl/auth:local"

func runAuth(ctx context.Context, env *Env, args []string) int {
	if len(args) == 0 {
		return authHelp(env, ExitUsage)
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "login":
		return runAuthLogin(ctx, env, rest)
	case "status":
		return runAuthStatus(env)
	case "-h", "--help", "help":
		return authHelp(env, ExitOK)
	}
	fmt.Fprintf(env.Stderr, "agentctl auth: unknown subcommand %q\n\n", sub)
	return authHelp(env, ExitUsage)
}

func authHelp(env *Env, code int) int {
	w := env.Stderr
	if code == ExitOK {
		w = env.Stdout
	}
	fmt.Fprintln(w, "Usage: agentctl auth <subcommand> [flags]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Subcommands:")
	fmt.Fprintln(w, "  login    Run `claude auth login` inside a one-shot container; store credentials")
	fmt.Fprintln(w, "           under ~/.config/agentctl/claude/.credentials.json and switch sessions")
	fmt.Fprintln(w, "           to subscription auth.")
	fmt.Fprintln(w, "  status   Print whether sessions are configured to use an API key or OAuth.")
	return code
}

func runAuthStatus(env *Env) int {
	sec, err := secrets.Load(env.Layout.SecretsFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(env.Stderr, "auth status: %v\n", err)
		return ExitGeneric
	}
	mode := sec.ResolvedAuthMode()
	fmt.Fprintf(env.Stdout, "anthropic auth mode: %s\n", mode)
	switch mode {
	case secrets.AuthModeOAuth:
		credFile := env.Layout.ClaudeCredsFile
		if info, statErr := os.Stat(credFile); statErr == nil && info.Size() > 0 {
			fmt.Fprintf(env.Stdout, "credentials file:    %s\n", credFile)
		} else {
			fmt.Fprintf(env.Stdout, "credentials file:    %s (missing — run `agentctl auth login`)\n", credFile)
		}
	case secrets.AuthModeAPIKey:
		if sec.AnthropicAPIKey != "" {
			fmt.Fprintln(env.Stdout, "anthropic api key:   set (run `agentctl init --reset-token anthropic` to replace)")
		} else {
			fmt.Fprintln(env.Stdout, "anthropic api key:   NOT set (run `agentctl init` or `agentctl auth login`)")
		}
	}
	return ExitOK
}

func runAuthLogin(ctx context.Context, env *Env, args []string) int {
	fs := flag.NewFlagSet("auth login", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	skipBuild := fs.Bool("skip-build", false, "reuse existing auth helper image; skip `docker build`")
	noCache := fs.Bool("no-cache", false, "pass --no-cache to the auth helper image build")
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: agentctl auth login [flags]")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "Runs `claude auth login` inside a one-shot Linux container so the OAuth")
		fmt.Fprintln(env.Stderr, "flow writes credentials into ~/.config/agentctl/claude/ on the host,")
		fmt.Fprintln(env.Stderr, "instead of touching ~/.claude or the macOS Keychain.")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "On success, secrets.json is updated to anthropic_auth_mode=oauth and")
		fmt.Fprintln(env.Stderr, "subsequent `agentctl start` sessions bind-mount the credentials into")
		fmt.Fprintln(env.Stderr, "the container instead of injecting ANTHROPIC_API_KEY.")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "The container has no reachable callback port, so Claude Code falls back")
		fmt.Fprintln(env.Stderr, "to the paste flow: open the URL it prints on your host browser, sign in,")
		fmt.Fprintln(env.Stderr, "then paste the code Anthropic shows back into this terminal.")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "Flags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}

	if _, err := exec.LookPath("docker"); err != nil {
		fmt.Fprintln(env.Stderr, "auth login: docker not on PATH; install Docker Desktop / Engine")
		return ExitEnvironment
	}

	credsDir := env.Layout.ClaudeCredsDir
	if err := os.MkdirAll(credsDir, secrets.DirPerm); err != nil {
		fmt.Fprintf(env.Stderr, "auth login: mkdir %s: %v\n", credsDir, err)
		return ExitEnvironment
	}

	if !*skipBuild {
		if err := ensureAuthImage(ctx, env, *noCache); err != nil {
			fmt.Fprintf(env.Stderr, "auth login: %v\n", err)
			return ExitEnvironment
		}
	}

	runArgs := []string{
		"run", "--rm", "-it",
		"-v", credsDir + ":/creds",
		authImageTag,
	}
	cmd := exec.CommandContext(ctx, "docker", runArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = env.Stdout
	cmd.Stderr = env.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(env.Stderr, "auth login: docker run: %v\n", err)
		return ExitRuntime
	}

	credFile := env.Layout.ClaudeCredsFile
	info, err := os.Stat(credFile)
	if err != nil || info.Size() == 0 {
		fmt.Fprintf(env.Stderr, "auth login: %s was not written; login may have been cancelled\n", credFile)
		return ExitAuth
	}

	if err := persistOAuthMode(env); err != nil {
		fmt.Fprintf(env.Stderr, "auth login: credentials saved but failed to update secrets.json: %v\n", err)
		return ExitGeneric
	}

	fmt.Fprintf(env.Stdout, "Claude credentials saved to %s\n", credFile)
	fmt.Fprintln(env.Stdout, "Sessions will now authenticate with your Claude subscription.")
	return ExitOK
}

// persistOAuthMode flips secrets.json into oauth mode while preserving every
// other field. We deliberately keep AnthropicAPIKey in place so the user can
// switch back later by editing the mode (or by re-running init with a new
// key). Missing secrets.json is fine — we create one with mode=oauth.
func persistOAuthMode(env *Env) error {
	sec, err := secrets.Load(env.Layout.SecretsFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	sec.AnthropicAuthMode = secrets.AuthModeOAuth
	return secrets.Save(env.Layout.SecretsFile, sec)
}

func ensureAuthImage(ctx context.Context, env *Env, noCache bool) error {
	cfg := loadOrDefaultConfig(env.Layout.ConfigFile)
	contextPath := config.ExpandHome(cfg.Image.BuildContextPath)
	if contextPath == "" {
		contextPath = env.Layout.ImageDir
	}
	dockerfile := filepath.Join(contextPath, "auth.Dockerfile")
	if _, err := os.Stat(dockerfile); err != nil {
		return fmt.Errorf("auth.Dockerfile missing at %s; re-run installer to refresh the image build context", dockerfile)
	}
	fmt.Fprintf(env.Stdout, "Building auth helper image %s from %s ...\n", authImageTag, contextPath)
	buildArgs := []string{"build", "-t", authImageTag, "-f", dockerfile}
	if noCache {
		buildArgs = append(buildArgs, "--no-cache")
	}
	buildArgs = append(buildArgs, contextPath)
	cmd := exec.CommandContext(ctx, "docker", buildArgs...)
	cmd.Stdout = env.Stdout
	cmd.Stderr = env.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker build auth helper image: %w", err)
	}
	return nil
}
