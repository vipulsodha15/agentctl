package cc

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type stubVerifier struct {
	token string
	id    string
}

func (s stubVerifier) Verify(token string) (string, bool) {
	if token == s.token {
		return s.id, true
	}
	return "", false
}

type captureAdopter struct {
	mu        sync.Mutex
	sessions  []string
	conns     []Conn
	eventChs  []<-chan Frame
	collected [][]Frame
}

func (a *captureAdopter) Adopt(_ context.Context, sessionID string, conn Conn, events <-chan Frame) {
	a.mu.Lock()
	idx := len(a.sessions)
	a.sessions = append(a.sessions, sessionID)
	a.conns = append(a.conns, conn)
	a.eventChs = append(a.eventChs, events)
	a.collected = append(a.collected, nil)
	a.mu.Unlock()

	go func() {
		for f := range events {
			a.mu.Lock()
			a.collected[idx] = append(a.collected[idx], f)
			a.mu.Unlock()
		}
	}()
}

func (a *captureAdopter) framesFor(idx int) []Frame {
	a.mu.Lock()
	defer a.mu.Unlock()
	if idx >= len(a.collected) {
		return nil
	}
	out := make([]Frame, len(a.collected[idx]))
	copy(out, a.collected[idx])
	return out
}

func (a *captureAdopter) sessionCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.sessions)
}

func dialUnix(t *testing.T, path string) net.Conn {
	t.Helper()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return conn
}

func sendFrame(t *testing.T, c net.Conn, f Frame) {
	t.Helper()
	if err := NewFrameWriter(c).Write(f); err != nil {
		t.Fatalf("send: %v", err)
	}
}

func recvFrame(t *testing.T, c net.Conn) Frame {
	t.Helper()
	f, err := NewFrameReader(c).Read()
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	return f
}

func startServer(t *testing.T) (Server, *captureAdopter, string, string) {
	t.Helper()
	dir := t.TempDir()
	sock := filepath.Join(dir, "agentd.sock")
	srv := New(Options{})
	adopter := &captureAdopter{}
	srv.AdoptInjector(stubVerifier{token: "good-token", id: "sess-1"}, adopter)
	if _, err := srv.Listen("sess-1", "unix", sock); err != nil {
		t.Fatalf("listen: %v", err)
	}
	return srv, adopter, sock, dir
}

func TestHandshakeAcceptsValidToken(t *testing.T) {
	srv, adopter, sock, _ := startServer(t)
	defer func() { _ = srv.StopAll() }()

	c := dialUnix(t, sock)
	defer func() { _ = c.Close() }()

	body, _ := json.Marshal(map[string]any{
		"session_token": "good-token",
		"shim_version":  "1.0.0",
		"sdk_version":   "0.1.80",
		"pid":           1234,
		"capabilities":  []string{"runtime.hello"},
	})
	sendFrame(t, c, Frame{Kind: KindHello, Data: body})

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got := adopter.framesFor(0); len(got) >= 1 {
			if got[0].Kind != KindHello {
				t.Errorf("first frame to actor: got %s want %s", got[0].Kind, KindHello)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("actor never received hello frame")
}

func TestHandshakeRejectsBadToken(t *testing.T) {
	srv, adopter, sock, _ := startServer(t)
	defer func() { _ = srv.StopAll() }()

	c := dialUnix(t, sock)
	defer func() { _ = c.Close() }()

	body, _ := json.Marshal(map[string]any{"session_token": "wrong"})
	sendFrame(t, c, Frame{Kind: KindHello, Data: body})

	resp := recvFrame(t, c)
	if resp.Kind != KindAgentdError {
		t.Errorf("expected %s, got %s", KindAgentdError, resp.Kind)
	}
	// Connection should be closed; subsequent read returns EOF.
	if _, err := NewFrameReader(c).Read(); err == nil {
		t.Errorf("expected EOF after token rejection")
	}
	if adopter.sessionCount() != 0 {
		t.Errorf("actor must not be adopted on rejected shim")
	}
}

func TestHandshakeRejectsNonHelloFirstFrame(t *testing.T) {
	srv, _, sock, _ := startServer(t)
	defer func() { _ = srv.StopAll() }()

	c := dialUnix(t, sock)
	defer func() { _ = c.Close() }()
	sendFrame(t, c, Frame{Kind: KindHeartbeat, Data: json.RawMessage(`{}`)})
	if _, err := NewFrameReader(c).Read(); err == nil {
		t.Errorf("expected EOF after non-hello first frame")
	}
}

func TestSecondConnectionRejected(t *testing.T) {
	srv, _, sock, _ := startServer(t)
	defer func() { _ = srv.StopAll() }()

	first := dialUnix(t, sock)
	defer func() { _ = first.Close() }()
	body, _ := json.Marshal(map[string]any{"session_token": "good-token"})
	sendFrame(t, first, Frame{Kind: KindHello, Data: body})

	// Wait for the first connection to be authenticated.
	time.Sleep(50 * time.Millisecond)

	second := dialUnix(t, sock)
	defer func() { _ = second.Close() }()
	resp := recvFrame(t, second)
	if resp.Kind != KindAgentdError {
		t.Errorf("expected already_connected error, got %s", resp.Kind)
	}
	var ed struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(resp.Data, &ed)
	if ed.Code != "already_connected" {
		t.Errorf("error code: got %s want already_connected", ed.Code)
	}
}

func TestOversizeFrameClosesConnection(t *testing.T) {
	srv, _, sock, _ := startServer(t)
	defer func() { _ = srv.StopAll() }()

	c := dialUnix(t, sock)
	defer func() { _ = c.Close() }()
	// Write something larger than 1 MiB without using FrameWriter.
	junk := make([]byte, MaxFrameBytes+10)
	for i := range junk {
		junk[i] = 'x'
	}
	junk[len(junk)-1] = '\n'
	if _, err := c.Write(junk); err != nil {
		t.Fatalf("write: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	buf := make([]byte, 1)
	_ = c.SetReadDeadline(deadline)
	if _, err := c.Read(buf); err == nil {
		t.Errorf("expected close after oversize frame")
	}
}

func TestStopClosesActiveConnection(t *testing.T) {
	srv, _, sock, _ := startServer(t)
	c := dialUnix(t, sock)
	body, _ := json.Marshal(map[string]any{"session_token": "good-token"})
	sendFrame(t, c, Frame{Kind: KindHello, Data: body})
	time.Sleep(50 * time.Millisecond)
	if err := srv.Stop("sess-1"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := NewFrameReader(c).Read(); err == nil {
		t.Errorf("expected close after Stop")
	}
}

// TestListenTCPAndHandshake exercises the production path: agentd listens on
// TCP 127.0.0.1:0, the shim dials the resolved host:port and completes the
// runtime.hello / agentd adoption handshake. This is the wire the fix for
// "EOPNOTSUPP on bind-mounted unix socket inside Docker Desktop" runs on.
func TestListenTCPAndHandshake(t *testing.T) {
	srv := New(Options{})
	defer func() { _ = srv.StopAll() }()
	adopter := &captureAdopter{}
	srv.AdoptInjector(stubVerifier{token: "good-token", id: "sess-tcp"}, adopter)

	addr, err := srv.Listen("sess-tcp", "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	if addr == "" {
		t.Fatalf("Listen returned empty addr")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split host:port %q: %v", addr, err)
	}
	if host != "127.0.0.1" || port == "0" || port == "" {
		t.Fatalf("unexpected resolved addr: host=%q port=%q (raw=%q)", host, port, addr)
	}

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial tcp %s: %v", addr, err)
	}
	defer func() { _ = c.Close() }()

	body, _ := json.Marshal(map[string]any{
		"session_token": "good-token",
		"shim_version":  "1.0.0",
		"sdk_version":   "0.1.80",
	})
	sendFrame(t, c, Frame{Kind: KindHello, Data: body})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := adopter.framesFor(0); len(got) >= 1 {
			if got[0].Kind != KindHello {
				t.Errorf("first frame to actor over TCP: got %s want %s", got[0].Kind, KindHello)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("actor never received hello over TCP")
}
