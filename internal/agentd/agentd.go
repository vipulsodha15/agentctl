package agentd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/agentctl/agentctl/internal/api"
	"github.com/agentctl/agentctl/internal/cc"
	"github.com/agentctl/agentctl/internal/cm"
	"github.com/agentctl/agentctl/internal/config"
	"github.com/agentctl/agentctl/internal/fan"
	"github.com/agentctl/agentctl/internal/log"
	"github.com/agentctl/agentctl/internal/mcp"
	"github.com/agentctl/agentctl/internal/paths"
	"github.com/agentctl/agentctl/internal/secrets"
	"github.com/agentctl/agentctl/internal/skills"
	"github.com/agentctl/agentctl/internal/sm"
	"github.com/agentctl/agentctl/internal/socksrv"
	"github.com/agentctl/agentctl/internal/store"
	"github.com/agentctl/agentctl/internal/version"
	"github.com/agentctl/agentctl/internal/websrv"
)

type Options struct {
	Layout paths.Layout
}

func Run(ctx context.Context, opts Options) error {
	if os.Geteuid() == 0 && os.Getenv("AGENTCTL_ALLOW_ROOT") != "1" {
		return fmt.Errorf("agentd refuses to run as root; start as your normal user (set AGENTCTL_ALLOW_ROOT=1 to override in test rigs)")
	}
	logger := log.New(log.Options{Component: log.ComponentBoot})
	logger.Info("agentd.boot",
		slog.String("version", version.Version),
		slog.String("build", version.Build),
		slog.String("home", opts.Layout.Home),
	)

	cfg, err := config.Load(opts.Layout.ConfigFile)
	if err != nil {
		return fmt.Errorf("config: %w (run `agentctl init`)", err)
	}
	logger.Info("config.loaded", slog.String("web_addr", cfg.Agentd.WebAddr), slog.String("log_level", cfg.Agentd.LogLevel))

	if sec, err := secrets.Load(opts.Layout.SecretsFile); err == nil {
		log.RegisterSecret(sec.AnthropicAPIKey)
		log.RegisterSecret(sec.GitHubPAT)
	}
	if tok, err := secrets.ReadWebToken(opts.Layout.WebTokenFile); err == nil {
		log.RegisterSecret(tok)
	}

	st, err := store.Open(store.Options{Path: opts.Layout.DBFile})
	if err != nil {
		return fmt.Errorf("store open: %w", err)
	}
	defer func() { _ = st.Close() }()
	if err := st.Migrate(); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	v, _ := st.SchemaVersion()
	logger.Info("db.opened", slog.Int("schema_version", v))

	dockerProbe := &api.CLIDockerProbe{}
	apiSrv := api.New(api.Options{Docker: dockerProbe})
	apiSrv.SetReconciling(false)

	hub := fan.NewHub()
	smLog := log.New(log.Options{Component: log.ComponentSessions})

	var (
		cmAdapt *cmAdapter
		ccAdapt *ccAdapter
	)
	dockerCli, dockerErr := cm.NewDockerSDKClient()
	if dockerErr != nil {
		logger.Warn("docker.unavailable", slog.String("error", dockerErr.Error()))
	} else {
		cmAdapt = newCmAdapter(cm.NewManager(dockerCli))
		ccLog := log.New(log.Options{Component: log.ComponentContainer})
		ccSrv := cc.New(cc.Options{Logger: ccLog})
		ccAdapt = newCcAdapter(ccSrv)
		ccSrv.AdoptInjector(ccAdapt, ccAdapt)
	}

	mcpReg := mcp.NewRegistry(mcp.Options{Store: st})
	skillMgr := skills.NewManager(skills.Options{
		BuiltinDir: opts.Layout.BuiltinSkills,
		CustomDir:  opts.Layout.CustomSkills,
	})

	managerOpts := sm.Options{
		Store:        st,
		SessionsDir:  opts.Layout.SessionsDir,
		Hub:          hub,
		Logger:       smLog,
		DefaultModel: cfg.Model.Default,
		ImageID:      cfg.Image.PinnedID,
		SecretsPath:  opts.Layout.SecretsFile,
		MCPs:         mcpReg,
	}
	if cmAdapt != nil {
		managerOpts.Containers = cmAdapt
	}
	if ccAdapt != nil {
		managerOpts.Control = ccAdapt
	}
	manager := sm.New(managerOpts)
	defer func() { _ = manager.Shutdown(ctx) }()

	logStream := &log.SessionLogStreamer{SessionsDir: opts.Layout.SessionsDir}

	sockLog := log.New(log.Options{Component: log.ComponentSock})
	socketSrv := socksrv.New(socksrv.Options{
		SocketPath: opts.Layout.SocketFile,
		API:        apiSrv,
		Manager:    manager,
		MCPs:       mcpReg,
		Skills:     skillMgr,
		LogStream:  logStream,
		Logger:     sockLog,
	})
	if err := socketSrv.Start(); err != nil {
		return fmt.Errorf("cli socket: %w", err)
	}
	defer func() { _ = socketSrv.Close() }()
	logger.Info("sock.listening", slog.String("path", opts.Layout.SocketFile))

	tok, err := secrets.ReadWebToken(opts.Layout.WebTokenFile)
	if err != nil {
		return fmt.Errorf("web_token missing: %w (run `agentctl init`)", err)
	}
	webLog := log.New(log.Options{Component: log.ComponentWeb})
	webSrv := websrv.New(websrv.Options{
		Addr:    cfg.Agentd.WebAddr,
		Token:   tok,
		API:     apiSrv,
		Manager: manager,
		MCPs:    newMcpAdapter(mcpReg),
		Skills:  newSkillsAdapter(skillMgr),
		Logs:    logStream,
		Logger:  webLog,
	})
	if err := webSrv.Start(); err != nil {
		return fmt.Errorf("web server: %w", err)
	}
	defer func() { _ = webSrv.Close() }()
	logger.Info("web.listening", slog.String("addr", webSrv.Addr()))

	logger.Info("agentd.ready", slog.String("socket", opts.Layout.SocketFile), slog.String("web", webSrv.Addr()))

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-ctx.Done():
	case s := <-sigCh:
		logger.Info("agentd.signal", slog.String("signal", s.String()))
	}
	logger.Info("agentd.shutdown")
	return nil
}
