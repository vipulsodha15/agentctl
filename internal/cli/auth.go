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
	"github.com/agentctl/agentctl/internal/paths"
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
	fmt.Fprintln(w, "  login    Run the vendor OAuth flow inside a one-shot container; store")
	fmt.Fprintln(w, "           credentials under ~/.config/agentctl/<provider>/ and switch")
	fmt.Fprintln(w, "           sessions to subscription auth. Use --provider {anthropic|openai}")
	fmt.Fprintln(w, "           when both providers are configured (defaults to anthropic when")
	fmt.Fprintln(w, "           only one provider has credentials, for backward compatibility).")
	fmt.Fprintln(w, "  status   Print whether sessions are configured to use an API key or OAuth")
	fmt.Fprintln(w, "           for each enabled provider.")
	return code
}

// authStatusLine is a single provider's status row for runAuthStatus. The
// two-provider table prints these in lexical order (anthropic, openai)
// for stable test output.
type authStatusLine struct {
	provider string
	mode     string
	detail   string
	warning  string
}

func runAuthStatus(env *Env) int {
	sec, err := secrets.Load(env.Layout.SecretsFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(env.Stderr, "auth status: %v\n", err)
		return ExitGeneric
	}

	enabled := sec.EnabledProviders(func(p string) string {
		switch p {
		case secrets.ProviderAnthropic:
			return env.Layout.ClaudeCredsFile
		case secrets.ProviderOpenAI:
			return env.Layout.CodexCredsFile
		}
		return ""
	})

	// Provider-invisibility: when 0 or 1 providers are configured, keep the
	// historical single-line shape unchanged (ADR 0020 §UX principles).
	// The output is labelled with whichever provider is actually enabled —
	// historically Anthropic was the only option, but a fresh install of
	// just OpenAI must print openai labels, not anthropic ones. Zero-
	// enabled defaults to anthropic so the run-init hint is correctly
	// anthropic-shaped (the historical first-run experience).
	if len(enabled) <= 1 {
		p := secrets.ProviderAnthropic
		if len(enabled) == 1 {
			p = enabled[0]
		}
		switch p {
		case secrets.ProviderOpenAI:
			mode := sec.ResolvedOpenAIAuthMode()
			fmt.Fprintf(env.Stdout, "openai auth mode:    %s\n", mode)
			switch mode {
			case secrets.AuthModeOAuth:
				credFile := env.Layout.CodexCredsFile
				if info, statErr := os.Stat(credFile); statErr == nil && info.Size() > 0 {
					fmt.Fprintf(env.Stdout, "credentials file:    %s\n", credFile)
				} else {
					fmt.Fprintf(env.Stdout, "credentials file:    %s (missing — run `agentctl auth login --provider openai`)\n", credFile)
				}
			case secrets.AuthModeAPIKey:
				switch {
				case sec.OpenAIBaseURL != "" && sec.OpenAIAuthToken != "":
					fmt.Fprintf(env.Stdout, "openai endpoint:     %s (OPENAI_AUTH_TOKEN set)\n", sec.OpenAIBaseURL)
				case sec.OpenAIAPIKey != "":
					fmt.Fprintln(env.Stdout, "openai api key:      set (run `agentctl init --reset-token openai` to replace)")
				default:
					fmt.Fprintln(env.Stdout, "openai api key:      NOT set (run `agentctl init` or `agentctl auth login --provider openai`)")
				}
			}
		default:
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
		}
		return ExitOK
	}

	// Both providers configured — print a side-by-side table. The lines
	// here are short and aligned so a wide terminal renders one row per
	// provider; pipe-friendly callers can grep by the leading provider id.
	rows := []authStatusLine{
		buildAnthropicStatus(sec, env.Layout),
		buildOpenAIStatus(sec, env.Layout),
	}
	for _, r := range rows {
		fmt.Fprintf(env.Stdout, "%-10s %-8s %s\n", r.provider, r.mode, r.detail)
		if r.warning != "" {
			fmt.Fprintf(env.Stdout, "%-10s          warning: %s\n", "", r.warning)
		}
	}
	return ExitOK
}

func buildAnthropicStatus(sec secrets.Secrets, layout paths.Layout) authStatusLine {
	line := authStatusLine{provider: "anthropic"}
	switch sec.ResolvedAuthMode() {
	case secrets.AuthModeOAuth:
		line.mode = "oauth"
		credFile := layout.ClaudeCredsFile
		if info, err := os.Stat(credFile); err == nil && info.Size() > 0 {
			line.detail = fmt.Sprintf("(creds: %s)", credFile)
		} else {
			line.detail = fmt.Sprintf("(creds: %s missing — re-run `agentctl auth login --provider anthropic`)", credFile)
		}
	case secrets.AuthModeAPIKey:
		switch {
		case sec.AnthropicBaseURL != "" && sec.AnthropicAuthToken != "":
			line.mode = "endpoint"
			line.detail = fmt.Sprintf("(base_url=%s, ANTHROPIC_AUTH_TOKEN set)", sec.AnthropicBaseURL)
		case sec.AnthropicAPIKey != "":
			line.mode = "api_key"
			line.detail = "(ANTHROPIC_API_KEY set in secrets.json)"
		default:
			line.mode = "unset"
			line.detail = "(no anthropic credentials)"
		}
	}
	return line
}

func buildOpenAIStatus(sec secrets.Secrets, layout paths.Layout) authStatusLine {
	line := authStatusLine{provider: "openai"}
	// Custom-endpoint takes precedence over OAuth per ADR 0020 §Items to
	// verify / CODEX_PROVIDER_PLAN §5.2 — the ChatGPT OAuth flow doesn't
	// route through a third-party gateway, so if both are set the
	// gateway wins and we surface the override.
	customEndpoint := sec.OpenAIBaseURL != "" && sec.OpenAIAuthToken != ""
	oauth := sec.ResolvedOpenAIAuthMode() == secrets.AuthModeOAuth
	switch {
	case customEndpoint:
		line.mode = "endpoint"
		line.detail = fmt.Sprintf("(base_url=%s, OPENAI_AUTH_TOKEN set)", sec.OpenAIBaseURL)
		if oauth {
			line.warning = "OAuth credentials present but ignored; custom endpoint takes precedence"
		}
	case oauth:
		line.mode = "oauth"
		credFile := layout.CodexCredsFile
		if info, err := os.Stat(credFile); err == nil && info.Size() > 0 {
			line.detail = fmt.Sprintf("(creds: %s)", credFile)
		} else {
			line.detail = fmt.Sprintf("(creds: %s missing — re-run `agentctl auth login --provider openai`)", credFile)
		}
	case sec.OpenAIAPIKey != "":
		line.mode = "api_key"
		line.detail = "(OPENAI_API_KEY set in secrets.json)"
	default:
		line.mode = "unset"
		line.detail = "(no openai credentials)"
	}
	return line
}

// providerLoginConfig captures the per-provider knobs the login helper
// needs: where to bind-mount creds, which Docker ARG to pass, and the
// credentials file to check after the helper exits.
type providerLoginConfig struct {
	provider     string
	credsDir     string
	credsFile    string
	expectMsg    string
	loggedInHint string
}

func runAuthLogin(ctx context.Context, env *Env, args []string) int {
	fs := flag.NewFlagSet("auth login", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	skipBuild := fs.Bool("skip-build", false, "reuse existing auth helper image; skip `docker build`")
	noCache := fs.Bool("no-cache", false, "pass --no-cache to the auth helper image build")
	provider := fs.String("provider", "", "vendor to authenticate against: anthropic|openai (defaults to anthropic when only one provider is configured)")
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: agentctl auth login [flags]")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "Runs the vendor OAuth flow inside a one-shot Linux container so")
		fmt.Fprintln(env.Stderr, "credentials land under ~/.config/agentctl/<provider>/ on the host,")
		fmt.Fprintln(env.Stderr, "instead of touching ~/.claude or the macOS Keychain.")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "On success, secrets.json is updated to <provider>_auth_mode=oauth and")
		fmt.Fprintln(env.Stderr, "subsequent `agentctl start` sessions bind-mount the credentials into")
		fmt.Fprintln(env.Stderr, "the container instead of injecting the API key.")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "The container has no reachable callback port, so both vendors fall")
		fmt.Fprintln(env.Stderr, "back to a paste/device-code flow: open the URL on your host browser,")
		fmt.Fprintln(env.Stderr, "sign in, then paste/enter the code back into this terminal.")
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

	cfg, err := resolveLoginProvider(env, *provider)
	if err != nil {
		fmt.Fprintf(env.Stderr, "auth login: %v\n", err)
		return ExitUsage
	}

	if cfg.provider == secrets.ProviderAnthropic {
		printAnthropicSubscriptionBanWarning(env)
	}

	if err := os.MkdirAll(cfg.credsDir, secrets.DirPerm); err != nil {
		fmt.Fprintf(env.Stderr, "auth login: mkdir %s: %v\n", cfg.credsDir, err)
		return ExitEnvironment
	}
	// The auth container runs as uid 1000 (node user) and writes the
	// credentials file into the bind-mounted /creds. If the host user is
	// uid 1000, chown is a no-op; otherwise we fall back to 0777 so the
	// container user can still write. Mirrors sm/manager.go's volumeDir
	// handling for the same cross-platform reason.
	if err := os.Chown(cfg.credsDir, 1000, 1000); err != nil {
		_ = os.Chmod(cfg.credsDir, 0o777)
	}

	if !*skipBuild {
		if err := ensureAuthImage(ctx, env, *noCache); err != nil {
			fmt.Fprintf(env.Stderr, "auth login: %v\n", err)
			return ExitEnvironment
		}
	}

	runArgs := []string{
		"run", "--rm", "-it",
		"-e", "PROVIDER=" + cfg.provider,
		"-v", cfg.credsDir + ":/creds",
		authImageTag,
	}
	if err := runDocker(ctx, env, runArgs); err != nil {
		fmt.Fprintf(env.Stderr, "auth login: docker run: %v\n", err)
		return ExitRuntime
	}

	info, err := os.Stat(cfg.credsFile)
	if err != nil || info.Size() == 0 {
		fmt.Fprintf(env.Stderr, "auth login: %s was not written; login may have been cancelled\n", cfg.credsFile)
		return ExitAuth
	}

	if err := persistOAuthMode(env, cfg.provider); err != nil {
		fmt.Fprintf(env.Stderr, "auth login: credentials saved but failed to update secrets.json: %v\n", err)
		return ExitGeneric
	}

	fmt.Fprintf(env.Stdout, "%s credentials saved to %s\n", cfg.provider, cfg.credsFile)
	fmt.Fprintln(env.Stdout, cfg.loggedInHint)
	return ExitOK
}

// runDocker exists so tests can swap in a stub. dockerRunner is a package-
// level seam initialised to the real exec; tests reset it via
// setDockerRunner.
var dockerRunner = func(ctx context.Context, env *Env, args []string) error {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = env.Stdout
	cmd.Stderr = env.Stderr
	return cmd.Run()
}

func runDocker(ctx context.Context, env *Env, args []string) error {
	return dockerRunner(ctx, env, args)
}

// resolveLoginProvider picks the provider for `agentctl auth login`. When
// --provider is explicit, that wins. When omitted, we keep the historical
// Anthropic-only behaviour intact (ADR 0020 §UX principles): pick the
// single enabled provider if there's exactly one, or fall back to
// anthropic so the byte-for-byte old flow still works on a fresh install
// with neither provider configured yet.
func resolveLoginProvider(env *Env, requested string) (providerLoginConfig, error) {
	switch requested {
	case secrets.ProviderAnthropic, secrets.ProviderOpenAI:
		return loginConfigFor(env, requested), nil
	case "":
		// Inspect secrets to choose a sensible default without forcing the
		// user to pass --provider on a fresh install.
		sec, _ := secrets.Load(env.Layout.SecretsFile)
		enabled := sec.EnabledProviders(func(p string) string {
			switch p {
			case secrets.ProviderAnthropic:
				return env.Layout.ClaudeCredsFile
			case secrets.ProviderOpenAI:
				return env.Layout.CodexCredsFile
			}
			return ""
		})
		if len(enabled) >= 2 {
			return providerLoginConfig{}, fmt.Errorf("multiple providers configured (%v); pass --provider {anthropic|openai}", enabled)
		}
		return loginConfigFor(env, secrets.ProviderAnthropic), nil
	default:
		return providerLoginConfig{}, fmt.Errorf("--provider must be anthropic or openai (got %q)", requested)
	}
}

func loginConfigFor(env *Env, provider string) providerLoginConfig {
	switch provider {
	case secrets.ProviderOpenAI:
		return providerLoginConfig{
			provider:     secrets.ProviderOpenAI,
			credsDir:     env.Layout.CodexCredsDir,
			credsFile:    env.Layout.CodexCredsFile,
			expectMsg:    "auth.json",
			loggedInHint: "Sessions will now authenticate with your ChatGPT subscription via Codex.",
		}
	default:
		return providerLoginConfig{
			provider:     secrets.ProviderAnthropic,
			credsDir:     env.Layout.ClaudeCredsDir,
			credsFile:    env.Layout.ClaudeCredsFile,
			expectMsg:    ".credentials.json",
			loggedInHint: "Sessions will now authenticate with your Claude subscription.",
		}
	}
}

// persistOAuthMode flips secrets.json into oauth mode for the given
// provider while preserving every other field. We deliberately keep the
// existing API key in place so the user can switch back later by editing
// the mode (or by re-running init with a new key). Missing secrets.json
// is fine — we create one with mode=oauth.
func persistOAuthMode(env *Env, provider string) error {
	sec, err := secrets.Load(env.Layout.SecretsFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	switch provider {
	case secrets.ProviderOpenAI:
		sec.OpenAIAuthMode = secrets.AuthModeOAuth
	default:
		sec.AnthropicAuthMode = secrets.AuthModeOAuth
	}
	return secrets.Save(env.Layout.SecretsFile, sec)
}

// printAnthropicSubscriptionBanWarning surfaces the ToS / ban-risk notice
// before the OAuth flow launches. Anthropic has banned both individual and
// team-plan accounts for routing Claude subscription credentials through
// third-party clients; on a team plan the entire org is at risk, not just
// the signing-in user. Printed to stderr so it stays visible even if stdout
// is being piped.
func printAnthropicSubscriptionBanWarning(env *Env) {
	w := env.Stderr
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "========================================================================")
	fmt.Fprintln(w, "WARNING: Anthropic subscription / team plan ban risk")
	fmt.Fprintln(w, "========================================================================")
	fmt.Fprintln(w, "You are about to sign in with your Claude subscription (Pro / Max /")
	fmt.Fprintln(w, "Team / Enterprise). Using subscription credentials with third-party")
	fmt.Fprintln(w, "tools like agentctl may violate Anthropic's Terms of Service.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Anthropic has banned accounts — including TEAM PLAN accounts — for")
	fmt.Fprintln(w, "routing subscription auth through unofficial clients. If you sign in")
	fmt.Fprintln(w, "with a team plan account, you risk getting the ENTIRE TEAM PLAN")
	fmt.Fprintln(w, "banned, not just your user.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "If you are unsure, cancel now (Ctrl-C) and use an API key billed to a")
	fmt.Fprintln(w, "separate account via `agentctl init` instead.")
	fmt.Fprintln(w, "========================================================================")
	fmt.Fprintln(w, "")
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
