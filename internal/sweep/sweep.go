package sweep

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	agentlog "github.com/agentctl/agentctl/internal/log"
	"github.com/agentctl/agentctl/internal/store"
)

const (
	defaultIdleTickInterval      = 60 * time.Second
	defaultHardCutoffInterval    = 60 * time.Second
	defaultIdemCleanupInterval   = 5 * time.Minute
	defaultTombstoneReapInterval = 6 * time.Hour
	defaultIdemTTL               = 5 * time.Minute
	defaultTombstoneAge          = 7 * 24 * time.Hour
	stopReasonIdle               = "idle_timeout"
	stopReasonHardCutoff         = "hard_cutoff"
)

var ErrNoSession = errors.New("sweep: session not found")

type Sweeper interface {
	Tick(ctx context.Context) (actions int, err error)
	Name() string
	Interval() time.Duration
}

type Manager interface {
	Busy(sessionID string) (busy bool, ok bool)
	Stop(ctx context.Context, sessionID string, reason string) error
	Interrupt(ctx context.Context, sessionID string, clearQueue bool) error
}

type Options struct {
	Store        *store.Store
	Manager      Manager
	SessionsDir  string
	IdleTimeout  time.Duration
	MaxIdle      time.Duration
	IdemTTL      time.Duration
	TombstoneAge time.Duration
	IdleInterval time.Duration
	HardInterval time.Duration
	IdemInterval time.Duration
	ReapInterval time.Duration
	Now          func() time.Time
	Logger       *slog.Logger
}

func New(opts Options) []Sweeper {
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	if opts.IdleInterval == 0 {
		opts.IdleInterval = defaultIdleTickInterval
	}
	if opts.HardInterval == 0 {
		opts.HardInterval = defaultHardCutoffInterval
	}
	if opts.IdemInterval == 0 {
		opts.IdemInterval = defaultIdemCleanupInterval
	}
	if opts.ReapInterval == 0 {
		opts.ReapInterval = defaultTombstoneReapInterval
	}
	if opts.IdemTTL == 0 {
		opts.IdemTTL = defaultIdemTTL
	}
	if opts.TombstoneAge == 0 {
		opts.TombstoneAge = defaultTombstoneAge
	}
	if opts.Logger == nil {
		opts.Logger = agentlog.New(agentlog.Options{Component: agentlog.ComponentSweep})
	}
	return []Sweeper{
		&idleStopSweeper{opts: opts},
		&hardCutoffSweeper{opts: opts},
		&idemCleanupSweeper{opts: opts},
		&tombstoneReapSweeper{opts: opts},
	}
}

func RunAll(ctx context.Context, sweepers []Sweeper, logger *slog.Logger) {
	if logger == nil {
		logger = agentlog.New(agentlog.Options{Component: agentlog.ComponentSweep})
	}
	for _, s := range sweepers {
		s := s
		logger.Info("sweep.started", slog.String("name", s.Name()), slog.Duration("interval", s.Interval()))
		go runOne(ctx, s, logger)
	}
}

func runOne(ctx context.Context, s Sweeper, logger *slog.Logger) {
	t := time.NewTicker(s.Interval())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("sweep.stopped", slog.String("name", s.Name()))
			return
		case <-t.C:
			n, err := s.Tick(ctx)
			if err != nil {
				logger.Warn("sweep.tick_failed",
					slog.String("name", s.Name()),
					slog.String("error", err.Error()))
				continue
			}
			if n > 0 {
				logger.Info("sweep.tick",
					slog.String("name", s.Name()),
					slog.Int("actions", n))
			}
		}
	}
}

type idleStopSweeper struct{ opts Options }

func (s *idleStopSweeper) Name() string            { return "idle_stop" }
func (s *idleStopSweeper) Interval() time.Duration { return s.opts.IdleInterval }

func (s *idleStopSweeper) Tick(ctx context.Context) (int, error) {
	if s.opts.Store == nil || s.opts.Manager == nil || s.opts.IdleTimeout <= 0 {
		return 0, nil
	}
	cutoff := s.opts.Now().Add(-s.opts.IdleTimeout).Format(time.RFC3339Nano)
	rows, err := s.opts.Store.DB().QueryContext(ctx,
		`SELECT id FROM sessions WHERE status='running' AND last_activity_at < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("query: %w", err)
	}
	ids, err := scanSessionIDs(rows)
	if err != nil {
		return 0, err
	}
	actions := 0
	for _, id := range ids {
		busy, ok := s.opts.Manager.Busy(id)
		if !ok {
			s.opts.Logger.Debug("sweep.idle_stop.no_actor", slog.String("session_id", id))
			continue
		}
		if busy {
			s.opts.Logger.Debug("sweep.idle_stop.skip_busy", slog.String("session_id", id))
			continue
		}
		if err := s.opts.Manager.Stop(ctx, id, stopReasonIdle); err != nil {
			s.opts.Logger.Warn("sweep.idle_stop.stop_failed",
				slog.String("session_id", id),
				slog.String("error", err.Error()))
			continue
		}
		actions++
		s.opts.Logger.Info("sweep.idle_stop",
			slog.String("session_id", id),
			slog.String("reason", stopReasonIdle))
	}
	return actions, nil
}

type hardCutoffSweeper struct{ opts Options }

func (s *hardCutoffSweeper) Name() string            { return "hard_cutoff" }
func (s *hardCutoffSweeper) Interval() time.Duration { return s.opts.HardInterval }

func (s *hardCutoffSweeper) Tick(ctx context.Context) (int, error) {
	if s.opts.Store == nil || s.opts.Manager == nil || s.opts.MaxIdle <= 0 {
		return 0, nil
	}
	cutoff := s.opts.Now().Add(-s.opts.MaxIdle).Format(time.RFC3339Nano)
	rows, err := s.opts.Store.DB().QueryContext(ctx,
		`SELECT id, status FROM sessions WHERE status IN ('running','stopped') AND last_activity_at < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("query: %w", err)
	}
	type sessionEntry struct {
		id, status string
	}
	defer func() { _ = rows.Close() }()
	var batch []sessionEntry
	for rows.Next() {
		var r sessionEntry
		if err := rows.Scan(&r.id, &r.status); err != nil {
			return 0, err
		}
		batch = append(batch, r)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	actions := 0
	for _, r := range batch {
		if r.status == "running" {
			if err := s.opts.Manager.Interrupt(ctx, r.id, true); err != nil && !errors.Is(err, ErrNoSession) {
				s.opts.Logger.Warn("sweep.hard_cutoff.interrupt_failed",
					slog.String("session_id", r.id),
					slog.String("error", err.Error()))
			}
		}
		if err := s.opts.Manager.Stop(ctx, r.id, stopReasonHardCutoff); err != nil {
			if errors.Is(err, ErrNoSession) {
				continue
			}
			s.opts.Logger.Warn("sweep.hard_cutoff.stop_failed",
				slog.String("session_id", r.id),
				slog.String("error", err.Error()))
			continue
		}
		actions++
		s.opts.Logger.Info("sweep.hard_cutoff",
			slog.String("session_id", r.id),
			slog.String("from_status", r.status))
	}
	return actions, nil
}

type idemCleanupSweeper struct{ opts Options }

func (s *idemCleanupSweeper) Name() string            { return "idem_cleanup" }
func (s *idemCleanupSweeper) Interval() time.Duration { return s.opts.IdemInterval }

func (s *idemCleanupSweeper) Tick(ctx context.Context) (int, error) {
	if s.opts.Store == nil {
		return 0, nil
	}
	cutoff := s.opts.Now().Add(-s.opts.IdemTTL).Format(time.RFC3339Nano)
	res, err := s.opts.Store.DB().ExecContext(ctx,
		`DELETE FROM message_idempotency WHERE accepted_at < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("delete: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

type tombstoneReapSweeper struct{ opts Options }

func (s *tombstoneReapSweeper) Name() string            { return "tombstone_reap" }
func (s *tombstoneReapSweeper) Interval() time.Duration { return s.opts.ReapInterval }

func (s *tombstoneReapSweeper) Tick(_ context.Context) (int, error) {
	if s.opts.SessionsDir == "" {
		return 0, nil
	}
	dir := filepath.Join(s.opts.SessionsDir, ".tombstones")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("read %s: %w", dir, err)
	}
	threshold := s.opts.Now().Add(-s.opts.TombstoneAge)
	count := 0
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		info, err := e.Info()
		if err != nil {
			s.opts.Logger.Warn("sweep.tombstone_reap.stat_failed",
				slog.String("path", path),
				slog.String("error", err.Error()))
			continue
		}
		if info.ModTime().After(threshold) {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			s.opts.Logger.Warn("sweep.tombstone_reap.rm_failed",
				slog.String("path", path),
				slog.String("error", err.Error()))
			continue
		}
		count++
		s.opts.Logger.Info("sweep.tombstone_reap",
			slog.String("path", path),
			slog.Time("mtime", info.ModTime()))
	}
	return count, nil
}

func scanSessionIDs(rows *sql.Rows) ([]string, error) {
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
