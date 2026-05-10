package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/agentctl/agentctl/internal/agentd"
	"github.com/agentctl/agentctl/internal/cliclient"
	"github.com/agentctl/agentctl/internal/config"
	"github.com/agentctl/agentctl/internal/mcp"
	"github.com/agentctl/agentctl/internal/paths"
	"github.com/agentctl/agentctl/internal/secrets"
	"github.com/agentctl/agentctl/internal/service"
	"github.com/agentctl/agentctl/internal/skills"
	"github.com/agentctl/agentctl/internal/store"
	"github.com/agentctl/agentctl/internal/update"
	"github.com/agentctl/agentctl/internal/version"
)

type initFlags struct {
	anthropicKey   string
	githubPAT      string
	noImportSkills bool
	importSkills   bool
	claudePath     string
	foreground     bool
	resetToken     string
	resetWebToken  bool
	repair         bool
	skipBuild      bool
}

func runInit(ctx context.Context, env *Env, args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	var f initFlags
	fs.StringVar(&f.anthropicKey, "anthropic-key", "", "use this Anthropic API key (skip prompt)")
	fs.StringVar(&f.githubPAT, "github-pat", "", "use this GitHub PAT (skip prompt)")
	fs.BoolVar(&f.noImportSkills, "no-import-claude-skills", false, "skip the Claude Code skills import step")
	fs.BoolVar(&f.importSkills, "import-claude-skills", false, "force the Claude Code skills import step")
	fs.StringVar(&f.claudePath, "claude-path", "", "override the Claude Code skills source path")
	fs.BoolVar(&f.foreground, "foreground", false, "skip system-service install and run agentd in foreground")
	fs.StringVar(&f.resetToken, "reset-token", "", "force re-prompt for a token kind: anthropic|github")
	fs.BoolVar(&f.resetWebToken, "reset-web-token", false, "regenerate the web bearer token")
	fs.BoolVar(&f.repair, "repair", false, "re-run install steps without prompting for tokens")
	fs.BoolVar(&f.skipBuild, "skip-image-build", false, "(test-only) skip the docker build step")
	skipDocker := fs.Bool("skip-docker-check", false, "(test-only) skip the docker info reachability check")
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: agentctl init [flags]")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "First-time setup: verifies Docker, builds the session base image,")
		fmt.Fprintln(env.Stderr, "prompts for ANTHROPIC_API_KEY and GITHUB_PAT, ensures perms on")
		fmt.Fprintln(env.Stderr, "~/.config/agentctl, seeds the MCP registry, installs the user service,")
		fmt.Fprintln(env.Stderr, "and waits for /healthz to come up. Idempotent: re-run to repair drift.")
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

	sec, err := loadOrInitSecrets(layout, env, f)
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

	if !f.noImportSkills {
		if err := importClaudeSkillsAtInit(env, layout, f); err != nil {
			fmt.Fprintf(env.Stderr, "skills import: %v\n", err)
		}
	}

	printInitSummary(env, layout, cfg, foreground)

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

func loadOrInitSecrets(layout paths.Layout, env *Env, f initFlags) (secrets.Secrets, error) {
	existing, err := secrets.Load(layout.SecretsFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return secrets.Secrets{}, err
	}
	out := existing
	out.V = 1

	skipAnthropic := os.Getenv("AGENTCTL_SKIP_ANTHROPIC_VALIDATE") == "1"
	skipGitHub := os.Getenv("AGENTCTL_SKIP_GITHUB_PAT_CHECK") == "1"

	if f.anthropicKey != "" {
		if !skipAnthropic {
			if err := validateAnthropic(f.anthropicKey); err != nil {
				return secrets.Secrets{}, err
			}
		}
		out.AnthropicAPIKey = f.anthropicKey
	} else if out.AnthropicAPIKey == "" || f.resetToken == "anthropic" {
		v, err := promptSecret(env, "ANTHROPIC_API_KEY: ")
		if err != nil {
			return secrets.Secrets{}, err
		}
		if v == "" {
			return secrets.Secrets{}, fmt.Errorf("ANTHROPIC_API_KEY required (use --anthropic-key)")
		}
		if !skipAnthropic {
			if err := validateAnthropic(v); err != nil {
				return secrets.Secrets{}, err
			}
		}
		out.AnthropicAPIKey = v
	}

	if f.githubPAT != "" {
		if !skipGitHub {
			if err := validateGitHubPAT(f.githubPAT); err != nil {
				return secrets.Secrets{}, err
			}
		}
		out.GitHubPAT = f.githubPAT
		if out.GitHubPATKind == "" {
			out.GitHubPATKind = inferPATKind(f.githubPAT)
		}
	} else if out.GitHubPAT == "" || f.resetToken == "github" {
		v, err := promptSecret(env, "GITHUB_PAT: ")
		if err != nil {
			return secrets.Secrets{}, err
		}
		if v == "" {
			return secrets.Secrets{}, fmt.Errorf("GITHUB_PAT required (use --github-pat)")
		}
		if !skipGitHub {
			if err := validateGitHubPAT(v); err != nil {
				return secrets.Secrets{}, err
			}
		}
		out.GitHubPAT = v
		out.GitHubPATKind = inferPATKind(v)
	}
	return out, nil
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
	Version               string   `json:"version"`
	InstallMethod         string   `json:"install_method"`
	InstalledAt           string   `json:"installed_at"`
	SourceURL             string   `json:"source_url,omitempty"`
	ClaudeImportOfferedAt *string  `json:"claude_import_offered_at"`
	ClaudeImportedSkills  []string `json:"claude_imported_skills"`
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

func importClaudeSkillsAtInit(env *Env, layout paths.Layout, f initFlags) error {
	src := f.claudePath
	if src == "" {
		src = filepath.Join(layout.Home, ".claude", "skills")
	}
	if _, err := os.Stat(src); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
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

func printInitSummary(env *Env, layout paths.Layout, cfg config.Config, foreground bool) {
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

func validateGitHubPAT(pat string) error {
	req, err := http.NewRequest("GET", "https://api.github.com/user", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+pat)
	req.Header.Set("Accept", "application/vnd.github+json")
	client := http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("github api: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == 401 {
		return fmt.Errorf("github PAT rejected (401)")
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("github api status %d", resp.StatusCode)
	}
	return nil
}

func inferPATKind(pat string) string {
	switch {
	case strings.HasPrefix(pat, "github_pat_"):
		return "fine-grained"
	case strings.HasPrefix(pat, "ghp_"), strings.HasPrefix(pat, "gho_"), strings.HasPrefix(pat, "ghs_"):
		return "classic"
	default:
		return "unknown"
	}
}
