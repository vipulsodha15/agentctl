package sm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/agentctl/agentctl/internal/fan"
	agentlog "github.com/agentctl/agentctl/internal/log"
	"github.com/agentctl/agentctl/internal/mcp"
	"github.com/agentctl/agentctl/internal/proto"
	"github.com/agentctl/agentctl/internal/secrets"
	"github.com/agentctl/agentctl/internal/store"
	"github.com/agentctl/agentctl/internal/ulidgen"
)

type Manager interface {
	Create(ctx context.Context, req CreateRequest) (CreateResult, error)
	Send(ctx context.Context, req SendRequest) (SendResult, error)
	Interrupt(ctx context.Context, sessionID string, clearQueue bool) (InterruptResult, error)
	Attach(ctx context.Context, sessionID string) (Stream, error)
	List(ctx context.Context) ([]proto.SessionSummary, error)
	Get(ctx context.Context, sessionID string) (proto.SessionDetail, error)
	Restart(ctx context.Context, sessionID string) (RestartResult, error)
	Terminate(ctx context.Context, sessionID string) error
	Shutdown(ctx context.Context) error
	Busy(sessionID string) (busy bool, ok bool)
	Stop(ctx context.Context, sessionID string, reason string) error
	Diff(ctx context.Context, sessionID string, req DiffRequest) (DiffStream, error)
	ExportPatch(ctx context.Context, sessionID string, req DiffRequest) (DiffStream, error)
	ExportPush(ctx context.Context, sessionID string, req PushRequest) (PushResult, error)
	SessionRepos(ctx context.Context, sessionID string) ([]proto.RepoState, error)
}

type Stream = fan.Stream

type CreateRequest struct {
	Name          string
	MCPs          []string
	ExcludeMCPs   []string
	Repos         []string
	Model         string
	MemLimitBytes int64
	CPULimitCores float64
	ImageID       string
}

type CreateResult struct {
	SessionID string
	Status    string
	Summary   proto.SessionSummary
}

type SendRequest struct {
	SessionID      string
	Content        string
	ClientID       string
	IdempotencyKey string
}

type SendResult struct {
	MessageID  string
	Queued     bool
	QueueDepth int
	Idempotent bool
}

type InterruptResult struct {
	Interrupted       bool
	ClearedQueueDepth int
}

type RestartResult struct {
	SessionID string
	Status    string
	ImageID   string
}

var (
	ErrSessionNotFound   = errors.New("session not found")
	ErrAlreadyTerminated = errors.New("session already terminated")
	ErrNoInFlight        = errors.New("no in-flight turn")
	ErrSnapshotFailed    = errors.New("snapshot failed")
)

type Options struct {
	Store           *store.Store
	SessionsDir     string
	Hub             fan.Hub
	Containers      ContainerManager
	Control         ControlServer
	MCPs            mcp.Registry
	Skills          SkillsComposer
	Usage           UsageRecorder
	Logger          *slog.Logger
	Now             func() time.Time
	DefaultModel    string
	ImageID         string
	PinnedImageID   func() string
	SecretsPath     string
	ClaudeCredsDir  string
	User            string
	WebURL          func(sessionID string) string
	IdempotencyTTL  time.Duration
	SnapshotTimeout time.Duration
	ShutdownGrace   time.Duration
}

type manager struct {
	opts    Options
	hub     fan.Hub
	store   *store.Store
	logger  *slog.Logger
	now     func() time.Time
	mu      sync.Mutex
	actors  map[string]*actor
	closing bool
}

func New(opts Options) Manager {
	if opts.Hub == nil {
		opts.Hub = fan.NewHub()
	}
	if opts.Logger == nil {
		opts.Logger = agentlog.New(agentlog.Options{Component: agentlog.ComponentSessions})
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	if opts.SnapshotTimeout == 0 {
		opts.SnapshotTimeout = 10 * time.Second
	}
	if opts.ShutdownGrace == 0 {
		opts.ShutdownGrace = 30 * time.Second
	}
	if opts.IdempotencyTTL == 0 {
		opts.IdempotencyTTL = 5 * time.Minute
	}
	return &manager{
		opts:   opts,
		hub:    opts.Hub,
		store:  opts.Store,
		logger: opts.Logger,
		now:    opts.Now,
		actors: make(map[string]*actor),
	}
}

func (m *manager) Hub() fan.Hub { return m.hub }

func (m *manager) actorFor(id string) *actor {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.actors[id]
}

func (m *manager) Create(ctx context.Context, req CreateRequest) (CreateResult, error) {
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return CreateResult{}, fmt.Errorf("manager: shutting down")
	}
	m.mu.Unlock()

	id := ulidgen.WithPrefix("sess")
	now := m.now()
	model := req.Model
	if model == "" {
		model = m.opts.DefaultModel
	}
	resolvedEntries := m.resolveMCPs(ctx, req.MCPs, req.ExcludeMCPs)
	mcps := req.MCPs
	if mcps == nil {
		mcps = make([]string, 0, len(resolvedEntries))
		for _, e := range resolvedEntries {
			mcps = append(mcps, e.Name)
		}
	}
	repos := req.Repos
	if repos == nil {
		repos = []string{}
	}
	mcpJSON, _ := json.Marshal(mcps)
	reposState := make([]proto.RepoState, 0, len(repos))
	for _, url := range repos {
		reposState = append(reposState, proto.RepoState{Name: filepath.Base(url), URL: url})
	}
	reposJSON, _ := json.Marshal(reposState)

	dir := filepath.Join(m.opts.SessionsDir, id)
	volumeDir := filepath.Join(dir, "volume")
	if err := os.MkdirAll(volumeDir, 0o700); err != nil {
		return CreateResult{}, fmt.Errorf("session dir: %w", err)
	}
	// Container runs as uid 1000 and bind-mounts this dir at /work; needs write
	// access. Chown to 1000:1000 so the agent user owns it. Best-effort: if the
	// Chown fails (e.g. inside a user-namespace remap or rootless docker), the
	// container may still work with relaxed perms; we fall back to 0777.
	if err := os.Chown(volumeDir, 1000, 1000); err != nil {
		_ = os.Chmod(volumeDir, 0o777)
	}
	// Reserve the per-session control dir for legacy compatibility (unix-sock
	// tests still write there), but it is no longer bind-mounted into the
	// container. Production uses a TCP listener on 127.0.0.1; the container
	// reaches it via host.docker.internal, which sidesteps Docker Desktop's
	// fs-share refusing to pass unix sockets through bind-mounts.
	controlDir := filepath.Join(dir, "control")
	if err := os.MkdirAll(controlDir, 0o700); err != nil {
		return CreateResult{}, fmt.Errorf("control dir: %w", err)
	}
	skillsDir := filepath.Join(dir, "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		return CreateResult{}, fmt.Errorf("skills dir: %w", err)
	}
	var skillsResult SkillsComposeResult
	if m.opts.Skills != nil {
		res, cerr := m.opts.Skills.Compose(skillsDir)
		if cerr != nil {
			return CreateResult{}, fmt.Errorf("skills compose: %w", cerr)
		}
		skillsResult = res
	} else {
		skillsResult.Path = skillsDir
	}

	sessionToken, err := generateToken()
	if err != nil {
		return CreateResult{}, fmt.Errorf("session token: %w", err)
	}
	agentlog.RegisterSecret(sessionToken)

	sessionMeta := map[string]any{
		"v":             1,
		"session_id":    id,
		"session_token": sessionToken,
		"model":         model,
		"created_at":    now.Format(time.RFC3339Nano),
	}
	metaBytes, _ := json.MarshalIndent(sessionMeta, "", "  ")
	if err := writeFileSecure(filepath.Join(dir, "session.json"), metaBytes); err != nil {
		return CreateResult{}, err
	}

	logger, err := agentlog.NewSessionLogger(agentlog.SessionLogOptions{Dir: dir})
	if err != nil {
		return CreateResult{}, fmt.Errorf("session log: %w", err)
	}

	imageID := req.ImageID
	if imageID == "" {
		imageID = m.opts.ImageID
	}
	mem := req.MemLimitBytes
	if mem == 0 {
		mem = 4 * 1024 * 1024 * 1024
	}
	cpus := req.CPULimitCores
	if cpus == 0 {
		cpus = 2.0
	}

	envFile := filepath.Join(dir, "secrets.env")
	if err := writeSecretsEnv(envFile, m.opts.SecretsPath, secretsEnvInputs{
		SessionID:    id,
		SessionName:  defaultName(req.Name, id),
		Model:        model,
		SessionToken: sessionToken,
	}); err != nil {
		_ = logger.Close()
		return CreateResult{}, err
	}

	authMode := secrets.AuthModeAPIKey
	claudeCredsBindSource := ""
	if m.opts.SecretsPath != "" {
		if sec, err := secrets.Load(m.opts.SecretsPath); err == nil {
			authMode = sec.ResolvedAuthMode()
		}
	}
	if authMode == secrets.AuthModeOAuth {
		if m.opts.ClaudeCredsDir == "" {
			_ = logger.Close()
			return CreateResult{}, fmt.Errorf("auth mode=oauth but ClaudeCredsDir not configured; check agentd setup")
		}
		credFile := filepath.Join(m.opts.ClaudeCredsDir, ".credentials.json")
		if info, err := os.Stat(credFile); err != nil || info.Size() == 0 {
			_ = logger.Close()
			return CreateResult{}, fmt.Errorf("auth mode=oauth but %s missing or empty; run `agentctl auth login`", credFile)
		}
		claudeCredsBindSource = m.opts.ClaudeCredsDir
	}

	if m.store != nil {
		// control_sock_path is populated after Listen returns the assigned TCP
		// address in provisionContainer; insert with empty here.
		if _, err := m.store.DB().ExecContext(ctx, `INSERT INTO sessions
            (id, name, status, created_at, last_activity_at,
             image_id, volume_path, control_sock_path, skills_snapshot_path, skills_snapshot_hash,
             model, mem_limit_bytes, cpu_limit_cores, mcp_set_json, repos_json, session_token)
             VALUES (?, ?, 'starting', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, defaultName(req.Name, id), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
			imageID, filepath.Join(dir, "volume"), "",
			skillsResult.Path, skillsResult.Hash,
			model, mem, cpus, string(mcpJSON), string(reposJSON), sessionToken,
		); err != nil {
			_ = logger.Close()
			return CreateResult{}, fmt.Errorf("insert session: %w", err)
		}
		if _, err := m.store.DB().ExecContext(ctx, `INSERT INTO session_lifecycle (session_id, at, event, detail_json) VALUES (?, ?, 'created', ?)`,
			id, now.Format(time.RFC3339Nano), `{}`); err != nil {
			m.logger.Warn("lifecycle.insert_failed", slog.String("session_id", id), slog.String("error", err.Error()))
		}
	}

	summary := proto.SessionSummary{
		ID:             id,
		Name:           defaultName(req.Name, id),
		Status:         "starting",
		CreatedAt:      now,
		LastActivityAt: now,
		ImageID:        imageID,
		Model:          model,
		MCPs:           mcps,
		Repos:          repoNames(reposState),
		MemLimitBytes:  mem,
		CPULimitCores:  cpus,
	}

	pat := ""
	if m.opts.SecretsPath != "" {
		if sec, serr := secrets.Load(m.opts.SecretsPath); serr == nil {
			pat = sec.GitHubPAT
		}
	}
	a := newActor(actorOptions{
		ID:              id,
		Summary:         summary,
		SessionDir:      dir,
		Hub:             m.hub,
		Logger:          logger.Logger(),
		DaemonLogger:    m.logger,
		SessionLogger:   logger,
		Now:             m.now,
		SnapshotTimeout: m.opts.SnapshotTimeout,
		ShutdownGrace:   m.opts.ShutdownGrace,
		Containers:      m.opts.Containers,
		Control:         m.opts.Control,
		SessionToken:    sessionToken,
		Store:           m.store,
		Repos:           reposState,
		ResolvedMCPs:    resolvedEntries,
		GitHubPAT:       pat,
		SkillCollisions: skillsResult.Collisions,
		Usage:           m.opts.Usage,
	})
	m.mu.Lock()
	m.actors[id] = a
	m.mu.Unlock()
	a.start()

	go m.probeAndRecord(id, a, resolvedEntries)

	a.broadcast(proto.EventSessionStarting, mustJSON(map[string]any{"phase": "container"}))

	if err := m.provisionContainer(ctx, a, provisionInputs{
		SessionID:       id,
		Name:            defaultName(req.Name, id),
		ImageID:         imageID,
		EnvFile:         envFile,
		VolumeDir:       filepath.Join(dir, "volume"),
		SkillsDir:       skillsResult.Path,
		ClaudeCredsHost: claudeCredsBindSource,
		MemBytes:        mem,
		CPUs:            cpus,
		SessionToken:    sessionToken,
	}); err != nil {
		m.logger.Warn("session.provision_failed", slog.String("session_id", id), slog.String("error", err.Error()))
		return CreateResult{SessionID: id, Status: summary.Status, Summary: summary}, err
	}

	return CreateResult{SessionID: id, Status: "starting", Summary: summary}, nil
}

type provisionInputs struct {
	SessionID       string
	Name            string
	ImageID         string
	EnvFile         string
	VolumeDir       string
	SkillsDir       string
	ClaudeCredsHost string
	MemBytes        int64
	CPUs            float64
	SessionToken    string
	NetworkID       string
	NetworkName     string
}

// provisionContainer runs the Create -> Listen -> Start sequence from
// overview.md §6.2. When the container manager is absent (the no-Docker
// happy path: tests, or a host whose docker socket is unreachable) the
// actor still runs and tests can drive it via InjectControlConn; the
// session is just marked error with last_error="docker_unavailable" so
// `agentctl status` surfaces the cause.
func (m *manager) provisionContainer(ctx context.Context, a *actor, in provisionInputs) error {
	if m.opts.Containers == nil {
		const reason = "docker_unavailable"
		m.markSessionError(ctx, in.SessionID, reason)
		a.markError(reason)
		return nil
	}
	if m.opts.Control == nil {
		const reason = "control_server_unavailable"
		m.markSessionError(ctx, in.SessionID, reason)
		a.markError(reason)
		return nil
	}
	if in.ImageID == "" || strings.HasPrefix(in.ImageID, "sha256:pending") {
		const reason = "image_not_pinned"
		m.markSessionError(ctx, in.SessionID, reason)
		a.markError(reason)
		return fmt.Errorf("no image pinned; run `agentctl init` or `agentctl update`")
	}

	user := m.opts.User
	if user == "" {
		user = os.Getenv("USER")
	}
	now := m.now()
	labels := map[string]string{
		"agentctl.session":    in.SessionID,
		"agentctl.image_id":   in.ImageID,
		"agentctl.created_at": now.Format(time.RFC3339),
		"agentctl.user":       user,
	}
	short := suffix(in.SessionID)
	netID := in.NetworkID
	netName := in.NetworkName
	createdNetwork := false
	if netID == "" {
		ref, err := m.opts.Containers.NetworkCreate(ctx, in.SessionID, "agentctl-"+short)
		if err != nil {
			m.markSessionError(ctx, in.SessionID, "network_create_failed: "+err.Error())
			a.markError("network_create_failed")
			return fmt.Errorf("network create: %w", err)
		}
		netID = ref.ID
		netName = ref.Name
		createdNetwork = true
		a.setNetwork(netID, netName)
		if m.store != nil {
			if _, err := m.store.DB().ExecContext(ctx,
				`UPDATE sessions SET network_id=? WHERE id=?`,
				netID, in.SessionID); err != nil {
				m.logger.Warn("session.network_id_update_failed", slog.String("session_id", in.SessionID), slog.String("error", err.Error()))
			}
		}
	}
	mounts := []ContainerMount{
		{Type: MountBind, Source: in.VolumeDir, Target: "/work"},
	}
	if in.SkillsDir != "" {
		mounts = append(mounts, ContainerMount{
			Type: MountBind, Source: in.SkillsDir, Target: "/skills", ReadOnly: true,
		})
	}
	// OAuth mode: mount the host's Claude credentials directory at
	// /home/agent/.claude so the bundled `claude` CLI (spawned by the Agent
	// SDK inside the container) finds .credentials.json on disk. Read-write
	// is required because the CLI refreshes the access token in place — that
	// write goes back to our snapshot, not the user's host ~/.claude.
	if in.ClaudeCredsHost != "" {
		mounts = append(mounts, ContainerMount{
			Type:   MountBind,
			Source: in.ClaudeCredsHost,
			Target: "/home/agent/.claude",
		})
	}

	// Listen first so we know the host port to hand to the container. TCP on
	// 127.0.0.1 is reachable from the container via host.docker.internal,
	// which Docker Desktop resolves natively and Linux Docker resolves via
	// the host-gateway ExtraHost below.
	handler := func(conn ControlConn) { a.InjectControlConn(conn) }
	resolvedAddr, err := m.opts.Control.Listen(in.SessionID, "tcp", "127.0.0.1:0", in.SessionToken, handler)
	if err != nil {
		if createdNetwork {
			removeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = m.opts.Containers.NetworkRemove(removeCtx, netID)
			cancel()
		}
		m.markSessionError(ctx, in.SessionID, "control_listen_failed: "+err.Error())
		a.markError("control_listen_failed")
		return fmt.Errorf("control listen: %w", err)
	}
	containerAddr, err := containerControlAddr(resolvedAddr)
	if err != nil {
		_ = m.opts.Control.Stop(in.SessionID)
		if createdNetwork {
			removeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = m.opts.Containers.NetworkRemove(removeCtx, netID)
			cancel()
		}
		m.markSessionError(ctx, in.SessionID, "control_addr_invalid: "+err.Error())
		a.markError("control_addr_invalid")
		return fmt.Errorf("control addr: %w", err)
	}
	if m.store != nil {
		if _, err := m.store.DB().ExecContext(ctx,
			`UPDATE sessions SET control_sock_path=? WHERE id=?`,
			resolvedAddr, in.SessionID); err != nil {
			m.logger.Warn("session.control_addr_update_failed", slog.String("session_id", in.SessionID), slog.String("error", err.Error()))
		}
	}

	spec := ContainerSpec{
		SessionID:      in.SessionID,
		ImageID:        in.ImageID,
		Name:           "agentctl-" + short,
		Labels:         labels,
		EnvFile:        in.EnvFile,
		Env:            []string{"AGENTCTL_CONTROL_ADDR=" + containerAddr},
		Mounts:         mounts,
		MemBytes:       in.MemBytes,
		CPUs:           in.CPUs,
		MemorySwap:     in.MemBytes,
		NetworkID:      netName,
		ReadOnlyRootFS: true,
		CapDrop:        []string{"ALL"},
		SecurityOpts:   []string{"no-new-privileges"},
		PidsLimit:      512,
		ExtraHosts:     []string{"host.docker.internal:host-gateway"},
		Tmpfs: map[string]string{
			"/home/agent": "rw,size=64m,mode=0700,uid=1000,gid=1000",
			"/tmp":        "rw,size=128m,mode=1777",
		},
	}

	handle, err := m.opts.Containers.Create(ctx, spec)
	if err != nil {
		_ = m.opts.Control.Stop(in.SessionID)
		if createdNetwork {
			removeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = m.opts.Containers.NetworkRemove(removeCtx, netID)
			cancel()
		}
		m.markSessionError(ctx, in.SessionID, "container_create_failed: "+err.Error())
		a.markError("container_create_failed")
		return fmt.Errorf("container create: %w", err)
	}

	if err := m.opts.Containers.Start(ctx, handle.ID); err != nil {
		_ = m.opts.Control.Stop(in.SessionID)
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = m.opts.Containers.Stop(stopCtx, handle.ID, time.Second)
		_ = m.opts.Containers.Remove(stopCtx, handle.ID, true)
		if createdNetwork {
			_ = m.opts.Containers.NetworkRemove(stopCtx, netID)
		}
		cancel()
		m.markSessionError(ctx, in.SessionID, "container_start_failed: "+err.Error())
		a.markError("container_start_failed")
		return fmt.Errorf("container start: %w", err)
	}

	a.setContainerID(handle.ID)
	if m.store != nil {
		if _, err := m.store.DB().ExecContext(ctx,
			`UPDATE sessions SET container_id=? WHERE id=?`,
			handle.ID, in.SessionID); err != nil {
			m.logger.Warn("session.container_id_update_failed", slog.String("session_id", in.SessionID), slog.String("error", err.Error()))
		}
	}
	return nil
}

func (m *manager) markSessionError(ctx context.Context, sessionID, reason string) {
	if m.store == nil {
		return
	}
	if _, err := m.store.DB().ExecContext(ctx,
		`UPDATE sessions SET status='error', last_error=? WHERE id=?`,
		reason, sessionID); err != nil {
		m.logger.Warn("session.error_update_failed", slog.String("session_id", sessionID), slog.String("error", err.Error()))
	}
}

func suffix(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[len(id)-8:]
}

// containerControlAddr swaps the host-side bind address (127.0.0.1 / ::1 /
// 0.0.0.0) for host.docker.internal, which is the name the container uses to
// reach the host. Unix-socket paths (legacy / tests) are passed through.
func containerControlAddr(addr string) (string, error) {
	if addr == "" {
		return "", errors.New("empty control address")
	}
	if strings.HasPrefix(addr, "/") {
		return addr, nil
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("parse control addr %q: %w", addr, err)
	}
	switch host {
	case "127.0.0.1", "::1", "0.0.0.0", "", "::":
		host = "host.docker.internal"
	}
	return net.JoinHostPort(host, port), nil
}

type secretsEnvInputs struct {
	SessionID    string
	SessionName  string
	Model        string
	SessionToken string
}

func writeSecretsEnv(path, secretsPath string, in secretsEnvInputs) error {
	pairs := map[string]string{
		"SESSION_ID":             in.SessionID,
		"SESSION_NAME":           in.SessionName,
		"AGENTCTL_MODEL":         in.Model,
		"AGENTCTL_SESSION_TOKEN": in.SessionToken,
	}
	if secretsPath != "" {
		if sec, err := secrets.Load(secretsPath); err == nil {
			// Auth selection precedence (most-explicit first):
			//   1. oauth mode  -> inject nothing; the session container reads
			//      .credentials.json from a bind-mounted /home/agent/.claude/.
			//      Per Anthropic's auth precedence, ANTHROPIC_API_KEY /
			//      ANTHROPIC_AUTH_TOKEN would override the OAuth subscription
			//      and silently bill the wrong account.
			//   2. custom endpoint -> ANTHROPIC_AUTH_TOKEN (+ optional
			//      ANTHROPIC_BASE_URL) for routing through an LLM gateway.
			//   3. api_key -> ANTHROPIC_API_KEY (the historical default).
			switch {
			case sec.ResolvedAuthMode() == secrets.AuthModeOAuth:
				// no Anthropic env var injected
			case sec.AnthropicAuthToken != "":
				pairs["ANTHROPIC_AUTH_TOKEN"] = sec.AnthropicAuthToken
				if sec.AnthropicBaseURL != "" {
					pairs["ANTHROPIC_BASE_URL"] = sec.AnthropicBaseURL
				}
			case sec.AnthropicAPIKey != "":
				pairs["ANTHROPIC_API_KEY"] = sec.AnthropicAPIKey
			}
			if sec.GitHubPAT != "" {
				pairs["GITHUB_TOKEN"] = sec.GitHubPAT
			}
		}
	}
	keys := make([]string, 0, len(pairs))
	for k := range pairs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		v := pairs[k]
		if v == "" {
			continue
		}
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(v)
		b.WriteString("\n")
	}
	return writeFileSecure(path, []byte(b.String()))
}

func (m *manager) Send(ctx context.Context, req SendRequest) (SendResult, error) {
	a := m.actorFor(req.SessionID)
	if a == nil {
		return SendResult{}, ErrSessionNotFound
	}
	// A stopped session has no container and no control conn. handleSend would
	// park the message in the queue, but the only path that drains the queue
	// (the RuntimeReady frame from the shim) can't fire because there is no
	// shim. Bring the container back up first; the Send below then enqueues
	// through the normal path and the new shim pops it once it signals ready.
	if a.snapshotSummary().Status == "stopped" {
		if _, err := m.Restart(ctx, req.SessionID); err != nil {
			return SendResult{}, fmt.Errorf("restart stopped session: %w", err)
		}
	}
	if req.IdempotencyKey != "" && m.store != nil {
		if existing, ok := m.checkIdempotency(req.SessionID, req.IdempotencyKey); ok {
			return SendResult{MessageID: existing, Idempotent: true, QueueDepth: a.queueDepth()}, nil
		}
	}
	msgID := ulidgen.WithPrefix("msg")
	if req.IdempotencyKey != "" && m.store != nil {
		_ = m.recordIdempotency(req.SessionID, req.IdempotencyKey, msgID)
	}
	resCh := make(chan SendResult, 1)
	a.mailbox <- mboxItem{kind: mboxSend, send: &sendItem{
		messageID: msgID,
		content:   req.Content,
		clientID:  req.ClientID,
		reply:     resCh,
	}}
	select {
	case r := <-resCh:
		return r, nil
	case <-ctx.Done():
		return SendResult{}, ctx.Err()
	}
}

func (m *manager) Interrupt(ctx context.Context, sessionID string, clearQueue bool) (InterruptResult, error) {
	a := m.actorFor(sessionID)
	if a == nil {
		return InterruptResult{}, ErrSessionNotFound
	}
	resCh := make(chan InterruptResult, 1)
	errCh := make(chan error, 1)
	a.mailbox <- mboxItem{kind: mboxInterrupt, interrupt: &interruptItem{
		clearQueue: clearQueue,
		reply:      resCh,
		errReply:   errCh,
	}}
	select {
	case r := <-resCh:
		return r, nil
	case e := <-errCh:
		return InterruptResult{}, e
	case <-ctx.Done():
		return InterruptResult{}, ctx.Err()
	}
}

func (m *manager) Attach(ctx context.Context, sessionID string) (Stream, error) {
	a := m.actorFor(sessionID)
	if a == nil {
		return nil, ErrSessionNotFound
	}
	stream, cancel, err := m.hub.Subscribe(sessionID)
	if err != nil {
		return nil, err
	}
	snapshot, err := a.snapshotEvent(ctx)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("%w: %v", ErrSnapshotFailed, err)
	}
	stream = &snapshotPrependedStream{first: snapshot, inner: stream, cancel: cancel}
	return stream, nil
}

func (m *manager) List(_ context.Context) ([]proto.SessionSummary, error) {
	m.mu.Lock()
	out := make([]proto.SessionSummary, 0, len(m.actors))
	for _, a := range m.actors {
		out = append(out, a.snapshotSummary())
	}
	m.mu.Unlock()
	return out, nil
}

func (m *manager) Get(_ context.Context, sessionID string) (proto.SessionDetail, error) {
	a := m.actorFor(sessionID)
	if a == nil {
		return proto.SessionDetail{}, ErrSessionNotFound
	}
	return a.snapshotDetail(), nil
}

func (m *manager) Restart(ctx context.Context, sessionID string) (RestartResult, error) {
	a := m.actorFor(sessionID)
	if a == nil {
		return RestartResult{}, ErrSessionNotFound
	}
	imageID := m.currentImageID()
	if imageID == "" || strings.HasPrefix(imageID, "sha256:pending") {
		return RestartResult{}, fmt.Errorf("no image pinned; run `agentctl update`")
	}
	if _, err := m.Interrupt(ctx, sessionID, false); err != nil && !errors.Is(err, ErrNoInFlight) {
		m.logger.Warn("session.restart.interrupt_failed", slog.String("session_id", sessionID), slog.String("error", err.Error()))
	}
	containerID, networkID, networkName := a.snapshotIDs()
	if m.opts.Containers != nil && containerID != "" {
		stopCtx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		_ = m.opts.Containers.Stop(stopCtx, containerID, 30*time.Second)
		_ = m.opts.Containers.Remove(stopCtx, containerID, true)
		cancel()
	}
	if m.opts.Control != nil {
		_ = m.opts.Control.Stop(sessionID)
	}
	a.markRestarting(imageID)

	dir := filepath.Join(m.opts.SessionsDir, sessionID)
	envFile := filepath.Join(dir, "secrets.env")
	summary := a.snapshotSummary()
	in := provisionInputs{
		SessionID:    sessionID,
		Name:         summary.Name,
		ImageID:      imageID,
		EnvFile:      envFile,
		VolumeDir:    filepath.Join(dir, "volume"),
		MemBytes:     summary.MemLimitBytes,
		CPUs:         summary.CPULimitCores,
		SessionToken: a.opts.SessionToken,
		NetworkID:    networkID,
		NetworkName:  networkName,
	}
	if err := m.provisionContainer(ctx, a, in); err != nil {
		return RestartResult{}, err
	}
	if m.store != nil {
		if _, err := m.store.DB().ExecContext(ctx,
			`UPDATE sessions SET status='running', image_id=? WHERE id=?`,
			imageID, sessionID); err != nil {
			m.logger.Warn("session.restart.db_update_failed", slog.String("session_id", sessionID), slog.String("error", err.Error()))
		}
		_, _ = m.store.DB().ExecContext(ctx,
			`INSERT INTO session_lifecycle (session_id, at, event, detail_json) VALUES (?, ?, 'resumed', ?)`,
			sessionID, m.now().Format(time.RFC3339Nano), `{"reason":"restart"}`)
	}
	a.broadcast(proto.EventSessionResumed, mustJSON(map[string]string{"image_id": imageID}))
	return RestartResult{SessionID: sessionID, Status: "running", ImageID: imageID}, nil
}

func (m *manager) currentImageID() string {
	if m.opts.PinnedImageID != nil {
		if id := m.opts.PinnedImageID(); id != "" {
			return id
		}
	}
	return m.opts.ImageID
}

func (m *manager) Terminate(ctx context.Context, sessionID string) error {
	a := m.actorFor(sessionID)
	if a == nil {
		return ErrSessionNotFound
	}
	resCh := make(chan error, 1)
	a.mailbox <- mboxItem{kind: mboxTerminate, terminate: &terminateItem{reply: resCh}}
	select {
	case err := <-resCh:
		m.mu.Lock()
		delete(m.actors, sessionID)
		m.mu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *manager) Busy(sessionID string) (bool, bool) {
	a := m.actorFor(sessionID)
	if a == nil {
		return false, false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	busy := a.inFlight != "" || len(a.queue) > 0
	return busy, true
}

func (m *manager) Stop(ctx context.Context, sessionID, reason string) error {
	a := m.actorFor(sessionID)
	if a == nil {
		return ErrSessionNotFound
	}
	resCh := make(chan error, 1)
	a.mailbox <- mboxItem{kind: mboxStop, stop: &stopItem{reason: reason, reply: resCh}}
	select {
	case err := <-resCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.closing = true
	actors := make([]*actor, 0, len(m.actors))
	for _, a := range m.actors {
		actors = append(actors, a)
	}
	m.mu.Unlock()
	for _, a := range actors {
		a.shutdown()
	}
	return nil
}

func (m *manager) checkIdempotency(sessionID, key string) (string, bool) {
	row := m.store.DB().QueryRow(`SELECT message_id, accepted_at FROM message_idempotency WHERE session_id=? AND idempotency_key=?`, sessionID, key)
	var msgID, at string
	if err := row.Scan(&msgID, &at); err != nil {
		return "", false
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return "", false
	}
	if m.now().Sub(t) > m.opts.IdempotencyTTL {
		return "", false
	}
	return msgID, true
}

func (m *manager) recordIdempotency(sessionID, key, msgID string) error {
	_, err := m.store.DB().Exec(`INSERT OR REPLACE INTO message_idempotency (session_id, idempotency_key, message_id, accepted_at) VALUES (?, ?, ?, ?)`,
		sessionID, key, msgID, m.now().Format(time.RFC3339Nano))
	return err
}

func defaultName(name, id string) string {
	if name != "" {
		return name
	}
	return id
}

func repoNames(rs []proto.RepoState) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		if r.Name != "" {
			out = append(out, r.Name)
		} else {
			out = append(out, r.URL)
		}
	}
	return out
}

func writeFileSecure(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

type snapshotPrependedStream struct {
	first  *proto.Event
	inner  fan.Stream
	cancel func()
}

func (s *snapshotPrependedStream) Recv() (proto.Event, bool, string) {
	if s.first != nil {
		ev := *s.first
		s.first = nil
		return ev, true, ""
	}
	return s.inner.Recv()
}

func (s *snapshotPrependedStream) Close() {
	if s.cancel != nil {
		s.cancel()
	}
	s.inner.Close()
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`null`)
	}
	return b
}

func (m *manager) resolveMCPs(ctx context.Context, requested, excluded []string) []mcp.Entry {
	if m.opts.MCPs == nil {
		return nil
	}
	all, err := m.opts.MCPs.List(ctx)
	if err != nil {
		m.logger.Warn("mcp.list_failed", slog.String("error", err.Error()))
		return nil
	}
	return mcp.Resolve(all, requested, excluded)
}

func (m *manager) probeAndRecord(sessionID string, a *actor, entries []mcp.Entry) {
	if len(entries) == 0 {
		a.applyMCPStatus(map[string]string{}, nil)
		return
	}
	results := mcp.ProbeAll(context.Background(), entries, mcp.ProbeOptions{})
	statuses := mcp.ProbeResultsToStatusMap(results)
	transports := make(map[string]string, len(entries))
	for _, e := range entries {
		transports[e.Name] = e.Transport
	}
	failures := make([]proto.MCPUnreachableData, 0)
	for _, r := range results {
		if !r.OK {
			failures = append(failures, proto.MCPUnreachableData{
				Name: r.Name, Transport: transports[r.Name], Error: r.Reason,
			})
		}
	}
	a.applyMCPStatus(statuses, failures)
	if m.store != nil {
		body, _ := json.Marshal(statuses)
		if _, err := m.store.DB().Exec(`UPDATE sessions SET mcp_status_json = ? WHERE id = ?`, string(body), sessionID); err != nil {
			m.logger.Warn("mcp.status_write_failed", slog.String("session_id", sessionID), slog.String("error", err.Error()))
		}
	}
}
