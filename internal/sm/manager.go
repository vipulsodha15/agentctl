package sm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/agentctl/agentctl/internal/fan"
	agentlog "github.com/agentctl/agentctl/internal/log"
	"github.com/agentctl/agentctl/internal/proto"
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
	Logger          *slog.Logger
	Now             func() time.Time
	DefaultModel    string
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
	mcps := req.MCPs
	if mcps == nil {
		mcps = []string{}
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
	if err := writeFileSecure(filepath.Join(dir, "secrets.env"), []byte("")); err != nil {
		return CreateResult{}, err
	}

	logger, err := agentlog.NewSessionLogger(agentlog.SessionLogOptions{Dir: dir})
	if err != nil {
		return CreateResult{}, fmt.Errorf("session log: %w", err)
	}

	imageID := req.ImageID
	if imageID == "" {
		imageID = "sha256:pending"
	}
	mem := req.MemLimitBytes
	if mem == 0 {
		mem = 4 * 1024 * 1024 * 1024
	}
	cpus := req.CPULimitCores
	if cpus == 0 {
		cpus = 2.0
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
		ControlSockPath: filepath.Join(dir, "control", "agentd.sock"),
		SessionToken:    sessionToken,
		Store:           m.store,
		Repos:           reposState,
	})
	m.mu.Lock()
	m.actors[id] = a
	m.mu.Unlock()
	a.start()

	a.broadcast(proto.EventSessionStarting, mustJSON(map[string]any{"phase": "container"}))

	return CreateResult{SessionID: id, Status: "starting", Summary: summary}, nil
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
