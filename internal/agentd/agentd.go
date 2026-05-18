package agentd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/docker/docker/client"

	"github.com/agentctl/agentctl/internal/api"
	"github.com/agentctl/agentctl/internal/cc"
	"github.com/agentctl/agentctl/internal/cm"
	"github.com/agentctl/agentctl/internal/config"
	"github.com/agentctl/agentctl/internal/fan"
	"github.com/agentctl/agentctl/internal/log"
	"github.com/agentctl/agentctl/internal/mcp"
	"github.com/agentctl/agentctl/internal/paths"
	"github.com/agentctl/agentctl/internal/recovery"
	"github.com/agentctl/agentctl/internal/secrets"
	"github.com/agentctl/agentctl/internal/skills"
	"github.com/agentctl/agentctl/internal/sm"
	"github.com/agentctl/agentctl/internal/socksrv"
	"github.com/agentctl/agentctl/internal/store"
	"github.com/agentctl/agentctl/internal/sweep"
	"github.com/agentctl/agentctl/internal/tm"
	"github.com/agentctl/agentctl/internal/ttl"
	"github.com/agentctl/agentctl/internal/usage"
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
		log.RegisterSecret(sec.AnthropicAuthToken)
		log.RegisterSecret(sec.OpenAIAPIKey)
		log.RegisterSecret(sec.OpenAIAuthToken)
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
		cmAdapt   *cmAdapter
		ccAdapt   *ccAdapter
		recoverCM recovery.ContainerManager
		cmMgr     cm.Manager
	)
	dockerSDK, dockerSDKErr := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	dockerCli, dockerErr := cm.NewDockerSDKClient()
	if dockerErr != nil || dockerSDKErr != nil {
		err := dockerErr
		if err == nil {
			err = dockerSDKErr
		}
		logger.Warn("docker.unavailable", slog.String("error", err.Error()))
	} else {
		cmMgr = cm.NewManager(dockerCli)
		cmAdapt = newCmAdapter(cmMgr)
		ccLog := log.New(log.Options{Component: log.ComponentContainer})
		ccSrv := cc.New(cc.Options{Logger: ccLog})
		ccAdapt = newCcAdapter(ccSrv)
		ccSrv.AdoptInjector(ccAdapt, ccAdapt)
		recoverCM = newRecoveryAdapter(dockerSDK)
	}

	mcpReg := mcp.NewRegistry(mcp.Options{Store: st})
	skillMgr := skills.NewManager(skills.Options{
		BuiltinDir: opts.Layout.BuiltinSkills,
		CustomDir:  opts.Layout.CustomSkills,
	})

	// Task library — agents + assembly lines live in sqlite (one durable store
	// for all daemon state). Built-in YAMLs ship embedded in the binary
	// and are upserted into the agents/assembly_lines tables on every boot;
	// custom rows authored through the API/CLI are never overwritten.
	if written, err := ttl.Materialize(ctx, st.DB()); err != nil {
		logger.Warn("ttl.materialize_failed", slog.String("error", err.Error()))
	} else if written > 0 {
		logger.Info("ttl.builtins_materialized", slog.Int("rows", written))
	}
	taskLib := ttl.New(ttl.Options{DB: st.DB()})
	if issues, err := taskLib.Load(ctx); err == nil {
		if len(issues.AgentErrors) > 0 || len(issues.AssemblyLineErrors) > 0 {
			logger.Warn("ttl.load_issues",
				slog.Int("agent_errors", len(issues.AgentErrors)),
				slog.Int("assembly_line_errors", len(issues.AssemblyLineErrors)))
		}
		logger.Info("ttl.loaded",
			slog.Int("agents", len(taskLib.ListAgents())),
			slog.Int("assembly_lines", len(taskLib.ListAssemblyLines())))
	} else {
		logger.Warn("ttl.load_failed", slog.String("error", err.Error()))
	}

	usageLog := log.New(log.Options{Component: log.ComponentSessions})
	usageSvc := usage.New(usage.Options{
		Store:   st,
		Pricing: cfg.Pricing.Tables,
		Logger:  usageLog,
	})

	configPath := opts.Layout.ConfigFile
	managerOpts := sm.Options{
		Store:        st,
		SessionsDir:  opts.Layout.SessionsDir,
		Hub:          hub,
		Logger:       smLog,
		DefaultModel: cfg.Model.Default,
		ImageID:      cfg.Image.PinnedID,
		PinnedImageID: func() string {
			c, err := config.Load(configPath)
			if err != nil {
				return cfg.Image.PinnedID
			}
			return c.Image.PinnedID
		},
		SecretsPath:    opts.Layout.SecretsFile,
		ClaudeCredsDir: opts.Layout.ClaudeCredsDir,
		CodexCredsDir:  opts.Layout.CodexCredsDir,
		MCPs:           mcpReg,
		Skills:         newSkillsComposerAdapter(skillMgr),
		Usage:          newUsageRecorderAdapter(usageSvc),
		// Mid-session model switch (ADR 0020 §2 / Phase 4) validates the
		// requested model against this catalog before dispatching the
		// agentd.set_model frame. The closure re-reads config.toml on each
		// call so users editing [pricing.tables.models] don't have to
		// restart agentd. Models are partitioned by name prefix
		// (`claude-*` → anthropic, `gpt-*` → openai) so cross-provider
		// switches are rejected per ADR 0020 §1 — the session's immutable
		// provider field picks which list HasModel validates against.
		ProviderCatalog: func() sm.ProviderCatalog {
			c, err := config.Load(configPath)
			if err != nil {
				return sm.ProviderCatalog{}
			}
			byProv := map[string][]string{
				secrets.ProviderAnthropic: {},
				secrets.ProviderOpenAI:    {},
			}
			for name := range c.Pricing.Tables.Models {
				switch {
				case strings.HasPrefix(name, "claude-"):
					byProv[secrets.ProviderAnthropic] = append(byProv[secrets.ProviderAnthropic], name)
				case strings.HasPrefix(name, "gpt-"):
					byProv[secrets.ProviderOpenAI] = append(byProv[secrets.ProviderOpenAI], name)
				}
			}
			return sm.ProviderCatalog{ModelsByProvider: byProv}
		},
	}
	if cmAdapt != nil {
		managerOpts.Containers = cmAdapt
	}
	if ccAdapt != nil {
		managerOpts.Control = ccAdapt
	}
	manager := sm.New(managerOpts)
	defer func() { _ = manager.Shutdown(ctx) }()

	// Single provider-resolution closure shared between the CLI socket, the
	// web server, and the task-chat session runtime. Builds the resolver
	// inputs fresh on every call so rotating secrets or editing config.toml
	// is picked up without a daemon restart. ADR 0020 §3 — exactly one
	// implementation.
	providerResolver := newProviderResolver(opts.Layout, st)
	providerCatalog := newProviderCatalog(opts.Layout, st)

	// Task chat reuses the session-manager path: each task stage spawns one
	// fresh container session with its agent's prompt applied, instead of
	// the old direct-HTTP path that reimplemented auth + system framing.
	// The resolver must be wired here too — without it, sm.Create rejects
	// stages whose agent YAML doesn't pin a provider with ErrProviderRequired,
	// leaving the stage row's session_id NULL and breaking the task chat.
	taskHub := fan.NewHub()
	tmLog := log.New(log.Options{Component: "tm"})
	taskMgr := tm.New(tm.Options{
		Store:   st,
		Library: taskLib,
		Runtime: tm.NewSessionRuntime(manager, tmLog).
			WithResolver(tm.ProviderResolver(providerResolver)),
		Hub:    taskHub,
		Logger: tmLog,
	})
	_ = taskMgr

	recoverLog := log.New(log.Options{Component: log.ComponentRecovery})
	apiSrv.SetReconciling(true)
	report, recErr := recovery.Reconcile(ctx, recovery.Options{
		Store:       st,
		Containers:  recoverCM,
		Logger:      recoverLog,
		SessionsDir: opts.Layout.SessionsDir,
	})
	apiSrv.SetReconciling(false)
	if recErr != nil {
		return fmt.Errorf("recovery reconcile: %w", recErr)
	}
	if len(report.Adoptions) > 0 {
		readoptSessions(ctx, manager, ccAdapt, recoverLog, report.Adoptions)
	}

	if err := manager.Rehydrate(ctx); err != nil {
		logger.Warn("manager.rehydrate_failed", slog.String("error", err.Error()))
	}
	if err := taskMgr.Rehydrate(ctx); err != nil {
		logger.Warn("task_manager.rehydrate_failed", slog.String("error", err.Error()))
	}

	logStream := &log.SessionLogStreamer{SessionsDir: opts.Layout.SessionsDir}

	var containerLogStream socksrv.ContainerLogStreamer
	if cmMgr != nil {
		containerLogStream = newContainerLogStreamer(manager, cmMgr)
	}

	sockLog := log.New(log.Options{Component: log.ComponentSock})
	socketSrv := socksrv.New(socksrv.Options{
		SocketPath:       opts.Layout.SocketFile,
		API:              apiSrv,
		Manager:          manager,
		MCPs:             mcpReg,
		Skills:           skillMgr,
		LogStream:        logStream,
		ContainerLogs:    containerLogStream,
		Usage:            usageSvc,
		ProviderResolver: providerResolver,
		Logger:           sockLog,
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
		Addr:             cfg.Agentd.WebAddr,
		Token:            tok,
		API:              apiSrv,
		Manager:          manager,
		MCPs:             newMcpAdapter(mcpReg),
		Skills:           newSkillsAdapter(skillMgr),
		Usage:            newUsageWebAdapter(usageSvc),
		Logs:             logStream,
		Library:          taskLib,
		Tasks:            taskMgr,
		TaskHub:          taskHub,
		Secrets:          newSecretsAdapter(opts.Layout.SecretsFile),
		ProviderResolver: websrv.ProviderResolver(providerResolver),
		Providers:        providerCatalog,
		Logger:           webLog,
	})
	if err := webSrv.Start(); err != nil {
		return fmt.Errorf("web server: %w", err)
	}
	defer func() { _ = webSrv.Close() }()
	logger.Info("web.listening", slog.String("addr", webSrv.Addr()))

	sweepCtx, sweepCancel := context.WithCancel(ctx)
	defer sweepCancel()
	sweepLog := log.New(log.Options{Component: log.ComponentSweep})
	idleTimeout := parseDurationOrDefault(cfg.Session.IdleTimeout, 15*time.Minute, sweepLog, "session.idle_timeout")
	maxIdle := parseDurationOrDefault(cfg.Session.MaxIdle, 24*time.Hour, sweepLog, "session.max_idle")
	sweepers := sweep.New(sweep.Options{
		Store:       st,
		Manager:     newSweepAdapter(manager),
		SessionsDir: opts.Layout.SessionsDir,
		IdleTimeout: idleTimeout,
		MaxIdle:     maxIdle,
		Logger:      sweepLog,
	})
	sweep.RunAll(sweepCtx, sweepers, sweepLog)

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
