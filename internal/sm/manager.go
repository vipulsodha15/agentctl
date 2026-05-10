package sm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	Terminate(ctx context.Context, sessionID string) error
	Shutdown(ctx context.Context) error
	Busy(sessionID string) (busy bool, ok bool)
	Stop(ctx context.Context, sessionID string, reason string) error
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
	Logger          *slog.Logger
	Now             func() time.Time
	DefaultModel    string
	ImageID         string
	SecretsPath     string
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
	if err := os.MkdirAll(filepath.Join(dir, "volume"), 0o700); err != nil {
		return CreateResult{}, fmt.Errorf("session dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "control"), 0o700); err != nil {
		return CreateResult{}, fmt.Errorf("control dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "skills"), 0o700); err != nil {
		return CreateResult{}, fmt.Errorf("skills dir: %w", err)
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

	if m.store != nil {
		if _, err := m.store.DB().ExecContext(ctx, `INSERT INTO sessions
            (id, name, status, created_at, last_activity_at,
             image_id, volume_path, control_sock_path, skills_snapshot_path, skills_snapshot_hash,
             model, mem_limit_bytes, cpu_limit_cores, mcp_set_json, repos_json, session_token)
             VALUES (?, ?, 'starting', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, defaultName(req.Name, id), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
			imageID, filepath.Join(dir, "volume"), filepath.Join(dir, "control", "agentd.sock"),
			filepath.Join(dir, "skills"), "",
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

	sockPath := filepath.Join(dir, "control", "agentd.sock")
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
		ControlSockPath: sockPath,
		SessionToken:    sessionToken,
		Store:           m.store,
		Repos:           reposState,
		ResolvedMCPs:    resolvedEntries,
		GitHubPAT:       pat,
	})
	m.mu.Lock()
	m.actors[id] = a
	m.mu.Unlock()
	a.start()

	go m.probeAndRecord(id, a, resolvedEntries)

	a.broadcast(proto.EventSessionStarting, mustJSON(map[string]any{"phase": "container"}))

	if err := m.provisionContainer(ctx, a, provisionInputs{
		SessionID:    id,
		Name:         defaultName(req.Name, id),
		ImageID:      imageID,
		EnvFile:      envFile,
		VolumeDir:    filepath.Join(dir, "volume"),
		ControlDir:   filepath.Join(dir, "control"),
		SockPath:     sockPath,
		MemBytes:     mem,
		CPUs:         cpus,
		SessionToken: sessionToken,
	}); err != nil {
		m.logger.Warn("session.provision_failed", slog.String("session_id", id), slog.String("error", err.Error()))
		return CreateResult{SessionID: id, Status: summary.Status, Summary: summary}, err
	}

	return CreateResult{SessionID: id, Status: "starting", Summary: summary}, nil
}

type provisionInputs struct {
	SessionID    string
	Name         string
	ImageID      string
	EnvFile      string
	VolumeDir    string
	ControlDir   string
	SockPath     string
	MemBytes     int64
	CPUs         float64
	SessionToken string
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
	spec := ContainerSpec{
		SessionID: in.SessionID,
		ImageID:   in.ImageID,
		Name:      "agentctl-" + suffix(in.SessionID),
		Labels:    labels,
		EnvFile:   in.EnvFile,
		Mounts: []ContainerMount{
			{Type: MountBind, Source: in.VolumeDir, Target: "/work"},
			{Type: MountBind, Source: in.ControlDir, Target: "/run/agentctl/control"},
		},
		MemBytes: in.MemBytes,
		CPUs:     in.CPUs,
	}

	handle, err := m.opts.Containers.Create(ctx, spec)
	if err != nil {
		m.markSessionError(ctx, in.SessionID, "container_create_failed: "+err.Error())
		a.markError("container_create_failed")
		return fmt.Errorf("container create: %w", err)
	}

	handler := func(conn ControlConn) { a.InjectControlConn(conn) }
	if err := m.opts.Control.Listen(in.SessionID, in.SockPath, in.SessionToken, handler); err != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = m.opts.Containers.Stop(stopCtx, handle.ID, time.Second)
		_ = m.opts.Containers.Remove(stopCtx, handle.ID, true)
		cancel()
		m.markSessionError(ctx, in.SessionID, "control_listen_failed: "+err.Error())
		a.markError("control_listen_failed")
		return fmt.Errorf("control listen: %w", err)
	}

	if err := m.opts.Containers.Start(ctx, handle.ID); err != nil {
		_ = m.opts.Control.Stop(in.SessionID)
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = m.opts.Containers.Stop(stopCtx, handle.ID, time.Second)
		_ = m.opts.Containers.Remove(stopCtx, handle.ID, true)
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
			if sec.AnthropicAPIKey != "" {
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
