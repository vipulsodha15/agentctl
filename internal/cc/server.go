package cc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// TokenVerifier resolves a session_token presented by a shim into the
// session_id it owns. Returning ("", false) means the token is invalid; the
// connection is closed without sending agentd.greet.
type TokenVerifier interface {
	Verify(token string) (sessionID string, ok bool)
}

// Adopter is the session-actor side of the handoff. The cc server invokes it
// with an authenticated connection and a synthetic-event sink. The actor takes
// ownership and is responsible for closing the connection on shutdown.
type Adopter interface {
	Adopt(ctx context.Context, sessionID string, conn Conn, events <-chan Frame)
}

// Server listens on per-session control endpoints, authenticates incoming
// shims, and hands successful connections off to the session-actor layer.
//
// `network` is "tcp" or "unix" (Go's net package conventions). For TCP, the
// container reaches agentd via host.docker.internal so a bind-mounted unix
// socket (which Docker Desktop / WSL2 fs-shares don't pass through reliably)
// isn't needed. For unix, the legacy behaviour is preserved for tests.
//
// Listen returns the resolved listening address (e.g. "127.0.0.1:54321"). The
// caller is responsible for storing this and telling the container how to
// reach it.
type Server interface {
	Listen(sessionID, network, addr string) (string, error)
	Stop(sessionID string) error
	StopAll() error
	AdoptInjector(verifier TokenVerifier, adopter Adopter)
}

type Options struct {
	Logger *slog.Logger
	// Now is overridable for tests; defaults to time.Now.
	Now func() time.Time
}

type server struct {
	logger   *slog.Logger
	now      func() time.Time
	verifier TokenVerifier
	adopter  Adopter

	mu        sync.Mutex
	listeners map[string]*listenerState
}

type listenerState struct {
	sessionID string
	network   string
	addr      string
	listener  net.Listener
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	current   *conn
	currentMu sync.Mutex
}

func New(opts Options) Server {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &server{
		logger:    logger,
		now:       now,
		listeners: map[string]*listenerState{},
	}
}

func (s *server) AdoptInjector(verifier TokenVerifier, adopter Adopter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.verifier = verifier
	s.adopter = adopter
}

func (s *server) Listen(sessionID, network, addr string) (string, error) {
	if network == "" {
		network = "tcp"
	}
	s.mu.Lock()
	if _, exists := s.listeners[sessionID]; exists {
		s.mu.Unlock()
		return "", fmt.Errorf("cc: already listening for session %s", sessionID)
	}
	s.mu.Unlock()

	switch network {
	case "unix":
		dir := filepath.Dir(addr)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", fmt.Errorf("cc: mkdir %s: %w", dir, err)
		}
		if err := os.Chmod(dir, 0o700); err != nil && !errors.Is(err, os.ErrNotExist) {
			s.logger.Warn("cc.dir_chmod_failed", slog.String("path", dir), slog.String("error", err.Error()))
		}
		if err := os.Remove(addr); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("cc: remove existing sock %s: %w", addr, err)
		}
	case "tcp", "tcp4", "tcp6":
		// nothing to prepare on the filesystem.
	default:
		return "", fmt.Errorf("cc: unsupported network %q", network)
	}

	ln, err := net.Listen(network, addr)
	if err != nil {
		return "", fmt.Errorf("cc: listen %s %s: %w", network, addr, err)
	}

	if network == "unix" {
		if err := os.Chmod(addr, 0o660); err != nil {
			_ = ln.Close()
			return "", fmt.Errorf("cc: chmod sock: %w", err)
		}
		// Legacy unix path: container runs as uid 1000; chown so it can
		// connect, falling back to a relaxed mode if Chown can't grant it
		// (rootless docker with userns remap, non-root agentd).
		if err := os.Chown(addr, 1000, 1000); err != nil {
			_ = os.Chmod(addr, 0o666)
		}
	}

	resolved := ln.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	st := &listenerState{
		sessionID: sessionID,
		network:   network,
		addr:      resolved,
		listener:  ln,
		cancel:    cancel,
	}
	s.mu.Lock()
	s.listeners[sessionID] = st
	s.mu.Unlock()

	st.wg.Add(1)
	go s.acceptLoop(ctx, st)
	return resolved, nil
}

func (s *server) Stop(sessionID string) error {
	s.mu.Lock()
	st := s.listeners[sessionID]
	delete(s.listeners, sessionID)
	s.mu.Unlock()
	if st == nil {
		return fmt.Errorf("cc: no listener for %s", sessionID)
	}
	return s.tearDownListener(st)
}

func (s *server) StopAll() error {
	s.mu.Lock()
	listeners := make([]*listenerState, 0, len(s.listeners))
	for _, st := range s.listeners {
		listeners = append(listeners, st)
	}
	s.listeners = map[string]*listenerState{}
	s.mu.Unlock()
	var first error
	for _, st := range listeners {
		if err := s.tearDownListener(st); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (s *server) tearDownListener(st *listenerState) error {
	st.cancel()
	_ = st.listener.Close()
	st.currentMu.Lock()
	if st.current != nil {
		_ = st.current.Close()
		st.current = nil
	}
	st.currentMu.Unlock()
	st.wg.Wait()
	if st.network == "unix" {
		_ = os.Remove(st.addr)
	}
	return nil
}

func (s *server) acceptLoop(ctx context.Context, st *listenerState) {
	defer st.wg.Done()
	for {
		raw, err := st.listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			s.logger.Warn("cc.accept_failed", slog.String("session", st.sessionID), slog.String("error", err.Error()))
			return
		}
		st.wg.Add(1)
		go s.handleConn(ctx, st, raw)
	}
}

func (s *server) handleConn(ctx context.Context, st *listenerState, raw net.Conn) {
	defer st.wg.Done()
	c := newConn(st.sessionID, raw)

	st.currentMu.Lock()
	if st.current != nil {
		st.currentMu.Unlock()
		// Reject second concurrent connection per api.md §4.4.
		_ = c.Send(Frame{
			Kind: KindAgentdError,
			Data: json.RawMessage(`{"code":"already_connected","message":"another shim is connected"}`),
		})
		_ = c.Close()
		return
	}
	st.current = c
	st.currentMu.Unlock()

	defer func() {
		st.currentMu.Lock()
		if st.current == c {
			st.current = nil
		}
		st.currentMu.Unlock()
	}()

	hello, err := c.Recv()
	if err != nil {
		s.logger.Debug("cc.hello_recv_failed", slog.String("session", st.sessionID), slog.String("error", err.Error()))
		_ = c.Close()
		return
	}
	if hello.Kind != KindHello {
		s.logger.Warn("cc.first_frame_not_hello", slog.String("session", st.sessionID), slog.String("kind", hello.Kind))
		_ = c.Close()
		return
	}
	if !s.authenticate(hello, st.sessionID) {
		s.logger.Warn("cc.auth_failed", slog.String("session", st.sessionID))
		_ = c.Send(Frame{
			Kind: KindAgentdError,
			Data: json.RawMessage(`{"code":"unauthenticated","message":"session_token mismatch"}`),
		})
		_ = c.Close()
		return
	}

	// Hand the authenticated connection (plus a synthetic-event channel for
	// throttle / fatal frames) off to the actor. The cc server's read loop
	// ferries inbound frames into the same channel; the actor drains it.
	events := make(chan Frame, 256)
	helloFwd := hello
	if s.adopter != nil {
		s.adopter.Adopt(ctx, st.sessionID, c, events)
	}
	// Re-emit the hello as the first frame on the channel so the actor sees
	// the capabilities/sdk_version even after authentication is complete.
	select {
	case events <- helloFwd:
	default:
	}
	s.readLoop(ctx, st, c, events)
	close(events)
	_ = c.Close()
}

func (s *server) authenticate(hello Frame, sessionID string) bool {
	if s.verifier == nil {
		return false
	}
	var data struct {
		SessionToken string `json:"session_token"`
	}
	if err := json.Unmarshal(hello.Data, &data); err != nil {
		return false
	}
	resolved, ok := s.verifier.Verify(data.SessionToken)
	if !ok {
		return false
	}
	return resolved == sessionID
}

func (s *server) readLoop(ctx context.Context, st *listenerState, c *conn, events chan<- Frame) {
	meter := newRateMeter(s.now)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		f, err := c.Recv()
		if err != nil {
			if !errors.Is(err, io.EOF) && !closedConnError(err) {
				s.logger.Debug("cc.read_failed", slog.String("session", st.sessionID), slog.String("error", err.Error()))
			}
			return
		}
		drop, transition := meter.observe()
		if transition != nil {
			deliverSyntheticTransition(events, *transition)
		}
		if drop && isAssistantDelta(f) {
			continue
		}
		select {
		case events <- f:
		default:
			s.logger.Debug("cc.actor_buffer_full", slog.String("session", st.sessionID))
		}
		if transition != nil && transition.Fatal {
			return
		}
	}
}

func deliverSyntheticTransition(events chan<- Frame, t ThrottleEvent) {
	if t.Fatal {
		body, _ := json.Marshal(map[string]any{
			"code":    "throttled_fatal",
			"message": "control channel sustained overflow >60s",
			"fatal":   true,
		})
		select {
		case events <- Frame{V: ProtocolVersion, Kind: KindError, TS: time.Now().UTC(), Data: body}:
		default:
		}
		return
	}
	body, _ := json.Marshal(map[string]any{"active": t.Active})
	select {
	case events <- Frame{V: ProtocolVersion, Kind: KindThrottled, TS: time.Now().UTC(), Data: body}:
	default:
	}
}

func isAssistantDelta(f Frame) bool {
	if f.Kind != KindEvent {
		return false
	}
	var event struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(f.Data, &event); err != nil {
		return false
	}
	return event.Kind == EventKindAssistantDelta
}
