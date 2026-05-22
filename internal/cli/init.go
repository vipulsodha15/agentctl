package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/agentctl/agentctl/internal/agentd"
	"github.com/agentctl/agentctl/internal/cliclient"
	"github.com/agentctl/agentctl/internal/config"
	"github.com/agentctl/agentctl/internal/mcp"
	"github.com/agentctl/agentctl/internal/mcpimport"
	"github.com/agentctl/agentctl/internal/paths"
	"github.com/agentctl/agentctl/internal/proto"
	"github.com/agentctl/agentctl/internal/secrets"
	"github.com/agentctl/agentctl/internal/service"
	"github.com/agentctl/agentctl/internal/skills"
	"github.com/agentctl/agentctl/internal/store"
	"github.com/agentctl/agentctl/internal/update"
	"github.com/agentctl/agentctl/internal/version"
)

type initFlags struct {
	anthropicKey       string
	anthropicBaseURL   string
	anthropicAuthToken string
	// openaiKey + openaiBaseURL / openaiAuthToken mirror the Anthropic
	// shape: either an OPENAI_API_KEY against api.openai.com, or a
	// custom OPENAI_BASE_URL + bearer token for an OpenAI-compatible
	// gateway (Azure OpenAI, vLLM, etc.). Phase 5 of CODEX_PROVIDER_PLAN.
	openaiKey           string
	openaiBaseURL       string
	openaiAuthToken     string
	githubPAT           string
	noImportSkills      bool
	importSkills        bool
	claudePath          string
	noImportCodexSkills bool
	importCodexSkills   bool
	codexPath           string
	noImportClaudeMCPs  bool
	importClaudeMCPs    bool
	claudeMCPPath       string
	noImportCodexMCPs   bool
	importCodexMCPs     bool
	codexMCPPath        string
	foreground          bool
	resetToken          string
	resetWebToken       bool
	repair              bool
	skipBuild           bool
}

func runInit(ctx context.Context, env *Env, args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	var f initFlags
	fs.StringVar(&f.anthropicKey, "anthropic-key", "", "use this Anthropic API key (skip prompt)")
	fs.StringVar(&f.anthropicBaseURL, "anthropic-base-url", "", "use a custom Anthropic-compatible endpoint (e.g. an LLM gateway); requires --anthropic-auth-token")
	fs.StringVar(&f.anthropicAuthToken, "anthropic-auth-token", "", "bearer token sent as Authorization to --anthropic-base-url (alternative to --anthropic-key)")
	fs.StringVar(&f.openaiKey, "openai-key", "", "use this OpenAI API key (enables the OpenAI/Codex provider)")
	fs.StringVar(&f.openaiBaseURL, "openai-base-url", "", "use a custom OpenAI-compatible endpoint (e.g. an LLM gateway); requires --openai-auth-token")
	fs.StringVar(&f.openaiAuthToken, "openai-auth-token", "", "bearer token sent as Authorization to --openai-base-url (alternative to --openai-key)")
	fs.StringVar(&f.githubPAT, "github-pat", "", "use this GitHub PAT (skip prompt)")
	fs.BoolVar(&f.noImportSkills, "no-import-claude-skills", false, "skip the Claude Code skills import step")
	fs.BoolVar(&f.importSkills, "import-claude-skills", false, "force the Claude Code skills import step")
	fs.StringVar(&f.claudePath, "claude-path", "", "override the Claude Code skills source path")
	fs.BoolVar(&f.noImportCodexSkills, "no-import-codex-skills", false, "skip the Codex CLI skills import step")
	fs.BoolVar(&f.importCodexSkills, "import-codex-skills", false, "force the Codex CLI skills import step")
	fs.StringVar(&f.codexPath, "codex-path", "", "override the Codex CLI skills source path")
	fs.BoolVar(&f.noImportClaudeMCPs, "no-import-claude-mcps", false, "skip the Claude Code MCP import step")
	fs.BoolVar(&f.importClaudeMCPs, "import-claude-mcps", false, "force the Claude Code MCP import step")
	fs.StringVar(&f.claudeMCPPath, "claude-mcp-path", "", "override the Claude Code MCP source file (default ~/.claude.json)")
	fs.BoolVar(&f.noImportCodexMCPs, "no-import-codex-mcps", false, "skip the Codex CLI MCP import step")
	fs.BoolVar(&f.importCodexMCPs, "import-codex-mcps", false, "force the Codex CLI MCP import step")
	fs.StringVar(&f.codexMCPPath, "codex-mcp-path", "", "override the Codex CLI MCP source file (default ~/.codex/config.toml)")
	fs.BoolVar(&f.foreground, "foreground", false, "skip system-service install and run agentd in foreground")
	fs.StringVar(&f.resetToken, "reset-token", "", "force re-prompt for a token kind: anthropic|openai|github")
	fs.BoolVar(&f.resetWebToken, "reset-web-token", false, "regenerate the web bearer token")
	fs.BoolVar(&f.repair, "repair", false, "re-run install steps without prompting for tokens")
	fs.BoolVar(&f.skipBuild, "skip-image-build", false, "(test-only) skip the docker build step")
	skipDocker := fs.Bool("skip-docker-check", false, "(test-only) skip the docker info reachability check")
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: agentctl init [flags]")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "First-time setup: verifies Docker, builds the session base image,")
		fmt.Fprintln(env.Stderr, "asks which provider(s) to enable (Anthropic, OpenAI, or both) and how")
		fmt.Fprintln(env.Stderr, "to authenticate each (API key, custom gateway, or OAuth), prompts for")
		fmt.Fprintln(env.Stderr, "GITHUB_PAT, ensures perms on ~/.config/agentctl, seeds the MCP")
		fmt.Fprintln(env.Stderr, "registry, installs the user service, and waits for /healthz to come")
		fmt.Fprintln(env.Stderr, "up. Idempotent: re-run to repair drift.")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "Flags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if *skipDocker {
		_ = os.Setenv("AGENTCTL_SKIP_DOCKER_CHECK", "1")
	}
	if err := initFlow(ctx, env, f); err != nil {
		fmt.Fprintf(env.Stderr, "init: %v\n", err)
		return mapInitError(err)
	}
	return ExitOK
}

type initError struct {
	err  error
	code int
}

func (e *initError) Error() string { return e.err.Error() }
func (e *initError) Unwrap() error { return e.err }

func mapInitError(err error) int {
	var ie *initError
	if errors.As(err, &ie) {
		return ie.code
	}
	return ExitGeneric
}

func wrapInit(code int, err error) error {
	if err == nil {
		return nil
	}
	return &initError{err: err, code: code}
}

func initFlow(ctx context.Context, env *Env, f initFlags) error {
	layout := env.Layout

	if err := ensureDirs(layout); err != nil {
		return wrapInit(ExitGeneric, err)
	}

	if err := dockerCheck(ctx); err != nil {
		return wrapInit(ExitEnvironment, err)
	}

	cfg := loadOrDefaultConfig(layout.ConfigFile)

	if !f.skipBuild {
		if err := buildImage(ctx, env, layout, &cfg); err != nil {
			return wrapInit(ExitEnvironment, err)
		}
	}

	sec, pendingOAuth, err := loadOrInitSecrets(layout, env, f)
	if err != nil {
		return wrapInit(ExitAuth, err)
	}

	if f.resetWebToken {
		if err := os.Remove(layout.WebTokenFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			return wrapInit(ExitGeneric, fmt.Errorf("remove web_token: %w", err))
		}
	}
	if err := ensureWebToken(layout); err != nil {
		return wrapInit(ExitGeneric, err)
	}
	if err := ensureSecretsFile(layout, sec); err != nil {
		return wrapInit(ExitGeneric, err)
	}
	if err := config.Save(layout.ConfigFile, cfg); err != nil {
		return wrapInit(ExitGeneric, err)
	}

	if err := initDB(layout); err != nil {
		return wrapInit(ExitGeneric, err)
	}

	if err := applyRegistrySeed(layout); err != nil {
		return wrapInit(ExitGeneric, err)
	}

	if err := writeInstallMetadata(layout); err != nil {
		return wrapInit(ExitGeneric, err)
	}

	binPath, _ := os.Executable()
	if binPath == "" {
		binPath = filepath.Join(layout.Home, ".local", "bin", "agentctl")
	}

	foreground := f.foreground
	if !foreground {
		mgr := service.New(layout.Home)
		if _, serr := mgr.Install(service.InstallOptions{BinaryPath: binPath, Home: layout.Home}); serr != nil {
			fmt.Fprintf(env.Stderr, "warn: service install failed (%v); falling back to foreground for this session\n", serr)
			foreground = true
		} else if err := mgr.Start(); err != nil {
			fmt.Fprintf(env.Stderr, "warn: service start failed (%v); foreground fallback\n", err)
			foreground = true
		}
	}

	alreadyRunning := foreground && probeHealth(cfg.Agentd.WebAddr)
	var fgErr chan error
	var fgCancel context.CancelFunc
	if foreground && !alreadyRunning {
		var fgCtx context.Context
		fgCtx, fgCancel = context.WithCancel(ctx)
		fgErr = make(chan error, 1)
		go func() { fgErr <- agentd.Run(fgCtx, agentd.Options{Layout: layout}) }()
	}

	if err := waitForHealth(ctx, layout, cfg.Agentd.WebAddr, 30*time.Second); err != nil {
		if fgCancel != nil {
			fgCancel()
		}
		hint := healthHint(foreground, fgErr)
		if hint != "" {
			err = fmt.Errorf("%w (%s)", err, hint)
		}
		return wrapInit(ExitEnvironment, err)
	}

	anthropicOn := anthropicEnabled(sec, pendingOAuth)
	openaiOn := openaiEnabled(sec, pendingOAuth)

	if !f.noImportSkills && (anthropicOn || f.claudePath != "" || f.importSkills) {
		if err := importClaudeSkillsAtInit(env, layout, f); err != nil {
			fmt.Fprintf(env.Stderr, "skills import: %v\n", err)
		}
	}

	if !f.noImportCodexSkills && (openaiOn || f.codexPath != "" || f.importCodexSkills) {
		if err := importCodexSkillsAtInit(env, layout, f); err != nil {
			fmt.Fprintf(env.Stderr, "codex skills import: %v\n", err)
		}
	}

	if !f.noImportClaudeMCPs && (anthropicOn || f.claudeMCPPath != "" || f.importClaudeMCPs) {
		if err := importClaudeMCPsAtInit(ctx, env, layout, cfg, f); err != nil {
			fmt.Fprintf(env.Stderr, "claude mcp import: %v\n", err)
		}
	}

	if !f.noImportCodexMCPs && (openaiOn || f.codexMCPPath != "" || f.importCodexMCPs) {
		if err := importCodexMCPsAtInit(ctx, env, layout, cfg, f); err != nil {
			fmt.Fprintf(env.Stderr, "codex mcp import: %v\n", err)
		}
	}

	printInitSummary(env, layout, cfg, foreground, pendingOAuth)

	if foreground && !alreadyRunning {
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
		select {
		case <-sigs:
			fgCancel()
			<-fgErr
		case err := <-fgErr:
			fgCancel()
			if err != nil {
				return wrapInit(ExitEnvironment, fmt.Errorf("agentd exited: %w", err))
			}
		}
	} else if fgCancel != nil {
		fgCancel()
	}
	return nil
}

func probeHealth(addr string) bool {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get("http://" + addr + "/healthz")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

func ensureDirs(l paths.Layout) error {
	for _, dir := range []string{l.ConfigDir, l.DataDir, l.StateDir, l.SessionsDir, l.CustomSkills} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
		_ = os.Chmod(dir, 0o700)
	}
	return nil
}

func dockerCheck(ctx context.Context) error {
	if os.Getenv("AGENTCTL_SKIP_DOCKER_CHECK") == "1" {
		return nil
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker not on PATH; install Docker Desktop / Engine and retry")
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := exec.CommandContext(cctx, "docker", "info").Run(); err != nil {
		return fmt.Errorf("docker info failed: %w (is the daemon running?)", err)
	}
	return nil
}

func loadOrDefaultConfig(path string) config.Config {
	cfg, err := config.Load(path)
	if err != nil {
		return config.Default()
	}
	return cfg
}

func buildImage(ctx context.Context, env *Env, layout paths.Layout, cfg *config.Config) error {
	contextPath := config.ExpandHome(cfg.Image.BuildContextPath)
	if contextPath == "" {
		contextPath = layout.ImageDir
	}
	if _, err := os.Stat(filepath.Join(contextPath, "Dockerfile")); err != nil {
		return fmt.Errorf("image build context missing at %s; re-run install.sh", contextPath)
	}
	fmt.Fprintf(env.Stdout, "Building base image %s from %s ...\n", cfg.Image.LocalTag, contextPath)
	res, err := update.Build(ctx, update.BuildOptions{
		Tag:         cfg.Image.LocalTag,
		ContextPath: contextPath,
		Output:      env.Stdout,
	})
	if err != nil {
		return fmt.Errorf("docker build: %w", err)
	}
	if cfg.Image.PinnedID != "" && cfg.Image.PinnedID != res.ImageID {
		cfg.Image.PreviousID = cfg.Image.PinnedID
	}
	cfg.Image.PinnedID = res.ImageID
	fmt.Fprintf(env.Stdout, "image built: %s (took %s)\n", res.ImageID, res.Duration.Round(time.Millisecond))
	return nil
}

// loadOrInitSecrets resolves provider credentials and the GitHub PAT,
// prompting interactively when needed. Returns the resolved secrets plus
// a list of providers ("anthropic"|"openai") for which the user picked
// OAuth and still needs to run `agentctl auth login` to finish setup.
//
// Decision order per provider:
//
//  1. If a provider's flags are set, use them (flag-driven, no prompt).
//  2. Else if the provider is already in oauth mode (from a prior
//     `agentctl auth login`) and the user didn't pass --reset-token, keep
//     it and print a status line.
//  3. Else if --reset-token names this provider, prompt for it.
//  4. Else if this is a fresh install (nothing configured for either
//     provider and no flags), the user gets a "which providers?" prompt
//     and then a per-provider auth method prompt.
//  5. Otherwise (re-install with partial state): only Anthropic gets a
//     prompt when it has no creds — OpenAI stays opt-in via flags or
//     --reset-token openai, matching today's behavior.
func loadOrInitSecrets(layout paths.Layout, env *Env, f initFlags) (secrets.Secrets, []string, error) {
	existing, err := secrets.Load(layout.SecretsFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return secrets.Secrets{}, nil, err
	}
	out := existing
	out.V = 1

	skipAnthropic := os.Getenv("AGENTCTL_SKIP_ANTHROPIC_VALIDATE") == "1"
	skipGitHub := os.Getenv("AGENTCTL_SKIP_GITHUB_PAT_CHECK") == "1"
	skipOpenAI := os.Getenv("AGENTCTL_SKIP_OPENAI_VALIDATE") == "1"

	anthropicFromFlags, err := applyAnthropicFlags(&out, f, skipAnthropic)
	if err != nil {
		return secrets.Secrets{}, nil, err
	}
	openaiFromFlags, err := applyOpenAIFlags(&out, f, skipOpenAI)
	if err != nil {
		return secrets.Secrets{}, nil, err
	}

	anthropicHasOAuth := out.ResolvedAuthMode() == secrets.AuthModeOAuth
	openaiHasOAuth := out.ResolvedOpenAIAuthMode() == secrets.AuthModeOAuth
	anthropicHasCreds := hasAnthropicCreds(out)
	openaiHasCreds := hasOpenAICreds(out)

	if anthropicHasOAuth && !anthropicFromFlags && f.resetToken != "anthropic" {
		fmt.Fprintln(env.Stdout, "Anthropic auth: oauth (from `agentctl auth login`)")
	}
	if openaiHasOAuth && !openaiFromFlags && f.resetToken != "openai" {
		fmt.Fprintln(env.Stdout, "OpenAI auth: oauth (from `agentctl auth login`)")
	}

	var pendingOAuth []string

	switch {
	case f.resetToken == "anthropic" && !anthropicFromFlags:
		oauth, err := promptAnthropicAuth(&out, env, skipAnthropic)
		if err != nil {
			return secrets.Secrets{}, nil, err
		}
		if oauth {
			pendingOAuth = append(pendingOAuth, "anthropic")
		}
	case f.resetToken == "openai" && !openaiFromFlags:
		oauth, err := promptOpenAIAuth(&out, env, skipOpenAI)
		if err != nil {
			return secrets.Secrets{}, nil, err
		}
		if oauth {
			pendingOAuth = append(pendingOAuth, "openai")
		}
	case !anthropicFromFlags && !openaiFromFlags && !anthropicHasCreds && !openaiHasCreds && !anthropicHasOAuth && !openaiHasOAuth:
		// Fresh install: ask which providers, then auth method per provider.
		promptAnthropic, promptOpenAI, err := promptProviderSelection(env)
		if err != nil {
			return secrets.Secrets{}, nil, err
		}
		if promptAnthropic {
			oauth, err := promptAnthropicAuth(&out, env, skipAnthropic)
			if err != nil {
				return secrets.Secrets{}, nil, err
			}
			if oauth {
				pendingOAuth = append(pendingOAuth, "anthropic")
			}
		}
		if promptOpenAI {
			oauth, err := promptOpenAIAuth(&out, env, skipOpenAI)
			if err != nil {
				return secrets.Secrets{}, nil, err
			}
			if oauth {
				pendingOAuth = append(pendingOAuth, "openai")
			}
		}
	default:
		// Re-install with at least one provider already configured (or
		// a flag-driven setup). Prompt only for Anthropic when it still
		// has no credentials at all — mirrors today's behavior where
		// Anthropic was the mandatory provider. OpenAI remains opt-in.
		if !anthropicFromFlags && !anthropicHasOAuth && !anthropicHasCreds {
			oauth, err := promptAnthropicAuth(&out, env, skipAnthropic)
			if err != nil {
				return secrets.Secrets{}, nil, err
			}
			if oauth {
				pendingOAuth = append(pendingOAuth, "anthropic")
			}
		}
	}

	if f.githubPAT != "" {
		if !skipGitHub {
			if err := secrets.ValidateGitHubPAT(context.Background(), f.githubPAT); err != nil {
				return secrets.Secrets{}, nil, err
			}
		}
		out.GitHubPAT = f.githubPAT
		if out.GitHubPATKind == "" {
			out.GitHubPATKind = secrets.InferGitHubPATKind(f.githubPAT)
		}
	} else if out.GitHubPAT == "" || f.resetToken == "github" {
		v, err := promptSecret(env, "GITHUB_PAT: ")
		if err != nil {
			return secrets.Secrets{}, nil, err
		}
		if v == "" {
			return secrets.Secrets{}, nil, fmt.Errorf("GITHUB_PAT required (use --github-pat)")
		}
		if !skipGitHub {
			if err := secrets.ValidateGitHubPAT(context.Background(), v); err != nil {
				return secrets.Secrets{}, nil, err
			}
		}
		out.GitHubPAT = v
		out.GitHubPATKind = secrets.InferGitHubPATKind(v)
	}
	return out, pendingOAuth, nil
}

func hasAnthropicCreds(s secrets.Secrets) bool {
	return s.AnthropicAPIKey != "" || s.AnthropicAuthToken != ""
}

func hasOpenAICreds(s secrets.Secrets) bool {
	return s.OpenAIAPIKey != "" || s.OpenAIAuthToken != ""
}

// anthropicEnabled reports whether the user has Anthropic configured in
// any auth mode, including OAuth selections from this init run that have
// not yet completed `agentctl auth login`.
func anthropicEnabled(s secrets.Secrets, pendingOAuth []string) bool {
	if hasAnthropicCreds(s) || s.AnthropicAuthMode == secrets.AuthModeOAuth {
		return true
	}
	for _, p := range pendingOAuth {
		if p == "anthropic" {
			return true
		}
	}
	return false
}

// openaiEnabled mirrors anthropicEnabled for the OpenAI/Codex provider.
func openaiEnabled(s secrets.Secrets, pendingOAuth []string) bool {
	if hasOpenAICreds(s) || s.OpenAIAuthMode == secrets.AuthModeOAuth {
		return true
	}
	for _, p := range pendingOAuth {
		if p == "openai" {
			return true
		}
	}
	return false
}

func ensureSecretsFile(layout paths.Layout, sec secrets.Secrets) error {
	return secrets.Save(layout.SecretsFile, sec)
}

func ensureWebToken(layout paths.Layout) error {
	if _, err := os.Stat(layout.WebTokenFile); err == nil {
		_, _ = config.EnsurePerms(layout.WebTokenFile, 0o600)
		return nil
	}
	tok, err := secrets.GenerateWebToken()
	if err != nil {
		return err
	}
	return secrets.WriteWebToken(layout.WebTokenFile, tok)
}

func initDB(layout paths.Layout) error {
	st, err := store.Open(store.Options{Path: layout.DBFile})
	if err != nil {
		return fmt.Errorf("store open: %w", err)
	}
	defer func() { _ = st.Close() }()
	return st.Migrate()
}

func applyRegistrySeed(layout paths.Layout) error {
	data, _, err := mcp.ResolveSeed(layout.UserSeedFile, layout.SiteSeedFile)
	if err != nil {
		return err
	}
	rows, err := mcp.ParseSeed(data)
	if err != nil {
		return err
	}
	st, err := store.Open(store.Options{Path: layout.DBFile})
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	if _, err := mcp.ApplySeed(st, rows, time.Now().UTC()); err != nil {
		return err
	}
	return nil
}

type installMetadata struct {
	Version                  string   `json:"version"`
	InstallMethod            string   `json:"install_method"`
	InstalledAt              string   `json:"installed_at"`
	SourceURL                string   `json:"source_url,omitempty"`
	ClaudeImportOfferedAt    *string  `json:"claude_import_offered_at"`
	ClaudeImportedSkills     []string `json:"claude_imported_skills"`
	CodexImportOfferedAt     *string  `json:"codex_import_offered_at"`
	CodexImportedSkills      []string `json:"codex_imported_skills"`
	ClaudeMCPImportOfferedAt *string  `json:"claude_mcp_import_offered_at,omitempty"`
	ClaudeImportedMCPs       []string `json:"claude_imported_mcps,omitempty"`
	CodexMCPImportOfferedAt  *string  `json:"codex_mcp_import_offered_at,omitempty"`
	CodexImportedMCPs        []string `json:"codex_imported_mcps,omitempty"`
}

func writeInstallMetadata(layout paths.Layout) error {
	if _, err := os.Stat(layout.InstallMeta); err == nil {
		return nil
	}
	meta := installMetadata{
		Version:               version.Version,
		InstallMethod:         "agentctl init",
		InstalledAt:           time.Now().UTC().Format(time.RFC3339),
		ClaudeImportOfferedAt: nil,
		ClaudeImportedSkills:  []string{},
		CodexImportOfferedAt:  nil,
		CodexImportedSkills:   []string{},
	}
	data, _ := json.MarshalIndent(meta, "", "  ")
	return os.WriteFile(layout.InstallMeta, data, 0o644)
}

func recordClaudeImportedSkills(layout paths.Layout, names []string, offeredAt time.Time) error {
	var meta installMetadata
	data, err := os.ReadFile(layout.InstallMeta)
	if err == nil {
		_ = json.Unmarshal(data, &meta)
	}
	at := offeredAt.UTC().Format(time.RFC3339)
	meta.ClaudeImportOfferedAt = &at
	for _, n := range names {
		seen := false
		for _, existing := range meta.ClaudeImportedSkills {
			if existing == n {
				seen = true
				break
			}
		}
		if !seen {
			meta.ClaudeImportedSkills = append(meta.ClaudeImportedSkills, n)
		}
	}
	out, _ := json.MarshalIndent(meta, "", "  ")
	return os.WriteFile(layout.InstallMeta, out, 0o644)
}

func loadInstallMetadata(layout paths.Layout) installMetadata {
	var meta installMetadata
	data, err := os.ReadFile(layout.InstallMeta)
	if err != nil {
		return meta
	}
	_ = json.Unmarshal(data, &meta)
	return meta
}

func claudeImportAlreadyOffered(layout paths.Layout) bool {
	meta := loadInstallMetadata(layout)
	return meta.ClaudeImportOfferedAt != nil && *meta.ClaudeImportOfferedAt != ""
}

func codexImportAlreadyOffered(layout paths.Layout) bool {
	meta := loadInstallMetadata(layout)
	return meta.CodexImportOfferedAt != nil && *meta.CodexImportOfferedAt != ""
}

func recordCodexImportedSkills(layout paths.Layout, names []string, offeredAt time.Time) error {
	meta := loadInstallMetadata(layout)
	at := offeredAt.UTC().Format(time.RFC3339)
	meta.CodexImportOfferedAt = &at
	for _, n := range names {
		seen := false
		for _, existing := range meta.CodexImportedSkills {
			if existing == n {
				seen = true
				break
			}
		}
		if !seen {
			meta.CodexImportedSkills = append(meta.CodexImportedSkills, n)
		}
	}
	out, _ := json.MarshalIndent(meta, "", "  ")
	return os.WriteFile(layout.InstallMeta, out, 0o644)
}

func countSkillDirs(root string) (int, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, ent := range entries {
		if ent.IsDir() {
			n++
		}
	}
	return n, nil
}

func importClaudeSkillsAtInit(env *Env, layout paths.Layout, f initFlags) error {
	src := f.claudePath
	if src == "" {
		src = filepath.Join(layout.Home, ".claude", "skills")
	}
	if _, err := os.Stat(src); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(env.Stdout, "claude skills: %s not found, nothing to import.\n", src)
			return nil
		}
		return err
	}
	if !f.importSkills {
		if claudeImportAlreadyOffered(layout) {
			return nil
		}
		count, err := countSkillDirs(src)
		if err != nil {
			return err
		}
		if count == 0 {
			_ = recordClaudeImportedSkills(layout, nil, time.Now())
			return nil
		}
		prompt := fmt.Sprintf("Import %d Claude Code skill(s) from %s? [y/N]: ", count, src)
		ok, err := promptYesNo(env, prompt, false)
		if err != nil {
			return err
		}
		_ = recordClaudeImportedSkills(layout, nil, time.Now())
		if !ok {
			fmt.Fprintln(env.Stdout, "claude skills: import skipped.")
			return nil
		}
	}
	mgr := skills.NewManager(skills.Options{
		BuiltinDir: layout.BuiltinSkills,
		CustomDir:  layout.CustomSkills,
	})
	imported, skipped, err := mgr.ImportDirectory(src, skills.ImportOptions{Force: f.importSkills})
	if err != nil {
		return err
	}
	if len(imported) == 0 && len(skipped) == 0 {
		fmt.Fprintln(env.Stdout, "claude skills: nothing to import.")
	}
	for _, im := range imported {
		fmt.Fprintf(env.Stdout, "claude skills: imported %s\n", im)
	}
	for _, sk := range skipped {
		fmt.Fprintf(env.Stderr, "claude skills: skipped %s (%s)\n", sk.Name, sk.Reason)
	}
	if len(imported) > 0 {
		_ = recordClaudeImportedSkills(layout, imported, time.Now())
	}
	return nil
}

func importCodexSkillsAtInit(env *Env, layout paths.Layout, f initFlags) error {
	src := f.codexPath
	if src == "" {
		codexHome := os.Getenv("CODEX_HOME")
		if codexHome == "" {
			codexHome = filepath.Join(layout.Home, ".codex")
		}
		src = filepath.Join(codexHome, "skills")
	}
	if _, err := os.Stat(src); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(env.Stdout, "codex skills: %s not found, nothing to import.\n", src)
			return nil
		}
		return err
	}
	if !f.importCodexSkills {
		if codexImportAlreadyOffered(layout) {
			return nil
		}
		count, err := countSkillDirs(src)
		if err != nil {
			return err
		}
		if count == 0 {
			_ = recordCodexImportedSkills(layout, nil, time.Now())
			return nil
		}
		prompt := fmt.Sprintf("Import %d Codex CLI skill(s) from %s? [y/N]: ", count, src)
		ok, err := promptYesNo(env, prompt, false)
		if err != nil {
			return err
		}
		_ = recordCodexImportedSkills(layout, nil, time.Now())
		if !ok {
			fmt.Fprintln(env.Stdout, "codex skills: import skipped.")
			return nil
		}
	}
	mgr := skills.NewManager(skills.Options{
		BuiltinDir: layout.BuiltinSkills,
		CustomDir:  layout.CustomSkills,
	})
	imported, skipped, err := mgr.ImportDirectory(src, skills.ImportOptions{Force: f.importCodexSkills})
	if err != nil {
		return err
	}
	if len(imported) == 0 && len(skipped) == 0 {
		fmt.Fprintln(env.Stdout, "codex skills: nothing to import.")
	}
	claudeSet := map[string]bool{}
	for _, n := range loadInstallMetadata(layout).ClaudeImportedSkills {
		claudeSet[n] = true
	}
	for _, im := range imported {
		fmt.Fprintf(env.Stdout, "codex skills: imported %s\n", im)
	}
	for _, sk := range skipped {
		reason := sk.Reason
		if reason == "already in custom-skills" && claudeSet[sk.Name] {
			reason = "already imported from claude — pass --import-codex-skills to overwrite"
		}
		fmt.Fprintf(env.Stderr, "codex skills: skipped %s (%s)\n", sk.Name, reason)
	}
	if len(imported) > 0 {
		_ = recordCodexImportedSkills(layout, imported, time.Now())
	}
	return nil
}

func claudeMCPImportAlreadyOffered(layout paths.Layout) bool {
	meta := loadInstallMetadata(layout)
	return meta.ClaudeMCPImportOfferedAt != nil && *meta.ClaudeMCPImportOfferedAt != ""
}

func codexMCPImportAlreadyOffered(layout paths.Layout) bool {
	meta := loadInstallMetadata(layout)
	return meta.CodexMCPImportOfferedAt != nil && *meta.CodexMCPImportOfferedAt != ""
}

func recordClaudeImportedMCPs(layout paths.Layout, names []string, offeredAt time.Time) error {
	meta := loadInstallMetadata(layout)
	at := offeredAt.UTC().Format(time.RFC3339)
	meta.ClaudeMCPImportOfferedAt = &at
	meta.ClaudeImportedMCPs = mergeStringSet(meta.ClaudeImportedMCPs, names)
	out, _ := json.MarshalIndent(meta, "", "  ")
	return os.WriteFile(layout.InstallMeta, out, 0o644)
}

func recordCodexImportedMCPs(layout paths.Layout, names []string, offeredAt time.Time) error {
	meta := loadInstallMetadata(layout)
	at := offeredAt.UTC().Format(time.RFC3339)
	meta.CodexMCPImportOfferedAt = &at
	meta.CodexImportedMCPs = mergeStringSet(meta.CodexImportedMCPs, names)
	out, _ := json.MarshalIndent(meta, "", "  ")
	return os.WriteFile(layout.InstallMeta, out, 0o644)
}

func mergeStringSet(existing, add []string) []string {
	for _, n := range add {
		seen := false
		for _, e := range existing {
			if e == n {
				seen = true
				break
			}
		}
		if !seen {
			existing = append(existing, n)
		}
	}
	return existing
}

func importClaudeMCPsAtInit(ctx context.Context, env *Env, layout paths.Layout, cfg config.Config, f initFlags) error {
	src := f.claudeMCPPath
	if src == "" {
		src = mcpimport.DefaultClaudePath(layout.Home)
	}
	return importMCPsAtInit(ctx, env, layout, cfg, importMCPSpec{
		label:             "claude",
		src:               src,
		parse:             mcpimport.ParseClaude,
		alreadyOffered:    claudeMCPImportAlreadyOffered,
		record:            recordClaudeImportedMCPs,
		forceImport:       f.importClaudeMCPs,
		promptLabelPlural: "Claude Code MCP server(s)",
	})
}

func importCodexMCPsAtInit(ctx context.Context, env *Env, layout paths.Layout, cfg config.Config, f initFlags) error {
	src := f.codexMCPPath
	if src == "" {
		src = mcpimport.DefaultCodexPath(layout.Home)
	}
	return importMCPsAtInit(ctx, env, layout, cfg, importMCPSpec{
		label:             "codex",
		src:               src,
		parse:             mcpimport.ParseCodex,
		alreadyOffered:    codexMCPImportAlreadyOffered,
		record:            recordCodexImportedMCPs,
		forceImport:       f.importCodexMCPs,
		promptLabelPlural: "Codex CLI MCP server(s)",
	})
}

type importMCPSpec struct {
	label             string
	src               string
	parse             func(string) ([]mcpimport.ParsedEntry, error)
	alreadyOffered    func(paths.Layout) bool
	record            func(paths.Layout, []string, time.Time) error
	forceImport       bool
	promptLabelPlural string
}

func importMCPsAtInit(ctx context.Context, env *Env, layout paths.Layout, cfg config.Config, spec importMCPSpec) error {
	entries, err := spec.parse(spec.src)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(env.Stdout, "%s mcps: %s not found, nothing to import.\n", spec.label, spec.src)
			return nil
		}
		return err
	}
	importable := 0
	for _, e := range entries {
		if e.Skip == "" {
			importable++
		}
	}
	if !spec.forceImport {
		if spec.alreadyOffered(layout) {
			return nil
		}
		if importable == 0 {
			_ = spec.record(layout, nil, time.Now())
			if len(entries) > 0 {
				fmt.Fprintf(env.Stdout, "%s mcps: %d entries found in %s but none are http/sse (stdio not supported yet).\n",
					spec.label, len(entries), spec.src)
			}
			return nil
		}
		prompt := fmt.Sprintf("Import %d %s from %s? [y/N]: ", importable, spec.promptLabelPlural, spec.src)
		ok, err := promptYesNo(env, prompt, false)
		if err != nil {
			return err
		}
		_ = spec.record(layout, nil, time.Now())
		if !ok {
			fmt.Fprintf(env.Stdout, "%s mcps: import skipped.\n", spec.label)
			return nil
		}
	}
	imported, err := addImportedMCPs(ctx, env, cfg, entries, spec.label, spec.forceImport)
	if err != nil {
		return err
	}
	if len(imported) > 0 {
		_ = spec.record(layout, imported, time.Now())
	}
	return nil
}

// addImportedMCPs sends parsed entries to agentd via the existing AddMCP
// RPC, falling back to UpdateMCP when force is set and the name already
// exists. Skipped entries (stdio, conflicts without --force, etc.) are
// reported on stderr.
func addImportedMCPs(_ context.Context, env *Env, cfg config.Config, entries []mcpimport.ParsedEntry, label string, force bool) ([]string, error) {
	c, code := dialAgentd(env)
	if c == nil {
		return nil, fmt.Errorf("%s mcps: agentd unreachable (code %d)", label, code)
	}
	defer func() { _ = c.Close() }()
	_ = cfg
	imported := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Skip != "" {
			fmt.Fprintf(env.Stderr, "%s mcps: skipped %s (%s)\n", label, e.Name, e.Skip)
			continue
		}
		var resp proto.AddMCPResponse
		err := c.Call(proto.OpAddMCP, proto.AddMCPRequest{
			Name:           e.Name,
			URL:            e.URL,
			Transport:      e.Transport,
			Kind:           "none",
			DefaultEnabled: true,
			Description:    e.Description,
		}, &resp, 5*time.Second)
		if err == nil {
			fmt.Fprintf(env.Stdout, "%s mcps: imported %s\n", label, e.Name)
			imported = append(imported, e.Name)
			continue
		}
		var apiErr *cliclient.APIError
		if isAPIError(err, &apiErr) && apiErr.Code == proto.ErrConflict {
			if !force {
				fmt.Fprintf(env.Stderr, "%s mcps: skipped %s (already in registry)\n", label, e.Name)
				continue
			}
			url, transport, kind, desc := e.URL, e.Transport, "none", e.Description
			var upResp proto.UpdateMCPResponse
			upErr := c.Call(proto.OpUpdateMCP, proto.UpdateMCPRequest{
				Name:        e.Name,
				URL:         &url,
				Transport:   &transport,
				Kind:        &kind,
				Description: &desc,
			}, &upResp, 5*time.Second)
			if upErr != nil {
				fmt.Fprintf(env.Stderr, "%s mcps: update %s failed: %v\n", label, e.Name, upErr)
				continue
			}
			fmt.Fprintf(env.Stdout, "%s mcps: updated %s\n", label, e.Name)
			imported = append(imported, e.Name)
			continue
		}
		fmt.Fprintf(env.Stderr, "%s mcps: add %s failed: %v\n", label, e.Name, err)
	}
	return imported, nil
}

func healthHint(foreground bool, fgErr chan error) string {
	if foreground && fgErr != nil {
		select {
		case err := <-fgErr:
			if err != nil {
				return "agentd: " + err.Error()
			}
		default:
		}
		return "see stderr above for agentd logs"
	}
	switch runtime.GOOS {
	case "linux":
		return "check `systemctl --user status agentd` and `journalctl --user -u agentd`"
	case "darwin":
		return "check ~/Library/Logs/agentctl/agentd.log"
	}
	return ""
}

func waitForHealth(ctx context.Context, layout paths.Layout, webAddr string, total time.Duration) error {
	deadline := time.Now().Add(total)
	tickCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	for {
		if c, err := cliclient.Dial(layout.SocketFile, 500*time.Millisecond); err == nil {
			_, err := c.Health()
			_ = c.Close()
			if err == nil {
				return nil
			}
		}
		client := http.Client{Timeout: 500 * time.Millisecond}
		resp, err := client.Get("http://" + webAddr + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 || resp.StatusCode == 503 {
				return nil
			}
		}
		select {
		case <-tickCtx.Done():
			return fmt.Errorf("agentd did not report healthy within %s", total)
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func printInitSummary(env *Env, layout paths.Layout, cfg config.Config, foreground bool, pendingOAuth []string) {
	fmt.Fprintln(env.Stdout, "")
	fmt.Fprintln(env.Stdout, "agentctl is ready.")
	fmt.Fprintln(env.Stdout, "")
	if foreground {
		fmt.Fprintln(env.Stdout, "  Service:        foreground (this shell only)")
	} else {
		fmt.Fprintln(env.Stdout, "  Service:        active (system service) — auto-starts on login")
	}
	fmt.Fprintf(env.Stdout, "  Web UI:         http://%s/ (run `agentctl ui` to open)\n", cfg.Agentd.WebAddr)
	if cfg.Image.PinnedID != "" {
		fmt.Fprintf(env.Stdout, "  Image pinned:   %s id=%s\n", cfg.Image.LocalTag, cfg.Image.PinnedID)
	}
	fmt.Fprintf(env.Stdout, "  Config:         %s\n", layout.ConfigFile)
	if len(pendingOAuth) > 0 {
		fmt.Fprintln(env.Stdout, "")
		fmt.Fprintln(env.Stdout, "Finish OAuth setup:")
		for _, p := range pendingOAuth {
			fmt.Fprintf(env.Stdout, "  agentctl auth login --provider %s\n", p)
		}
	}
	fmt.Fprintln(env.Stdout, "")
	fmt.Fprintln(env.Stdout, "Next: agentctl start --repo <git-url>   (M2)")
}

func promptSecret(env *Env, prompt string) (string, error) {
	if env.Stdin == os.Stdin {
		fmt.Fprint(env.Stderr, prompt)
	}
	buf := make([]byte, 0, 256)
	tmp := make([]byte, 256)
	for {
		n, err := env.Stdin.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			if idx := indexByte(tmp[:n], '\n'); idx >= 0 {
				return strings.TrimRight(string(buf[:len(buf)-(n-idx)+1]), "\r\n "), nil
			}
		}
		if err == io.EOF {
			return strings.TrimRight(string(buf), "\r\n "), nil
		}
		if err != nil {
			return "", err
		}
	}
}

func indexByte(b []byte, c byte) int {
	for i, v := range b {
		if v == c {
			return i
		}
	}
	return -1
}

// applyAnthropicFlags applies flag-driven Anthropic credentials (if any
// Anthropic flag is set). Returns true when a flag was applied so the
// caller can skip the interactive prompt for this provider.
func applyAnthropicFlags(out *secrets.Secrets, f initFlags, skipValidate bool) (bool, error) {
	if f.anthropicBaseURL != "" || f.anthropicAuthToken != "" {
		if f.anthropicBaseURL == "" || f.anthropicAuthToken == "" {
			return false, fmt.Errorf("--anthropic-base-url and --anthropic-auth-token must be set together")
		}
		if f.anthropicKey != "" {
			return false, fmt.Errorf("--anthropic-key cannot be combined with --anthropic-base-url/--anthropic-auth-token")
		}
		baseURL := normalizeBaseURL(f.anthropicBaseURL)
		if !skipValidate {
			if err := validateAnthropicCustom(baseURL, f.anthropicAuthToken); err != nil {
				return false, err
			}
		}
		out.AnthropicBaseURL = baseURL
		out.AnthropicAuthToken = f.anthropicAuthToken
		out.AnthropicAPIKey = ""
		out.AnthropicAuthMode = secrets.AuthModeAPIKey
		return true, nil
	}
	if f.anthropicKey != "" {
		if !skipValidate {
			if err := validateAnthropic(f.anthropicKey); err != nil {
				return false, err
			}
		}
		out.AnthropicAPIKey = f.anthropicKey
		out.AnthropicBaseURL = ""
		out.AnthropicAuthToken = ""
		out.AnthropicAuthMode = secrets.AuthModeAPIKey
		return true, nil
	}
	return false, nil
}

// applyOpenAIFlags is the OpenAI mirror of applyAnthropicFlags. The two
// providers share the same shape: either an API key against the vendor
// endpoint, or a custom base URL + bearer token for a gateway.
func applyOpenAIFlags(out *secrets.Secrets, f initFlags, skipValidate bool) (bool, error) {
	if f.openaiBaseURL != "" || f.openaiAuthToken != "" {
		if f.openaiBaseURL == "" || f.openaiAuthToken == "" {
			return false, fmt.Errorf("--openai-base-url and --openai-auth-token must be set together")
		}
		if f.openaiKey != "" {
			return false, fmt.Errorf("--openai-key cannot be combined with --openai-base-url/--openai-auth-token")
		}
		baseURL := normalizeBaseURL(f.openaiBaseURL)
		if !skipValidate {
			if err := validateOpenAICustom(baseURL, f.openaiAuthToken); err != nil {
				return false, err
			}
		}
		out.OpenAIBaseURL = baseURL
		out.OpenAIAuthToken = f.openaiAuthToken
		out.OpenAIAPIKey = ""
		out.OpenAIAuthMode = secrets.AuthModeAPIKey
		return true, nil
	}
	if f.openaiKey != "" {
		if !skipValidate {
			if err := validateOpenAI(f.openaiKey); err != nil {
				return false, err
			}
		}
		out.OpenAIAPIKey = f.openaiKey
		out.OpenAIBaseURL = ""
		out.OpenAIAuthToken = ""
		out.OpenAIAuthMode = secrets.AuthModeAPIKey
		return true, nil
	}
	return false, nil
}

// promptProviderSelection asks the fresh-install user which providers to
// enable. Returns (anthropic, openai); at least one is true. Defaults to
// Anthropic-only on Enter so the historical single-provider onboarding
// stays a single keystroke away.
func promptProviderSelection(env *Env) (bool, bool, error) {
	fmt.Fprintln(env.Stdout, "")
	fmt.Fprintln(env.Stdout, "Which provider(s) do you want to enable?")
	fmt.Fprintln(env.Stdout, "  [1] Anthropic (Claude)")
	fmt.Fprintln(env.Stdout, "  [2] OpenAI (Codex)")
	fmt.Fprintln(env.Stdout, "  [3] Both")
	n, err := promptChoice(env, "Choice [1]: ", 1, 3)
	if err != nil {
		return false, false, err
	}
	switch n {
	case 1:
		return true, false, nil
	case 2:
		return false, true, nil
	case 3:
		return true, true, nil
	}
	return false, false, fmt.Errorf("unreachable")
}

// promptAnthropicAuth runs the interactive auth-method picker for
// Anthropic and stores the resulting credentials. The returned bool is
// true when the user chose OAuth; in that case credentials are left
// untouched and `agentctl auth login --provider anthropic` is expected
// to complete setup (it flips AnthropicAuthMode to oauth when the
// credentials file is written).
func promptAnthropicAuth(out *secrets.Secrets, env *Env, skipValidate bool) (bool, error) {
	method, err := promptProviderAuthMethod(env, "Anthropic")
	if err != nil {
		return false, err
	}
	switch method {
	case authMethodAPIKey:
		v, err := promptSecret(env, "ANTHROPIC_API_KEY: ")
		if err != nil {
			return false, err
		}
		if v == "" {
			return false, fmt.Errorf("ANTHROPIC_API_KEY required (re-run and pick OAuth to defer)")
		}
		if !skipValidate {
			if err := validateAnthropic(v); err != nil {
				return false, err
			}
		}
		out.AnthropicAPIKey = v
		out.AnthropicBaseURL = ""
		out.AnthropicAuthToken = ""
		out.AnthropicAuthMode = secrets.AuthModeAPIKey
		return false, nil
	case authMethodGateway:
		baseURL, err := promptSecret(env, "ANTHROPIC_BASE_URL (e.g. https://gateway.example.com): ")
		if err != nil {
			return false, err
		}
		if baseURL == "" {
			return false, fmt.Errorf("ANTHROPIC_BASE_URL required")
		}
		token, err := promptSecret(env, "ANTHROPIC_AUTH_TOKEN: ")
		if err != nil {
			return false, err
		}
		if token == "" {
			return false, fmt.Errorf("ANTHROPIC_AUTH_TOKEN required")
		}
		baseURL = normalizeBaseURL(baseURL)
		if !skipValidate {
			if err := validateAnthropicCustom(baseURL, token); err != nil {
				return false, err
			}
		}
		out.AnthropicBaseURL = baseURL
		out.AnthropicAuthToken = token
		out.AnthropicAPIKey = ""
		out.AnthropicAuthMode = secrets.AuthModeAPIKey
		return false, nil
	case authMethodOAuth:
		fmt.Fprintln(env.Stdout, "")
		fmt.Fprintln(env.Stdout, "Anthropic OAuth selected. After init finishes, run:")
		fmt.Fprintln(env.Stdout, "  agentctl auth login --provider anthropic")
		return true, nil
	}
	return false, fmt.Errorf("unreachable")
}

// promptOpenAIAuth is the OpenAI mirror of promptAnthropicAuth.
func promptOpenAIAuth(out *secrets.Secrets, env *Env, skipValidate bool) (bool, error) {
	method, err := promptProviderAuthMethod(env, "OpenAI")
	if err != nil {
		return false, err
	}
	switch method {
	case authMethodAPIKey:
		v, err := promptSecret(env, "OPENAI_API_KEY: ")
		if err != nil {
			return false, err
		}
		if v == "" {
			return false, fmt.Errorf("OPENAI_API_KEY required (re-run and pick OAuth to defer)")
		}
		if !skipValidate {
			if err := validateOpenAI(v); err != nil {
				return false, err
			}
		}
		out.OpenAIAPIKey = v
		out.OpenAIBaseURL = ""
		out.OpenAIAuthToken = ""
		out.OpenAIAuthMode = secrets.AuthModeAPIKey
		return false, nil
	case authMethodGateway:
		baseURL, err := promptSecret(env, "OPENAI_BASE_URL (e.g. https://gateway.example.com): ")
		if err != nil {
			return false, err
		}
		if baseURL == "" {
			return false, fmt.Errorf("OPENAI_BASE_URL required")
		}
		token, err := promptSecret(env, "OPENAI_AUTH_TOKEN: ")
		if err != nil {
			return false, err
		}
		if token == "" {
			return false, fmt.Errorf("OPENAI_AUTH_TOKEN required")
		}
		baseURL = normalizeBaseURL(baseURL)
		if !skipValidate {
			if err := validateOpenAICustom(baseURL, token); err != nil {
				return false, err
			}
		}
		out.OpenAIBaseURL = baseURL
		out.OpenAIAuthToken = token
		out.OpenAIAPIKey = ""
		out.OpenAIAuthMode = secrets.AuthModeAPIKey
		return false, nil
	case authMethodOAuth:
		fmt.Fprintln(env.Stdout, "")
		fmt.Fprintln(env.Stdout, "OpenAI OAuth selected. After init finishes, run:")
		fmt.Fprintln(env.Stdout, "  agentctl auth login --provider openai")
		return true, nil
	}
	return false, fmt.Errorf("unreachable")
}

const (
	authMethodAPIKey  = "apikey"
	authMethodGateway = "gateway"
	authMethodOAuth   = "oauth"
)

// promptProviderAuthMethod offers the three auth modes for `provider`
// ("Anthropic"|"OpenAI"). Defaults to API key on Enter — the single
// most common case and historically the only one we prompted for.
func promptProviderAuthMethod(env *Env, provider string) (string, error) {
	var keyName, oauthHint string
	switch provider {
	case "Anthropic":
		keyName = "ANTHROPIC_API_KEY"
		oauthHint = "sign in with your Claude subscription"
	case "OpenAI":
		keyName = "OPENAI_API_KEY"
		oauthHint = "sign in with your ChatGPT account"
	}
	fmt.Fprintln(env.Stdout, "")
	fmt.Fprintf(env.Stdout, "How would you like to authenticate with %s?\n", provider)
	fmt.Fprintf(env.Stdout, "  [1] API key        — paste %s now\n", keyName)
	fmt.Fprintln(env.Stdout, "  [2] Custom gateway — base URL + bearer token for an LLM gateway / proxy")
	fmt.Fprintf(env.Stdout, "  [3] OAuth          — %s via `agentctl auth login` (after init)\n", oauthHint)
	n, err := promptChoice(env, "Choice [1]: ", 1, 3)
	if err != nil {
		return "", err
	}
	switch n {
	case 1:
		return authMethodAPIKey, nil
	case 2:
		return authMethodGateway, nil
	case 3:
		return authMethodOAuth, nil
	}
	return "", fmt.Errorf("unreachable")
}

// promptChoice reads a single integer in [1, max] from env.Stdin. Empty
// input returns def. Invalid input is re-prompted until the user either
// gives a valid answer, hits EOF (returns def), or the underlying
// reader errors (propagated).
func promptChoice(env *Env, prompt string, def, max int) (int, error) {
	for {
		v, err := promptSecret(env, prompt)
		if err != nil {
			return 0, err
		}
		v = strings.TrimSpace(v)
		if v == "" {
			return def, nil
		}
		n, convErr := strconv.Atoi(v)
		if convErr == nil && n >= 1 && n <= max {
			return n, nil
		}
		fmt.Fprintf(env.Stderr, "  please answer 1-%d (got %q)\n", max, v)
	}
}

func normalizeBaseURL(u string) string {
	return strings.TrimRight(strings.TrimSpace(u), "/")
}

func promptYesNo(env *Env, prompt string, def bool) (bool, error) {
	v, err := promptSecret(env, prompt)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "":
		return def, nil
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return false, fmt.Errorf("answer y or n (got %q)", v)
	}
}

func validateAnthropicCustom(baseURL, token string) error {
	req, err := http.NewRequest("GET", baseURL+"/v1/models", nil)
	if err != nil {
		return fmt.Errorf("anthropic base url: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-version", "2023-06-01")
	client := http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("anthropic endpoint %s: %w", baseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return fmt.Errorf("anthropic auth token rejected by %s (status %d)", baseURL, resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("anthropic endpoint %s status %d", baseURL, resp.StatusCode)
	}
	return nil
}

// validateOpenAICustom is the gateway variant: hit GET
// <base-url>/v1/models with the bearer. Mirror of validateAnthropicCustom
// — same shape, OpenAI-style Authorization header. Per
// CODEX_PROVIDER_PLAN §5.1, gateways may not expose /v1/models; we treat
// any 4xx as "configured but unverified" (warn, continue) and only fail
// hard on network/TLS errors.
func validateOpenAICustom(baseURL, token string) error {
	req, err := http.NewRequest("GET", baseURL+"/v1/models", nil)
	if err != nil {
		return fmt.Errorf("openai base url: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	// Internal gateway hostnames (e.g. internal-gateway.corp.local) can
	// leak into CI logs / shared error output, so error strings include
	// host-only redactions; the full URL is the user's input — they can
	// see what they typed — and the warn line still prints it locally
	// for diagnostic value.
	host := redactBaseURL(baseURL)
	if err != nil {
		// Network / TLS errors are hard fails — the user supplied an
		// unreachable host or a cert mismatch and we won't get further.
		return fmt.Errorf("openai endpoint %s: %w", host, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		// Auth-rejection is a hard fail: a real gateway that does
		// expose /v1/models is telling us the token is wrong.
		return fmt.Errorf("openai auth token rejected by %s (status %d)", host, resp.StatusCode)
	}
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		// Other 4xx (404 etc.): treat as "configured but unverified".
		// Many gateways serve /v1/chat/completions but no /v1/models —
		// the connection works, so leave the secrets in place and
		// emit a hint instead of failing init.
		fmt.Fprintf(os.Stderr, "warn: %s returned status %d for /v1/models; gateway endpoint set, but unverified\n", baseURL, resp.StatusCode)
		return nil
	}
	if resp.StatusCode >= 500 {
		return fmt.Errorf("openai endpoint %s status %d", host, resp.StatusCode)
	}
	return nil
}

// redactBaseURL returns scheme://host for use in error strings, dropping
// the path / query / fragment. Internal gateway URLs (e.g.
// https://internal-gw.corp.local/v2/openai/abc?token=…) often carry
// path-encoded identifiers that don't belong in CI logs or shared error
// output. Hostname-only is enough for the user — who typed the URL — to
// recognize which endpoint failed.
func redactBaseURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "<gateway>"
	}
	if u.Scheme == "" {
		return u.Host
	}
	return u.Scheme + "://" + u.Host
}

// validateOpenAI hits GET https://api.openai.com/v1/models with the bearer
// key. Mirror of validateAnthropic — same shape, different endpoint and
// auth header.
func validateOpenAI(key string) error {
	req, err := http.NewRequest("GET", "https://api.openai.com/v1/models", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	client := http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("openai api: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return fmt.Errorf("openai key rejected (status %d)", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("openai api status %d", resp.StatusCode)
	}
	return nil
}

func validateAnthropic(key string) error {
	req, err := http.NewRequest("GET", "https://api.anthropic.com/v1/models", nil)
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")
	client := http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("anthropic api: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return fmt.Errorf("anthropic key rejected (status %d)", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("anthropic api status %d", resp.StatusCode)
	}
	return nil
}
