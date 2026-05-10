// Package usage records per-turn token consumption to the `usage` table and
// computes USD cost from the loaded `[pricing.tables]` block. R10 requires
// that rows live past session end and that price-table updates do not
// retroactively affect historical rows; the recorder snapshots
// `price_table_version` at insert time for that reason.
package usage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/agentctl/agentctl/internal/config"
)

type UsageEvent struct {
	SessionID        string
	TurnID           string
	At               time.Time
	Model            string
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
}

type Recorder interface {
	OnUsage(ctx context.Context, ev UsageEvent) error
}

type ModelTotals struct {
	Model            string  `json:"model"`
	Turns            int     `json:"turns"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	CostUSD          float64 `json:"cost_usd"`
	HasUnknown       bool    `json:"has_unknown_model,omitempty"`
}

type TurnRow struct {
	TurnID           string    `json:"turn_id"`
	At               time.Time `json:"at"`
	Model            string    `json:"model"`
	InputTokens      int64     `json:"input_tokens"`
	OutputTokens     int64     `json:"output_tokens"`
	CacheReadTokens  int64     `json:"cache_read_tokens"`
	CacheWriteTokens int64     `json:"cache_write_tokens"`
	CostUSD          *float64  `json:"cost_usd,omitempty"`
}

type PerSessionTotals struct {
	SessionID        string        `json:"session_id"`
	Turns            int           `json:"turns"`
	InputTokens      int64         `json:"input_tokens"`
	OutputTokens     int64         `json:"output_tokens"`
	CacheReadTokens  int64         `json:"cache_read_tokens"`
	CacheWriteTokens int64         `json:"cache_write_tokens"`
	CostUSD          float64       `json:"cost_usd"`
	HasUnknown       bool          `json:"has_unknown_model,omitempty"`
	ByModel          []ModelTotals `json:"by_model"`
	Timeline         []TurnRow     `json:"timeline"`
}

type SessionTotals struct {
	SessionID    string  `json:"session_id"`
	Name         string  `json:"name,omitempty"`
	Status       string  `json:"status,omitempty"`
	Turns        int     `json:"turns"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	HasUnknown   bool    `json:"has_unknown_model,omitempty"`
}

type RangeTotals struct {
	Start        time.Time       `json:"start"`
	End          time.Time       `json:"end"`
	Turns        int             `json:"turns"`
	InputTokens  int64           `json:"input_tokens"`
	OutputTokens int64           `json:"output_tokens"`
	CostUSD      float64         `json:"cost_usd"`
	HasUnknown   bool            `json:"has_unknown_model,omitempty"`
	BySession    []SessionTotals `json:"by_session"`
}

type Aggregator interface {
	PerSession(ctx context.Context, sessionID string) (PerSessionTotals, error)
	Range(ctx context.Context, start, end time.Time, sessionFilter string) (RangeTotals, error)
	RunningTotals(ctx context.Context, sessionIDs []string) (map[string]float64, error)
}

type Options struct {
	Store   Store
	Pricing config.PricingTables
	Logger  *slog.Logger
	Now     func() time.Time
}

type Store interface {
	DB() *sql.DB
}

type Service struct {
	store   Store
	pricing config.PricingTables
	logger  *slog.Logger
	now     func() time.Time

	mu sync.Mutex
}

// New returns a value implementing both Recorder and Aggregator. The
// recorder serializes inserts on a single mutex; aggregator reads do not
// take it (sqlite WAL handles read-write concurrency).
func New(opts Options) *Service {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{
		store:   opts.Store,
		pricing: opts.Pricing,
		logger:  opts.Logger,
		now:     opts.Now,
	}
}

var ErrNoStore = errors.New("usage: store not configured")

func (s *Service) OnUsage(ctx context.Context, ev UsageEvent) error {
	if s.store == nil {
		return ErrNoStore
	}
	if ev.SessionID == "" || ev.TurnID == "" {
		return fmt.Errorf("usage: session_id and turn_id required")
	}
	at := ev.At
	if at.IsZero() {
		at = s.now()
	}
	cost, costOK := s.computeCost(ev)
	if !costOK {
		s.logger.Warn("usage.unknown_model",
			slog.String("session_id", ev.SessionID),
			slog.String("turn_id", ev.TurnID),
			slog.String("model", ev.Model))
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	var costArg any
	if costOK {
		costArg = cost
	} else {
		costArg = nil
	}
	_, err := s.store.DB().ExecContext(ctx,
		`INSERT OR IGNORE INTO usage
            (session_id, turn_id, at, model,
             input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
             cost_usd, price_table_version)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.SessionID, ev.TurnID, at.Format(time.RFC3339Nano), ev.Model,
		ev.InputTokens, ev.OutputTokens, ev.CacheReadTokens, ev.CacheWriteTokens,
		costArg, s.pricing.Version,
	)
	if err != nil {
		return fmt.Errorf("usage insert: %w", err)
	}
	return nil
}

// CostFor exposes the price-table calculation for callers (e.g., the actor
// that wants to enrich its outbound `usage` event with the same cost the
// recorder just persisted). The second return is false when the model is
// not in the table.
func (s *Service) CostFor(ev UsageEvent) (float64, bool) {
	return s.computeCost(ev)
}

func (s *Service) computeCost(ev UsageEvent) (float64, bool) {
	if s.pricing.Models == nil {
		return 0, false
	}
	entry, ok := s.pricing.Models[ev.Model]
	if !ok {
		return 0, false
	}
	cost := (float64(ev.InputTokens)*entry.Input +
		float64(ev.OutputTokens)*entry.Output +
		float64(ev.CacheReadTokens)*entry.CacheRead +
		float64(ev.CacheWriteTokens)*entry.CacheWrite) / 1_000_000.0
	return cost, true
}
