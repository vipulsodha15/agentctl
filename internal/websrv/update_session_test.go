package websrv

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/agentctl/agentctl/internal/api"
	"github.com/agentctl/agentctl/internal/proto"
	"github.com/agentctl/agentctl/internal/sm"
)

// patchSession is a tiny helper for PATCH /v1/sessions/<id> requests. The
// existing server tests roll their own http.NewRequest for each case; the
// PATCH-specific shape is repetitive enough that a one-call wrapper makes
// the assertions read as straight cases.
func patchSession(t *testing.T, s *Server, id, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPatch,
		"http://"+s.Addr()+"/v1/sessions/"+id, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Origin", "http://"+s.Addr())
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("patch %s: %v", id, err)
	}
	return resp
}

func TestPatchSessionUpdatesModel(t *testing.T) {
	mgr := &stubManager{updateModelOut: proto.SessionSummary{ID: "sess_1", Model: "claude-opus-4-7"}}
	s := startServer(t, "tok", mgr)
	resp := patchSession(t, s, "sess_1", `{"model":"claude-opus-4-7"}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	if len(mgr.updatedModel) != 1 {
		t.Fatalf("UpdateModel called %d times, want 1", len(mgr.updatedModel))
	}
	if got := mgr.updatedModel[0]; got.ID != "sess_1" || got.Model != "claude-opus-4-7" {
		t.Errorf("UpdateModel got %+v", got)
	}
	var out proto.GetSessionResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Session.Model != "claude-opus-4-7" {
		t.Errorf("response.session.model = %q want claude-opus-4-7", out.Session.Model)
	}
}

func TestPatchSessionRejectsProviderField(t *testing.T) {
	mgr := &stubManager{}
	s := startServer(t, "tok", mgr)
	resp := patchSession(t, s, "sess_1", `{"provider":"openai","model":"gpt-5.5"}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	if len(mgr.updatedModel) != 0 {
		t.Errorf("UpdateModel should not have been called when provider rejected: %+v", mgr.updatedModel)
	}
}

func TestPatchSessionRejectsCrossProviderModel(t *testing.T) {
	mgr := &stubManager{updateModelErr: sm.ErrModelInvalid}
	s := startServer(t, "tok", mgr)
	resp := patchSession(t, s, "sess_1", `{"model":"gpt-not-claude"}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
}

func TestPatchSessionRejectsEmptyBody(t *testing.T) {
	mgr := &stubManager{}
	s := startServer(t, "tok", mgr)
	resp := patchSession(t, s, "sess_1", `{}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 for no mutable fields", resp.StatusCode)
	}
}

func TestPatchSessionRejectsUnknownField(t *testing.T) {
	mgr := &stubManager{}
	s := startServer(t, "tok", mgr)
	resp := patchSession(t, s, "sess_1", `{"name":"new name"}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 for unknown PATCH field", resp.StatusCode)
	}
}

// stubProviderService gives us deterministic /v1/providers content without
// pulling internal/config into the test.
type stubProviderService struct {
	entries map[string]ProviderEntry
}

func (s stubProviderService) List(_ context.Context) (map[string]ProviderEntry, error) {
	return s.entries, nil
}

func TestGetProvidersReturnsCatalog(t *testing.T) {
	provs := stubProviderService{entries: map[string]ProviderEntry{
		"anthropic": {Enabled: true, DefaultModel: "claude-sonnet-4-6", Models: []string{"claude-sonnet-4-6", "claude-opus-4-7"}},
	}}
	s := startServerWithProviders(t, "tok", &stubManager{}, provs)
	req, _ := http.NewRequest(http.MethodGet, "http://"+s.Addr()+"/v1/providers", nil)
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out map[string]ProviderEntry
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got, ok := out["anthropic"]; !ok || len(got.Models) != 2 {
		t.Errorf("expected anthropic with 2 models, got %+v", out)
	}
}

// startServerWithProviders mirrors startServer but threads a ProviderService.
// Lives in this file so the providers-only test isn't tangled into the
// shared scaffolding for the rest of the suite.
func startServerWithProviders(t *testing.T, token string, mgr Manager, provs ProviderService) *Server {
	t.Helper()
	apiSrv := api.New(api.Options{Docker: stubDocker{}})
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	s := New(Options{Addr: addr, Token: token, API: apiSrv, Manager: mgr, Providers: provs, Logger: logger})
	if err := s.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
