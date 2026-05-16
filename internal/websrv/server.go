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
	"github.com/agentctl/agentctl/internal/proto"
	"github.com/agentctl/agentctl/internal/sm"
	"github.com/agentctl/agentctl/web"
)

const (
	BearerCookieName = "agentctl_token"
	WSSubprotocol    = "agentctl.v1"
)

// ProviderResolver resolves (provider, model) for a session-create. Wired
// by agentd to a closure over secrets + config + workspace state per ADR
// 0020 §3. When nil, request.Provider / request.Model pass through unchanged
// (and sm.Create rejects an empty Provider).
type ProviderResolver func(cliProvider, cliModel string) (provider, model string, err error)

// ProviderCatalog answers GET /api/providers. Returns the catalog the web
// SPA filters its session-/agent-create dropdowns on. Sourced from
// secrets.EnabledProviders + config.toml [model] / [pricing.tables.models]
// (single source of truth — no parallel catalog file, ADR 0020 §UX
// principles).
type ProviderCatalog interface {
	Catalog() ProvidersResponse
}

// ProvidersResponse is the body of GET /api/providers. Shape per ADR 0020 §9.
type ProvidersResponse map[string]ProviderInfo

type ProviderInfo struct {
	Enabled      bool     `json:"enabled"`
	DefaultModel string   `json:"default_model"`
	Models       []string `json:"models"`
}

type Server struct {
	httpSrv   *http.Server
	listener  net.Listener
	logger    *slog.Logger
	apiSrv    *api.Server
	manager   Manager
	mcps      MCPRegistry
	skills    SkillsService
	usage     UsageService
	logs      LogStreamer
	doctor    Doctor
	updater   Updater
	library   LibraryService
	tasks     TaskService
	taskHub   TaskHub
	secrets   SecretsService
	resolve   ProviderResolver
	providers ProviderCatalog
	token     string
	addr      string
	originOK  string
}

type Options struct {
	Addr             string
	Token            string
	API              *api.Server
	Manager          Manager
	MCPs             MCPRegistry
	Skills           SkillsService
	Usage            UsageService
	Logs             LogStreamer
	Doctor           Doctor
	Updater          Updater
	Library          LibraryService
	Tasks            TaskService
	TaskHub          TaskHub
	Secrets          SecretsService
	ProviderResolver ProviderResolver
	Providers        ProviderCatalog
	Logger           *slog.Logger
}

func New(opts Options) *Server {
	s := &Server{
		logger:    opts.Logger,
		apiSrv:    opts.API,
		manager:   opts.Manager,
		mcps:      opts.MCPs,
		skills:    opts.Skills,
		usage:     opts.Usage,
		logs:      opts.Logs,
		doctor:    opts.Doctor,
		updater:   opts.Updater,
		library:   opts.Library,
		tasks:     opts.Tasks,
		taskHub:   opts.TaskHub,
		secrets:   opts.Secrets,
		resolve:   opts.ProviderResolver,
		providers: opts.Providers,
		token:     opts.Token,
		addr:      opts.Addr,
		originOK:  "http://" + opts.Addr,
	}
	if s.logger == nil {
		s.logger = slog.Default()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/", s.handleRoot)
	mux.Handle("/v1/", s.authMiddleware(http.HandlerFunc(s.routeV1)))

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
	s.originOK = "http://" + ln.Addr().String()
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

// routeV1 dispatches every /v1/* request after the auth middleware. Method
// + path matching is hand-rolled (rather than gorilla/mux) to keep the
// dependency set minimal — the route count is small and stable.
func (s *Server) routeV1(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	method := r.Method
	switch {
	case path == "/v1/sessions":
		switch method {
		case http.MethodGet:
			s.handleListSessions(w, r)
		case http.MethodPost:
			s.requireOrigin(w, r, s.handleCreateSession)
		default:
			methodNotAllowed(w)
		}
		return
	case path == "/v1/mcps":
		switch method {
		case http.MethodGet:
			s.handleListMCPs(w, r)
		case http.MethodPost:
			s.requireOrigin(w, r, s.handleAddMCP)
		default:
			methodNotAllowed(w)
		}
		return
	case path == "/v1/skills":
		switch method {
		case http.MethodGet:
			s.handleListInstalledSkills(w, r)
		case http.MethodPost:
			s.requireOrigin(w, r, s.handleAddSkill)
		default:
			methodNotAllowed(w)
		}
		return
	case path == "/v1/skills/import":
		if method == http.MethodPost {
			s.requireOrigin(w, r, s.handleImportSkill)
			return
		}
		methodNotAllowed(w)
		return
	case path == "/v1/usage":
		if method == http.MethodGet {
			s.handleGetUsage(w, r)
			return
		}
		methodNotAllowed(w)
		return
	case path == "/v1/doctor":
		if method == http.MethodPost {
			s.requireOrigin(w, r, s.handleDoctor)
			return
		}
		methodNotAllowed(w)
		return
	case path == "/v1/update":
		if method == http.MethodPost {
			s.requireOrigin(w, r, s.handleUpdate)
			return
		}
		methodNotAllowed(w)
		return
	case path == "/v1/secrets/github":
		switch method {
		case http.MethodGet:
			s.handleGetGitHubToken(w, r)
		case http.MethodPut:
			s.requireOrigin(w, r, s.handleUpdateGitHubToken)
		default:
			methodNotAllowed(w)
		}
		return
	case path == "/v1/agents":
		switch method {
		case http.MethodGet:
			s.handleListAgents(w, r)
		case http.MethodPost:
			s.requireOrigin(w, r, s.handleAddAgent)
		default:
			methodNotAllowed(w)
		}
		return
	case path == "/v1/providers":
		if method == http.MethodGet {
			s.handleListProviders(w, r)
			return
		}
		methodNotAllowed(w)
		return
	case path == "/v1/assembly-lines":
		switch method {
		case http.MethodGet:
			s.handleListAssemblyLines(w, r)
		case http.MethodPost:
			s.requireOrigin(w, r, s.handleAddAssemblyLine)
		default:
			methodNotAllowed(w)
		}
		return
	case path == "/v1/tasks":
		switch method {
		case http.MethodGet:
			s.handleListTasks(w, r)
		case http.MethodPost:
			s.requireOrigin(w, r, s.handleCreateTask)
		default:
			methodNotAllowed(w)
		}
		return
	case path == "/v1/providers":
		if method == http.MethodGet {
			s.handleListProviders(w, r)
			return
		}
		methodNotAllowed(w)
		return
	}

	if name, ok := matchPrefix(path, "/v1/agents/"); ok && !strings.Contains(name, "/") {
		switch method {
		case http.MethodGet:
			s.handleGetAgent(w, r, name)
		case http.MethodPut:
			s.requireOrigin(w, r, func(w http.ResponseWriter, r *http.Request) {
				s.handlePutAgent(w, r, name)
			})
		case http.MethodDelete:
			s.requireOrigin(w, r, func(w http.ResponseWriter, r *http.Request) {
				s.handleRemoveAgent(w, r, name)
			})
		default:
			methodNotAllowed(w)
		}
		return
	}

	if name, ok := matchPrefix(path, "/v1/assembly-lines/"); ok && !strings.Contains(name, "/") {
		switch method {
		case http.MethodGet:
			s.handleGetAssemblyLine(w, r, name)
		case http.MethodPut:
			s.requireOrigin(w, r, func(w http.ResponseWriter, r *http.Request) {
				s.handlePutAssemblyLine(w, r, name)
			})
		case http.MethodDelete:
			s.requireOrigin(w, r, func(w http.ResponseWriter, r *http.Request) {
				s.handleRemoveAssemblyLine(w, r, name)
			})
		default:
			methodNotAllowed(w)
		}
		return
	}

	if rest, ok := matchPrefix(path, "/v1/tasks/"); ok {
		s.routeTaskItem(w, r, rest)
		return
	}

	if name, ok := matchPrefix(path, "/v1/mcps/"); ok && !strings.Contains(name, "/") {
		switch method {
		case http.MethodPatch:
			s.requireOrigin(w, r, func(w http.ResponseWriter, r *http.Request) {
				s.handleUpdateMCP(w, r, name)
			})
		case http.MethodDelete:
			s.requireOrigin(w, r, func(w http.ResponseWriter, r *http.Request) {
				s.handleRemoveMCP(w, r, name)
			})
		default:
			methodNotAllowed(w)
		}
		return
	}

	if rest, ok := matchPrefix(path, "/v1/skills/"); ok {
		s.routeSkillsItem(w, r, rest)
		return
	}

	if rest, ok := matchPrefix(path, "/v1/sessions/"); ok {
		s.routeSessionItem(w, r, rest)
		return
	}

	writeError(w, http.StatusNotFound, proto.ErrNotFound, "no such endpoint")
}

func (s *Server) routeSkillsItem(w http.ResponseWriter, r *http.Request, rest string) {
	method := r.Method
	if name, suffix, hasSuffix := splitOne(rest); hasSuffix {
		switch suffix {
		case "validate":
			if method == http.MethodPost {
				s.requireOrigin(w, r, func(w http.ResponseWriter, r *http.Request) {
					s.handleValidateSkill(w, r, name)
				})
				return
			}
		case "export":
			if method == http.MethodGet {
				s.handleExportSkill(w, r, name)
				return
			}
		}
		methodNotAllowed(w)
		return
	}
	name := rest
	switch method {
	case http.MethodDelete:
		s.requireOrigin(w, r, func(w http.ResponseWriter, r *http.Request) {
			s.handleRemoveSkill(w, r, name)
		})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) routeSessionItem(w http.ResponseWriter, r *http.Request, rest string) {
	method := r.Method
	id, suffix, hasSuffix := splitOne(rest)
	if !hasSuffix {
		switch method {
		case http.MethodGet:
			s.handleGetSession(w, r, id)
		case http.MethodPatch:
			// ADR 0020 §2 / §4 — mid-session model switch lives here.
			s.requireOrigin(w, r, func(w http.ResponseWriter, r *http.Request) {
				s.handleUpdateSession(w, r, id)
			})
		case http.MethodDelete:
			s.requireOrigin(w, r, func(w http.ResponseWriter, r *http.Request) {
				s.handleTerminateSession(w, r, id)
			})
		default:
			methodNotAllowed(w)
		}
		return
	}
	switch {
	case suffix == "messages" && method == http.MethodPost:
		s.requireOrigin(w, r, func(w http.ResponseWriter, r *http.Request) {
			s.handleSendMessage(w, r, id)
		})
	case suffix == "interrupt" && method == http.MethodPost:
		s.requireOrigin(w, r, func(w http.ResponseWriter, r *http.Request) {
			s.handleInterrupt(w, r, id)
		})
	case suffix == "restart" && method == http.MethodPost:
		s.requireOrigin(w, r, func(w http.ResponseWriter, r *http.Request) {
			unavailable(w, "RestartSession", "M4")
		})
	case suffix == "diff" && method == http.MethodGet:
		s.handleDiff(w, r, id)
	case suffix == "export/patch" && method == http.MethodPost:
		s.requireOrigin(w, r, func(w http.ResponseWriter, r *http.Request) {
			s.handleExportPatch(w, r, id)
		})
	case suffix == "export/push" && method == http.MethodPost:
		s.requireOrigin(w, r, func(w http.ResponseWriter, r *http.Request) {
			s.handleExportPush(w, r, id)
		})
	case suffix == "repos" && method == http.MethodGet:
		s.handleListSessionRepos(w, r, id)
	case suffix == "logs" && method == http.MethodGet:
		s.handleSessionLogs(w, r, id)
	case suffix == "skills" && method == http.MethodGet:
		s.handleSessionSkills(w, r, id)
	case suffix == "stream" && method == http.MethodGet:
		s.handleAttachStream(w, r, id)
	case suffix == "snapshot" && method == http.MethodGet:
		s.handleSessionSnapshot(w, r, id)
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) routeTaskItem(w http.ResponseWriter, r *http.Request, rest string) {
	method := r.Method
	id, suffix, hasSuffix := splitOne(rest)
	if !hasSuffix {
		switch method {
		case http.MethodGet:
			s.handleGetTask(w, r, id)
		default:
			methodNotAllowed(w)
		}
		return
	}
	switch {
	case suffix == "attach" && method == http.MethodPost:
		s.requireOrigin(w, r, func(w http.ResponseWriter, r *http.Request) {
			s.handleAttachAssemblyLine(w, r, id)
		})
	case suffix == "messages" && method == http.MethodPost:
		s.requireOrigin(w, r, func(w http.ResponseWriter, r *http.Request) {
			s.handleTaskSend(w, r, id)
		})
	case suffix == "handoff" && method == http.MethodPost:
		s.requireOrigin(w, r, func(w http.ResponseWriter, r *http.Request) {
			s.handleTaskHandoff(w, r, id)
		})
	case suffix == "complete" && method == http.MethodPost:
		s.requireOrigin(w, r, func(w http.ResponseWriter, r *http.Request) {
			s.handleTaskComplete(w, r, id)
		})
	case suffix == "abandon" && method == http.MethodPost:
		s.requireOrigin(w, r, func(w http.ResponseWriter, r *http.Request) {
			s.handleTaskAbandon(w, r, id)
		})
	case suffix == "stream" && method == http.MethodGet:
		s.handleAttachTaskStream(w, r, id)
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		s.handleSPAStatic(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	body, err := web.FS.ReadFile("dist/index.html")
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(fallbackLoaderHTML))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(body)
}

func (s *Server) handleSPAStatic(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/")
	if rel == "" || strings.Contains(rel, "..") {
		http.NotFound(w, r)
		return
	}
	body, err := web.FS.ReadFile("dist/" + rel)
	if err != nil {
		idx, ierr := web.FS.ReadFile("dist/index.html")
		if ierr != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(idx)
		return
	}
	w.Header().Set("Content-Type", contentTypeFor(rel))
	_, _ = w.Write(body)
}

func contentTypeFor(name string) string {
	switch {
	case strings.HasSuffix(name, ".html"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(name, ".js"):
		return "application/javascript"
	case strings.HasSuffix(name, ".css"):
		return "text/css"
	case strings.HasSuffix(name, ".json"):
		return "application/json"
	case strings.HasSuffix(name, ".svg"):
		return "image/svg+xml"
	case strings.HasSuffix(name, ".png"):
		return "image/png"
	case strings.HasSuffix(name, ".woff2"):
		return "font/woff2"
	case strings.HasSuffix(name, ".map"):
		return "application/json"
	}
	return "application/octet-stream"
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := extractBearerToken(r)
		if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(s.token)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="agentctl"`)
			writeError(w, http.StatusUnauthorized, proto.ErrUnauthenticated, "missing or invalid bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireOrigin gates state-changing requests behind a matching Origin
// header (and Sec-Fetch-Site when present) per ADR 0007.
func (s *Server) requireOrigin(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
	if !s.originAllowed(r) {
		writeError(w, http.StatusForbidden, proto.ErrForbidden, "origin mismatch")
		return
	}
	next(w, r)
}

func (s *Server) originAllowed(r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != s.originOK {
		return false
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		return false
	}
	return true
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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	if status != 0 {
		w.WriteHeader(status)
	}
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": proto.ErrorData{Code: code, Message: message},
	})
}

func methodNotAllowed(w http.ResponseWriter) {
	writeError(w, http.StatusMethodNotAllowed, proto.ErrBadRequest, "method not allowed")
}

func unavailable(w http.ResponseWriter, op, milestone string) {
	writeError(w, http.StatusServiceUnavailable, proto.ErrUnavailable,
		fmt.Sprintf("%s lands in %s", op, milestone))
}

func matchPrefix(path, prefix string) (string, bool) {
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	rest := strings.TrimPrefix(path, prefix)
	if rest == "" {
		return "", false
	}
	return rest, true
}

func splitOne(rest string) (head, tail string, hasTail bool) {
	i := strings.Index(rest, "/")
	if i < 0 {
		return rest, "", false
	}
	return rest[:i], rest[i+1:], true
}

func mapManagerErr(err error) (status int, code string) {
	switch {
	case errors.Is(err, sm.ErrSessionNotFound):
		return http.StatusNotFound, proto.ErrNotFound
	case errors.Is(err, sm.ErrNoInFlight):
		return http.StatusPreconditionFailed, proto.ErrPreconditionFailed
	case errors.Is(err, sm.ErrSnapshotFailed):
		return http.StatusInternalServerError, proto.ErrSnapshotFailed
	default:
		return http.StatusInternalServerError, proto.ErrInternal
	}
}

const fallbackLoaderHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>agentctl Web UI</title>
</head>
<body>
<script>
(function () {
  var hash = window.location.hash || "";
  var token = "";
  if (hash.indexOf("#t=") === 0) { token = decodeURIComponent(hash.substring(3)); }
  else if (hash.indexOf("#token=") === 0) { token = decodeURIComponent(hash.substring(7)); }
  if (token) {
    document.cookie = "agentctl_token=" + token + "; Path=/; SameSite=Strict";
    history.replaceState(null, "", window.location.pathname);
  }
})();
</script>
<p>agentctl Web UI &mdash; SPA build pending; open with <code>agentctl ui</code>.</p>
</body>
</html>
`
