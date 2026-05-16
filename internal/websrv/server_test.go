package websrv

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/agentctl/agentctl/internal/api"
	"github.com/agentctl/agentctl/internal/proto"
	"github.com/agentctl/agentctl/internal/sm"
)

type stubDocker struct{}

func (stubDocker) Info(_ context.Context) (proto.DockerHealth, error) {
	return proto.DockerHealth{OK: true, Version: "27.0.0"}, nil
}

type stubManager struct {
	listResult []proto.SessionSummary
	listErr    error
	stream     sm.Stream
	streamErr  error
	terminated []string
	created    *sm.CreateRequest
	createOut  sm.CreateResult
	createErr  error
	sentMsg    *sm.SendRequest
	// updatedModel records the (sessionID, model) tuples passed to
	// UpdateModel so PATCH tests can assert dispatch. updateModelErr is the
	// canned error to return; updateModelOut is the SessionSummary the
	// happy-path PATCH returns.
	updatedModel   []struct{ ID, Model string }
	updateModelErr error
	updateModelOut proto.SessionSummary
}

func (m *stubManager) Create(_ context.Context, req sm.CreateRequest) (sm.CreateResult, error) {
	m.created = &req
	return m.createOut, m.createErr
}
func (m *stubManager) Send(_ context.Context, req sm.SendRequest) (sm.SendResult, error) {
	m.sentMsg = &req
	return sm.SendResult{MessageID: "msg_test"}, nil
}
func (m *stubManager) Interrupt(_ context.Context, _ string, _ bool) (sm.InterruptResult, error) {
	return sm.InterruptResult{Interrupted: true}, nil
}
func (m *stubManager) Attach(_ context.Context, _ string) (sm.Stream, error) {
	if m.streamErr != nil {
		return nil, m.streamErr
	}
	return m.stream, nil
}
func (m *stubManager) List(_ context.Context) ([]proto.SessionSummary, error) {
	return m.listResult, m.listErr
}
func (m *stubManager) Get(_ context.Context, id string) (proto.SessionDetail, error) {
	if id == "missing" {
		return proto.SessionDetail{}, sm.ErrSessionNotFound
	}
	return proto.SessionDetail{SessionSummary: proto.SessionSummary{ID: id}}, nil
}
func (m *stubManager) Terminate(_ context.Context, id string) error {
	m.terminated = append(m.terminated, id)
	return nil
}

func (m *stubManager) Diff(_ context.Context, id string, _ sm.DiffRequest) (sm.DiffStream, error) {
	if id == "missing" {
		return nil, sm.ErrSessionNotFound
	}
	return newCannedDiff([]sm.DiffChunk{
		{Repo: "alpha", Data: []byte("--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n")},
		{Repo: "alpha", End: true, ExitCode: 0, BaseSHA: "abc"},
	}), nil
}

func (m *stubManager) ExportPatch(ctx context.Context, id string, req sm.DiffRequest) (sm.DiffStream, error) {
	return m.Diff(ctx, id, req)
}

func (m *stubManager) ExportPush(_ context.Context, id string, req sm.PushRequest) (sm.PushResult, error) {
	if id == "missing" {
		return sm.PushResult{}, sm.ErrSessionNotFound
	}
	return sm.PushResult{Success: true, Repo: req.Repo, Branch: req.Branch, Output: "pushed"}, nil
}

func (m *stubManager) SessionRepos(_ context.Context, id string) ([]proto.RepoState, error) {
	if id == "missing" {
		return nil, sm.ErrSessionNotFound
	}
	return []proto.RepoState{{Name: "alpha", URL: "https://example/alpha", Branch: "main"}}, nil
}

func (m *stubManager) StoredConversation(_ context.Context, id string) ([]byte, error) {
	if id == "missing" {
		return nil, nil
	}
	return []byte(`[{"type":"user","message":{"role":"user","content":"hi"}}]`), nil
}

func (m *stubManager) UpdateModel(_ context.Context, id, model string) (proto.SessionSummary, error) {
	m.updatedModel = append(m.updatedModel, struct{ ID, Model string }{ID: id, Model: model})
	if m.updateModelErr != nil {
		return proto.SessionSummary{}, m.updateModelErr
	}
	out := m.updateModelOut
	if out.ID == "" {
		out.ID = id
	}
	if out.Model == "" {
		out.Model = model
	}
	return out, nil
}

type cannedDiff struct {
	chunks []sm.DiffChunk
	idx    int
	closed bool
}

func newCannedDiff(chunks []sm.DiffChunk) *cannedDiff { return &cannedDiff{chunks: chunks} }

func (c *cannedDiff) Recv() (sm.DiffChunk, error) {
	if c.idx >= len(c.chunks) {
		return sm.DiffChunk{}, io.EOF
	}
	ch := c.chunks[c.idx]
	c.idx++
	return ch, nil
}

func (c *cannedDiff) Close() error { c.closed = true; return nil }

type fakeStream struct {
	events []proto.Event
	idx    int
	closed bool
}

func (s *fakeStream) Recv() (proto.Event, bool, string) {
	if s.idx >= len(s.events) {
		return proto.Event{}, false, "client_disconnected"
	}
	ev := s.events[s.idx]
	s.idx++
	return ev, true, ""
}
func (s *fakeStream) Close() { s.closed = true }

func startServer(t *testing.T, token string, mgr Manager) *Server {
	t.Helper()
	apiSrv := api.New(api.Options{Docker: stubDocker{}})
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	s := New(Options{Addr: addr, Token: token, API: apiSrv, Manager: mgr, Logger: logger})
	if err := s.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestHealthzNoAuth(t *testing.T) {
	s := startServer(t, "tok", nil)
	resp, err := http.Get("http://" + s.Addr() + "/healthz")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	var hr proto.HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&hr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !hr.OK {
		t.Errorf("ok = false")
	}
}

func TestRootLoaderPageContainsTokenScript(t *testing.T) {
	s := startServer(t, "tok", nil)
	resp, err := http.Get("http://" + s.Addr() + "/")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	got := string(body)
	if !strings.Contains(got, "agentctl_token") {
		t.Errorf("loader page missing cookie js: %s", got)
	}
	if !strings.Contains(got, "history.replaceState") {
		t.Errorf("loader page missing history.replaceState: %s", got)
	}
}

func TestV1RequiresBearer(t *testing.T) {
	s := startServer(t, "tok", &stubManager{})
	resp, err := http.Get("http://" + s.Addr() + "/v1/sessions")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 401 {
		t.Errorf("expected 401 unauth, got %d", resp.StatusCode)
	}
}

func TestV1AcceptsBearerHeader(t *testing.T) {
	mgr := &stubManager{listResult: []proto.SessionSummary{{ID: "sess_1"}}}
	s := startServer(t, "tok", mgr)
	req, _ := http.NewRequest("GET", "http://"+s.Addr()+"/v1/sessions", nil)
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	var body proto.ListSessionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Sessions) != 1 || body.Sessions[0].ID != "sess_1" {
		t.Errorf("unexpected body: %+v", body)
	}
}

func TestV1AcceptsBearerCookie(t *testing.T) {
	s := startServer(t, "tok", &stubManager{})
	req, _ := http.NewRequest("GET", "http://"+s.Addr()+"/v1/sessions", nil)
	req.AddCookie(&http.Cookie{Name: BearerCookieName, Value: "tok"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == 401 {
		t.Errorf("cookie auth failed")
	}
}

func TestV1WrongBearerRejected(t *testing.T) {
	s := startServer(t, "tok", &stubManager{})
	req, _ := http.NewRequest("GET", "http://"+s.Addr()+"/v1/sessions", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 401 {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestOriginEnforcedOnPOST(t *testing.T) {
	s := startServer(t, "tok", &stubManager{})
	req, _ := http.NewRequest("POST", "http://"+s.Addr()+"/v1/sessions", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Origin", "http://evil.example.com")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 403 {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

func TestOriginEnforcedOnDELETE(t *testing.T) {
	s := startServer(t, "tok", &stubManager{})
	req, _ := http.NewRequest("DELETE", "http://"+s.Addr()+"/v1/sessions/sess_1", nil)
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 403 {
		t.Errorf("expected 403 missing origin, got %d", resp.StatusCode)
	}
}

func TestOriginAcceptedOnPOST(t *testing.T) {
	mgr := &stubManager{createOut: sm.CreateResult{SessionID: "sess_new", Status: "starting"}}
	s := startServer(t, "tok", mgr)
	req, _ := http.NewRequest("POST", "http://"+s.Addr()+"/v1/sessions", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Origin", "http://"+s.Addr())
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("expected 201, got %d", resp.StatusCode)
	}
}

func TestSecFetchSiteCrossSiteRejected(t *testing.T) {
	s := startServer(t, "tok", &stubManager{})
	req, _ := http.NewRequest("POST", "http://"+s.Addr()+"/v1/sessions", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Origin", "http://"+s.Addr())
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 403 {
		t.Errorf("expected 403 cross-site, got %d", resp.StatusCode)
	}
}

func TestRefuseNonLoopback(t *testing.T) {
	apiSrv := api.New(api.Options{Docker: stubDocker{}})
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	s := New(Options{Addr: "0.0.0.0:0", Token: "tok", API: apiSrv, Logger: logger})
	if err := s.Start(); err == nil {
		t.Fatalf("expected refusal to bind 0.0.0.0")
	}
}

func TestUnavailableForUnimplemented(t *testing.T) {
	s := startServer(t, "tok", &stubManager{})
	cases := []struct {
		method, path string
	}{
		{"POST", "/v1/sessions/sess_1/restart"},
		{"GET", "/v1/usage"},
	}
	for _, c := range cases {
		req, _ := http.NewRequest(c.method, "http://"+s.Addr()+c.path, nil)
		req.Header.Set("Authorization", "Bearer tok")
		if c.method != "GET" {
			req.Header.Set("Origin", "http://"+s.Addr())
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do %s %s: %v", c.method, c.path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s %s: expected 503, got %d", c.method, c.path, resp.StatusCode)
		}
	}
}

func TestDiffEndpointReturnsPatch(t *testing.T) {
	s := startServer(t, "tok", &stubManager{})
	req, _ := http.NewRequest("GET", "http://"+s.Addr()+"/v1/sessions/sess_1/diff", nil)
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "+++ b/x") {
		t.Errorf("body: %q", string(body))
	}
}

func TestExportPatchEndpointWritesAttachment(t *testing.T) {
	s := startServer(t, "tok", &stubManager{})
	req, _ := http.NewRequest("POST", "http://"+s.Addr()+"/v1/sessions/sess_1/export/patch",
		strings.NewReader(`{"repo":"alpha"}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Origin", "http://"+s.Addr())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Content-Disposition"), "alpha.patch") {
		t.Errorf("Content-Disposition: %q", resp.Header.Get("Content-Disposition"))
	}
}

func TestExportPushReturnsJSON(t *testing.T) {
	s := startServer(t, "tok", &stubManager{})
	req, _ := http.NewRequest("POST", "http://"+s.Addr()+"/v1/sessions/sess_1/export/push",
		strings.NewReader(`{"repo":"alpha","branch":"feat/x","message":"hi"}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Origin", "http://"+s.Addr())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Success bool   `json:"success"`
		Branch  string `json:"branch"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Success || body.Branch != "feat/x" {
		t.Errorf("body=%+v", body)
	}
}

func TestSessionReposEndpoint(t *testing.T) {
	s := startServer(t, "tok", &stubManager{})
	req, _ := http.NewRequest("GET", "http://"+s.Addr()+"/v1/sessions/sess_1/repos", nil)
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Repos []proto.RepoState `json:"repos"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Repos) != 1 || body.Repos[0].Name != "alpha" {
		t.Errorf("repos=%+v", body.Repos)
	}
}

func TestSessionSnapshotEndpoint(t *testing.T) {
	s := startServer(t, "tok", &stubManager{})
	req, _ := http.NewRequest("GET", "http://"+s.Addr()+"/v1/sessions/sess_1/snapshot", nil)
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Conversation []json.RawMessage `json:"conversation"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Conversation) != 1 {
		t.Errorf("expected 1 record, got %d", len(body.Conversation))
	}
}

func TestSessionSnapshotEmpty(t *testing.T) {
	// `missing` returns nil from the stub; the handler must still respond
	// with a valid empty array so the client doesn't have to special-case
	// the not-found shape.
	s := startServer(t, "tok", &stubManager{})
	req, _ := http.NewRequest("GET", "http://"+s.Addr()+"/v1/sessions/missing/snapshot", nil)
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Conversation []json.RawMessage `json:"conversation"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Conversation) != 0 {
		t.Errorf("expected empty array, got %d records", len(body.Conversation))
	}
}

func TestMCPsUnavailableWithoutRegistry(t *testing.T) {
	s := startServer(t, "tok", &stubManager{})
	req, _ := http.NewRequest("GET", "http://"+s.Addr()+"/v1/mcps", nil)
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503 for absent registry, got %d", resp.StatusCode)
	}
}

type stubSecrets struct {
	info       GitHubTokenInfo
	updated    string
	updatedVal bool
	updateErr  error
}

func (s *stubSecrets) GetGitHub(_ context.Context) (GitHubTokenInfo, error) {
	return s.info, nil
}

func (s *stubSecrets) UpdateGitHub(_ context.Context, token string, validate bool) (GitHubTokenInfo, error) {
	if s.updateErr != nil {
		return GitHubTokenInfo{}, s.updateErr
	}
	s.updated = token
	s.updatedVal = validate
	s.info = GitHubTokenInfo{HasToken: true, Kind: "classic", Hint: token[len(token)-4:]}
	return s.info, nil
}

func startServerWithSecrets(t *testing.T, token string, sec SecretsService) *Server {
	t.Helper()
	apiSrv := api.New(api.Options{Docker: stubDocker{}})
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	s := New(Options{Addr: addr, Token: token, API: apiSrv, Manager: &stubManager{}, Secrets: sec, Logger: logger})
	if err := s.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestGitHubTokenGetReportsPresence(t *testing.T) {
	sec := &stubSecrets{info: GitHubTokenInfo{HasToken: true, Kind: "fine-grained", Hint: "abcd"}}
	s := startServerWithSecrets(t, "tok", sec)
	req, _ := http.NewRequest("GET", "http://"+s.Addr()+"/v1/secrets/github", nil)
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body GitHubTokenInfo
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.HasToken || body.Kind != "fine-grained" || body.Hint != "abcd" {
		t.Errorf("unexpected body: %+v", body)
	}
}

func TestGitHubTokenUpdate(t *testing.T) {
	sec := &stubSecrets{}
	s := startServerWithSecrets(t, "tok", sec)
	req, _ := http.NewRequest("PUT",
		"http://"+s.Addr()+"/v1/secrets/github",
		strings.NewReader(`{"token":"ghp_abcdefghij","skip_validate":true}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Origin", "http://"+s.Addr())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if sec.updated != "ghp_abcdefghij" {
		t.Errorf("token not stored, got %q", sec.updated)
	}
	if sec.updatedVal {
		t.Errorf("expected validate=false when skip_validate=true")
	}
}

func TestGitHubTokenUpdateRejectsEmpty(t *testing.T) {
	sec := &stubSecrets{}
	s := startServerWithSecrets(t, "tok", sec)
	req, _ := http.NewRequest("PUT",
		"http://"+s.Addr()+"/v1/secrets/github",
		strings.NewReader(`{"token":""}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Origin", "http://"+s.Addr())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for empty token, got %d", resp.StatusCode)
	}
}

func TestGitHubTokenUnavailableWithoutService(t *testing.T) {
	s := startServer(t, "tok", &stubManager{})
	req, _ := http.NewRequest("GET", "http://"+s.Addr()+"/v1/secrets/github", nil)
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", resp.StatusCode)
	}
}

func TestSendMessageViaWeb(t *testing.T) {
	mgr := &stubManager{}
	s := startServer(t, "tok", mgr)
	req, _ := http.NewRequest("POST",
		"http://"+s.Addr()+"/v1/sessions/sess_1/messages",
		strings.NewReader(`{"content":"hi"}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Origin", "http://"+s.Addr())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if mgr.sentMsg == nil || mgr.sentMsg.SessionID != "sess_1" || mgr.sentMsg.Content != "hi" {
		t.Errorf("manager.Send called with wrong args: %+v", mgr.sentMsg)
	}
}

func TestWebSocketRejectsBadOrigin(t *testing.T) {
	s := startServer(t, "tok", &stubManager{})
	wsURL := "ws://" + s.Addr() + "/v1/sessions/sess_1/stream"
	dialer := websocket.Dialer{
		Subprotocols: []string{WSSubprotocol},
	}
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer tok")
	hdr.Set("Origin", "http://evil.example.com")
	conn, _, err := dialer.Dial(wsURL, hdr)
	if err == nil {
		_ = conn.Close()
		t.Fatalf("expected dial to fail with bad origin")
	}
}

func TestWebSocketRejectsMissingSubprotocol(t *testing.T) {
	s := startServer(t, "tok", &stubManager{})
	dialer := websocket.Dialer{}
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer tok")
	hdr.Set("Origin", "http://"+s.Addr())
	u, _ := url.Parse("ws://" + s.Addr() + "/v1/sessions/sess_1/stream")
	_, resp, err := dialer.Dial(u.String(), hdr)
	if err == nil {
		t.Fatalf("expected dial failure without subprotocol")
	}
	if resp == nil || resp.StatusCode != http.StatusBadRequest {
		got := -1
		if resp != nil {
			got = resp.StatusCode
		}
		t.Errorf("expected 400, got %d (err=%v)", got, err)
	}
}

func TestWebSocketForwardsEvents(t *testing.T) {
	stream := &fakeStream{events: []proto.Event{
		{EventID: "ev_1", Kind: proto.EventSessionSnapshot, SessionID: "sess_1", TS: time.Now().UTC(), Data: json.RawMessage(`{"hello":"world"}`)},
		{EventID: "ev_2", Kind: proto.EventAssistantMessage, SessionID: "sess_1", TS: time.Now().UTC(), Data: json.RawMessage(`{"content":"hi"}`)},
	}}
	mgr := &stubManager{stream: stream}
	s := startServer(t, "tok", mgr)

	dialer := websocket.Dialer{Subprotocols: []string{WSSubprotocol}}
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer tok")
	hdr.Set("Origin", "http://"+s.Addr())
	conn, _, err := dialer.Dial("ws://"+s.Addr()+"/v1/sessions/sess_1/stream", hdr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if conn.Subprotocol() != WSSubprotocol {
		t.Errorf("subprotocol = %q want %q", conn.Subprotocol(), WSSubprotocol)
	}

	deadline := time.Now().Add(2 * time.Second)
	for i, want := range []string{"ev_1", "ev_2"} {
		_ = conn.SetReadDeadline(deadline)
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read[%d]: %v", i, err)
		}
		var f proto.Frame
		if err := json.Unmarshal(msg, &f); err != nil {
			t.Fatalf("unmarshal frame: %v", err)
		}
		if f.Kind != proto.KindEvent {
			t.Errorf("kind = %s want event", f.Kind)
		}
		if f.ID != want {
			t.Errorf("id = %q want %q", f.ID, want)
		}
	}
	_ = conn.SetReadDeadline(deadline)
	_, msg, err := conn.ReadMessage()
	if err != nil {
		var cerr *websocket.CloseError
		if errors.As(err, &cerr) {
			return
		}
		t.Fatalf("expected stream_end frame, got err=%v", err)
	}
	var f proto.Frame
	_ = json.Unmarshal(msg, &f)
	if f.Kind != proto.KindStreamEnd {
		t.Errorf("expected stream_end, got %s", f.Kind)
	}
}
