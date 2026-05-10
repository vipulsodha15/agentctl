package log

import (
	"context"
	"io"
	"log/slog"
	"os"
	"regexp"
	"sync"
	"time"
)

const (
	ComponentBoot      = "boot"
	ComponentMigration = "migration"
	ComponentSessions  = "sessions"
	ComponentContainer = "containers"
	ComponentMCP       = "mcp"
	ComponentWeb       = "web"
	ComponentSock      = "sock"
	ComponentCLI       = "cli"
	ComponentSweep     = "sweep"
	ComponentRecovery  = "recovery"
	ComponentUsage     = "usage"
	ComponentDoctor    = "doctor"
	ComponentService   = "service"
	ComponentUpdate    = "update"
)

var redactPatterns = []*regexp.Regexp{
	regexp.MustCompile(`sk-ant-[A-Za-z0-9_\-]+`),
	regexp.MustCompile(`gh[psour]_[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]+`),
}

var (
	dynamicMu sync.RWMutex
	dynamic   = []string{}
)

func RegisterSecret(s string) {
	if s == "" {
		return
	}
	dynamicMu.Lock()
	defer dynamicMu.Unlock()
	for _, existing := range dynamic {
		if existing == s {
			return
		}
	}
	dynamic = append(dynamic, s)
}

func ClearDynamicSecrets() {
	dynamicMu.Lock()
	defer dynamicMu.Unlock()
	dynamic = nil
}

func Redact(s string) string {
	for _, re := range redactPatterns {
		s = re.ReplaceAllString(s, "***REDACTED***")
	}
	dynamicMu.RLock()
	defer dynamicMu.RUnlock()
	for _, secret := range dynamic {
		if secret == "" {
			continue
		}
		s = replaceLiteral(s, secret, "***REDACTED***")
	}
	return s
}

func replaceLiteral(haystack, needle, repl string) string {
	if needle == "" {
		return haystack
	}
	out := make([]byte, 0, len(haystack))
	for {
		i := indexOf(haystack, needle)
		if i < 0 {
			out = append(out, haystack...)
			break
		}
		out = append(out, haystack[:i]...)
		out = append(out, repl...)
		haystack = haystack[i+len(needle):]
	}
	return string(out)
}

func indexOf(haystack, needle string) int {
	hl, nl := len(haystack), len(needle)
	if nl == 0 || nl > hl {
		return -1
	}
	for i := 0; i+nl <= hl; i++ {
		if haystack[i:i+nl] == needle {
			return i
		}
	}
	return -1
}

type redactingWriter struct {
	w io.Writer
}

func (rw *redactingWriter) Write(p []byte) (int, error) {
	redacted := Redact(string(p))
	if _, err := rw.w.Write([]byte(redacted)); err != nil {
		return 0, err
	}
	return len(p), nil
}

type Options struct {
	Level     slog.Level
	Output    io.Writer
	Component string
}

func New(opts Options) *slog.Logger {
	out := opts.Output
	if out == nil {
		out = os.Stderr
	}
	handler := slog.NewJSONHandler(&redactingWriter{w: out}, &slog.HandlerOptions{
		Level: opts.Level,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			switch a.Key {
			case slog.TimeKey:
				return slog.String("ts", a.Value.Time().UTC().Format(time.RFC3339Nano))
			case slog.LevelKey:
				return slog.String("level", a.Value.String())
			case slog.MessageKey:
				return slog.String("msg", a.Value.String())
			}
			return a
		},
	})
	logger := slog.New(handler)
	if opts.Component != "" {
		logger = logger.With(slog.String("component", opts.Component))
	}
	return logger
}

func ParseLevel(s string) slog.Level {
	switch s {
	case "debug", "DEBUG":
		return slog.LevelDebug
	case "warn", "WARN", "warning":
		return slog.LevelWarn
	case "error", "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

type Logger interface {
	DebugContext(ctx context.Context, msg string, args ...any)
	InfoContext(ctx context.Context, msg string, args ...any)
	WarnContext(ctx context.Context, msg string, args ...any)
	ErrorContext(ctx context.Context, msg string, args ...any)
}
