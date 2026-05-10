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
)

type stubDocker struct{}

func (stubDocker) Info(_ context.Context) (proto.DockerHealth, error) {
	return proto.DockerHealth{OK: true, Version: "27.0.0"}, nil
}

func startServer(t *testing.T, token string) *Server {
	t.Helper()
	apiSrv := api.New(api.Options{Docker: stubDocker{}})
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	s := New(Options{Addr: addr, Token: token, API: apiSrv, Logger: logger})
	if err := s.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestHealthzNoAuth(t *testing.T) {
	s := startServer(t, "tok")
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

func TestRootLoaderPage(t *testing.T) {
	s := startServer(t, "tok")
	resp, err := http.Get("http://" + s.Addr() + "/")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	got := string(body)
	if !strings.Contains(got, "agentctl_token") {
		t.Errorf("loader page missing cookie js")
	}
	if !strings.Contains(got, "M3 milestone") {
		t.Errorf("loader page missing milestone placeholder")
	}
}

func TestV1RequiresBearer(t *testing.T) {
	s := startServer(t, "tok")
	resp, err := http.Get("http://" + s.Addr() + "/v1/sessions")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 401 {
		t.Errorf("expected 401 unauth, got %d", resp.StatusCode)
	}
}

func TestV1AcceptsBearer(t *testing.T) {
	s := startServer(t, "tok")
	req, _ := http.NewRequest("GET", "http://"+s.Addr()+"/v1/sessions", nil)
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == 401 {
		t.Errorf("got 401 with valid token")
	}
}

func TestV1AcceptsCookie(t *testing.T) {
	s := startServer(t, "tok")
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

func TestOriginEnforcedOnPOST(t *testing.T) {
	s := startServer(t, "tok")
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

func TestOriginAcceptedOnPOST(t *testing.T) {
	s := startServer(t, "tok")
	req, _ := http.NewRequest("POST", "http://"+s.Addr()+"/v1/sessions", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Origin", "http://"+s.Addr())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == 403 {
		t.Errorf("matching origin should not be 403")
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
