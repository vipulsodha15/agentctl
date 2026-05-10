package log

import (
	"compress/gzip"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	SessionLogPerm  = 0o640
	SessionDirPerm  = 0o700
	DefaultMaxBytes = 50 * 1024 * 1024
	DefaultKeepGen  = 7
)

type SessionLogger struct {
	mu        sync.Mutex
	dir       string
	maxBytes  int64
	keepGen   int
	now       func() time.Time
	currDay   int
	currMonth time.Month
	currYear  int
	f         *os.File
	written   int64
	logger    *slog.Logger
}

type SessionLogOptions struct {
	Dir      string
	MaxBytes int64
	KeepGen  int
	Now      func() time.Time
}

func NewSessionLogger(opts SessionLogOptions) (*SessionLogger, error) {
	if opts.Dir == "" {
		return nil, fmt.Errorf("session log: dir required")
	}
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	keep := opts.KeepGen
	if keep <= 0 {
		keep = DefaultKeepGen
	}
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().Local() }
	}
	if err := os.MkdirAll(opts.Dir, SessionDirPerm); err != nil {
		return nil, fmt.Errorf("session log: mkdir %s: %w", opts.Dir, err)
	}
	sl := &SessionLogger{
		dir:      opts.Dir,
		maxBytes: maxBytes,
		keepGen:  keep,
		now:      now,
	}
	if err := sl.openLocked(); err != nil {
		return nil, err
	}
	handler := slog.NewJSONHandler(&redactingWriter{w: writerFunc(sl.write)}, &slog.HandlerOptions{
		Level: slog.LevelDebug,
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
	sl.logger = slog.New(handler)
	return sl, nil
}

func (s *SessionLogger) Logger() *slog.Logger { return s.logger }

func (s *SessionLogger) Path() string {
	return filepath.Join(s.dir, "agentd.log")
}

func (s *SessionLogger) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f != nil {
		err := s.f.Close()
		s.f = nil
		return err
	}
	return nil
}

func (s *SessionLogger) write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.maybeRotateLocked(int64(len(p))); err != nil {
		return 0, err
	}
	if s.f == nil {
		if err := s.openLocked(); err != nil {
			return 0, err
		}
	}
	n, err := s.f.Write(p)
	s.written += int64(n)
	return n, err
}

func (s *SessionLogger) openLocked() error {
	path := s.Path()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, SessionLogPerm)
	if err != nil {
		return fmt.Errorf("session log open %s: %w", path, err)
	}
	if err := os.Chmod(path, SessionLogPerm); err != nil {
		_ = f.Close()
		return err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	s.f = f
	s.written = info.Size()
	t := s.now()
	s.currYear, s.currMonth, s.currDay = t.Year(), t.Month(), t.Day()
	return nil
}

func (s *SessionLogger) maybeRotateLocked(incoming int64) error {
	t := s.now()
	dayChanged := t.Year() != s.currYear || t.Month() != s.currMonth || t.Day() != s.currDay
	if s.f != nil && (s.written+incoming > s.maxBytes || dayChanged) {
		return s.rotateLocked()
	}
	return nil
}

func (s *SessionLogger) Rotate() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return nil
	}
	return s.rotateLocked()
}

func (s *SessionLogger) rotateLocked() error {
	if s.f != nil {
		_ = s.f.Close()
		s.f = nil
	}
	current := s.Path()
	if _, err := os.Stat(current); err == nil {
		oldest := filepath.Join(s.dir, fmt.Sprintf("agentd.log.%d.gz", s.keepGen))
		_ = os.Remove(oldest)
		for i := s.keepGen - 1; i >= 1; i-- {
			from := filepath.Join(s.dir, fmt.Sprintf("agentd.log.%d.gz", i))
			to := filepath.Join(s.dir, fmt.Sprintf("agentd.log.%d.gz", i+1))
			if _, err := os.Stat(from); err == nil {
				_ = os.Rename(from, to)
			}
		}
		gzPath := filepath.Join(s.dir, "agentd.log.1.gz")
		if err := gzipCopy(current, gzPath); err != nil {
			return fmt.Errorf("rotate gzip: %w", err)
		}
		if err := os.Remove(current); err != nil {
			return fmt.Errorf("rotate remove: %w", err)
		}
	}
	return s.openLocked()
}

func gzipCopy(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, SessionLogPerm)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	gw := gzip.NewWriter(out)
	if _, err := io.Copy(gw, in); err != nil {
		_ = gw.Close()
		return err
	}
	return gw.Close()
}

type writerFunc func(p []byte) (int, error)

func (w writerFunc) Write(p []byte) (int, error) { return w(p) }
