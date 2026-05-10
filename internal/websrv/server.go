package websrv

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/agentctl/agentctl/internal/api"
)

const (
	BearerCookieName = "agentctl_token"
	WSSubprotocol    = "agentctl.v1"
)

type Server struct {
	httpSrv  *http.Server
	listener net.Listener
	logger   *slog.Logger
	apiSrv   *api.Server
	token    string
	addr     string
}

type Options struct {
	Addr   string
	Token  string
	API    *api.Server
	Logger *slog.Logger
}

func New(opts Options) *Server {
	mux := http.NewServeMux()
	s := &Server{
		logger: opts.Logger,
		apiSrv: opts.API,
		token:  opts.Token,
		addr:   opts.Addr,
	}
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/", s.handleRoot)
	mux.Handle("/v1/", s.authMiddleware(s.originMiddleware(http.HandlerFunc(s.handleV1))))

	s.httpSrv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return s
}

func (s *Server) Start() error {
	addr := s.addr
	if !isLoopbackAddr(addr) {
		return fmt.Errorf("web addr %q is not loopback (must bind 127.0.0.1 or ::1)", addr)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	s.listener = ln
	go func() {
		if err := s.httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error("web.serve_failed", slog.String("error", err.Error()))
		}
	}()
	return nil
}

func (s *Server) Addr() string {
	if s.listener == nil {
		return s.addr
	}
	return s.listener.Addr().String()
}

func (s *Server) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.httpSrv.Shutdown(ctx)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	hr := s.apiSrv.Health(r.Context())
	w.Header().Set("Content-Type", "application/json")
	if !hr.OK {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(hr)
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(loaderHTML))
}

func (s *Server) handleV1(w http.ResponseWriter, r *http.Request) {
	http.Error(w, `{"code":"not_found","message":"endpoint reserved for M3"}`, http.StatusNotFound)
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := extractBearerToken(r)
		if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(s.token)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="agentctl"`)
			http.Error(w, `{"code":"unauthenticated","message":"missing or invalid bearer token"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) originMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		expected := "http://" + s.addr
		if origin := r.Header.Get("Origin"); origin != expected {
			http.Error(w, `{"code":"forbidden","message":"origin mismatch"}`, http.StatusForbidden)
			return
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
			http.Error(w, `{"code":"forbidden","message":"cross-site request"}`, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func extractBearerToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if strings.HasPrefix(h, "Bearer ") {
			return strings.TrimPrefix(h, "Bearer ")
		}
	}
	if c, err := r.Cookie(BearerCookieName); err == nil {
		return c.Value
	}
	return ""
}

func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "127.0.0.1" || host == "::1" || host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

const loaderHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>agentctl Web UI</title>
<style>
  body { font-family: system-ui, -apple-system, sans-serif; margin: 4rem auto; max-width: 36rem; color: #222; }
  code { background: #f0f0f0; padding: 0.1rem 0.3rem; border-radius: 3px; }
  .placeholder { color: #777; font-style: italic; }
</style>
</head>
<body>
<h1>agentctl Web UI</h1>
<p class="placeholder">M3 milestone &mdash; SPA not yet shipped.</p>
<p>The auth handshake is wired: open this page via <code>agentctl ui</code> so the bearer token in the URL fragment can be persisted as a cookie.</p>
<pre id="status">initializing&hellip;</pre>
<script>
(function () {
  var status = document.getElementById("status");
  var hash = window.location.hash || "";
  var token = "";
  if (hash.indexOf("#t=") === 0) {
    token = decodeURIComponent(hash.substring(3));
  } else if (hash.indexOf("#token=") === 0) {
    token = decodeURIComponent(hash.substring(7));
  }
  if (token) {
    document.cookie = "agentctl_token=" + token + "; Path=/; SameSite=Strict";
    history.replaceState(null, "", window.location.pathname);
    status.textContent = "auth cookie set; the SPA will load here in M3.";
  } else {
    status.textContent = "no token in URL fragment; run \"agentctl ui\" from a terminal.";
  }
})();
</script>
</body>
</html>
`
