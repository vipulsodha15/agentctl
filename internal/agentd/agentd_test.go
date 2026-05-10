package agentd

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
)

func TestProbeExistingAgentdMatchesOurShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":          true,
			"version":     "0.1.0",
			"build":       "dev",
			"reconciling": false,
			"docker":      map[string]any{"ok": true, "version": "29.0"},
			"uptime_s":    42,
		})
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	h, ok := probeExistingAgentd(addr)
	if !ok {
		t.Fatalf("expected probe to detect existing agentd")
	}
	if h.Version != "0.1.0" || h.UptimeS != 42 {
		t.Errorf("unexpected health payload: %+v", h)
	}
}

func TestProbeExistingAgentdIgnoresUnrelatedServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello from some other process"))
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	if _, ok := probeExistingAgentd(addr); ok {
		t.Errorf("probe matched a non-agentctl server")
	}
}

func TestProbeExistingAgentdFalseWhenNothingListens(t *testing.T) {
	// Bind a port and immediately release it so we know nothing is on it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	if _, ok := probeExistingAgentd(addr); ok {
		t.Errorf("probe matched a closed port")
	}
}

func TestIsAddrInUseRecognizesEADDRINUSE(t *testing.T) {
	if !isAddrInUse(&net.OpError{Op: "listen", Err: syscall.EADDRINUSE}) {
		t.Errorf("expected EADDRINUSE to be recognized")
	}
	if !isAddrInUse(errors.New("listen tcp 127.0.0.1:7777: bind: address already in use")) {
		t.Errorf("expected text-only address-in-use error to be recognized")
	}
	if isAddrInUse(errors.New("some unrelated failure")) {
		t.Errorf("unrelated error should not match")
	}
}
