package mcp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/agentctl/agentctl/internal/store"
)

var (
	ErrNotFound = errors.New("mcp: not found")
	ErrConflict = errors.New("mcp: name already exists")
)

type Entry struct {
	Name           string    `json:"name"`
	URL            string    `json:"url"`
	Transport      string    `json:"transport"`
	Kind           string    `json:"kind"`
	AuthConfigJSON string    `json:"auth_config,omitempty"`
	DefaultEnabled bool      `json:"default_enabled"`
	Description    string    `json:"description,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type EntryUpdate struct {
	URL            *string `json:"url,omitempty"`
	Transport      *string `json:"transport,omitempty"`
	Kind           *string `json:"kind,omitempty"`
	AuthConfigJSON *string `json:"auth_config,omitempty"`
	DefaultEnabled *bool   `json:"default_enabled,omitempty"`
	Description    *string `json:"description,omitempty"`
}

type Registry interface {
	List(ctx context.Context) ([]Entry, error)
	Get(ctx context.Context, name string) (Entry, error)
	Add(ctx context.Context, e Entry) error
	Update(ctx context.Context, name string, upd EntryUpdate) error
	Remove(ctx context.Context, name string, force bool) error
	SetDefault(ctx context.Context, name string, defaultEnabled bool) error
}

type sqlRegistry struct {
	store *store.Store
	now   func() time.Time
}

type Options struct {
	Store *store.Store
	Now   func() time.Time
}

func NewRegistry(opts Options) Registry {
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &sqlRegistry{store: opts.Store, now: now}
}

const baseSelect = `SELECT name, url, transport, kind, auth_config_json,
        default_enabled, description, created_at, updated_at FROM mcp_registry`

func (r *sqlRegistry) List(ctx context.Context) ([]Entry, error) {
	rows, err := r.store.DB().QueryContext(ctx, baseSelect+` ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("mcp list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]Entry, 0, 8)
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *sqlRegistry) Get(ctx context.Context, name string) (Entry, error) {
	row := r.store.DB().QueryRowContext(ctx, baseSelect+` WHERE name = ?`, name)
	e, err := scanEntry(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Entry{}, ErrNotFound
		}
		return Entry{}, err
	}
	return e, nil
}

func (r *sqlRegistry) Add(ctx context.Context, e Entry) error {
	if e.Name == "" || e.URL == "" {
		return fmt.Errorf("mcp add: name and url are required")
	}
	if e.Transport == "" {
		e.Transport = "http"
	}
	if e.Kind == "" {
		e.Kind = "none"
	}
	now := r.now().Format(time.RFC3339Nano)
	_, err := r.store.DB().ExecContext(ctx, `INSERT INTO mcp_registry
        (name, url, transport, kind, auth_config_json, default_enabled, description, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Name, e.URL, e.Transport, e.Kind, nullableString(e.AuthConfigJSON),
		boolToInt(e.DefaultEnabled), nullableString(e.Description), now, now,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrConflict
		}
		return fmt.Errorf("mcp add: %w", err)
	}
	return nil
}

func (r *sqlRegistry) Update(ctx context.Context, name string, upd EntryUpdate) error {
	sets := make([]string, 0, 6)
	args := make([]any, 0, 7)
	if upd.URL != nil {
		sets = append(sets, "url = ?")
		args = append(args, *upd.URL)
	}
	if upd.Transport != nil {
		sets = append(sets, "transport = ?")
		args = append(args, *upd.Transport)
	}
	if upd.Kind != nil {
		sets = append(sets, "kind = ?")
		args = append(args, *upd.Kind)
	}
	if upd.AuthConfigJSON != nil {
		sets = append(sets, "auth_config_json = ?")
		args = append(args, nullableString(*upd.AuthConfigJSON))
	}
	if upd.DefaultEnabled != nil {
		sets = append(sets, "default_enabled = ?")
		args = append(args, boolToInt(*upd.DefaultEnabled))
	}
	if upd.Description != nil {
		sets = append(sets, "description = ?")
		args = append(args, nullableString(*upd.Description))
	}
	if len(sets) == 0 {
		_, err := r.Get(ctx, name)
		return err
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, r.now().Format(time.RFC3339Nano))
	args = append(args, name)
	q := `UPDATE mcp_registry SET ` + strings.Join(sets, ", ") + ` WHERE name = ?`
	res, err := r.store.DB().ExecContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("mcp update: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *sqlRegistry) Remove(ctx context.Context, name string, _ bool) error {
	// TODO(M4): consult sessions.mcp_set_json for active references and reject without --force.
	res, err := r.store.DB().ExecContext(ctx, `DELETE FROM mcp_registry WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("mcp remove: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *sqlRegistry) SetDefault(ctx context.Context, name string, defaultEnabled bool) error {
	now := r.now().Format(time.RFC3339Nano)
	res, err := r.store.DB().ExecContext(ctx,
		`UPDATE mcp_registry SET default_enabled = ?, updated_at = ? WHERE name = ?`,
		boolToInt(defaultEnabled), now, name)
	if err != nil {
		return fmt.Errorf("mcp set-default: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanEntry(s rowScanner) (Entry, error) {
	var (
		e             Entry
		auth, descr   sql.NullString
		defaultEnab   int
		createdAt, up string
	)
	if err := s.Scan(&e.Name, &e.URL, &e.Transport, &e.Kind, &auth, &defaultEnab, &descr, &createdAt, &up); err != nil {
		return Entry{}, err
	}
	if auth.Valid {
		e.AuthConfigJSON = auth.String
	}
	if descr.Valid {
		e.Description = descr.String
	}
	e.DefaultEnabled = defaultEnab != 0
	if t, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
		e.CreatedAt = t
	}
	if t, err := time.Parse(time.RFC3339Nano, up); err == nil {
		e.UpdatedAt = t
	}
	return e, nil
}

// ApplySeed inserts the parsed seed rows with INSERT OR IGNORE semantics so
// existing user-edited rows are preserved (ADR 0006).
func ApplySeed(s *store.Store, entries []SeedEntry, now time.Time) (int, error) {
	rows := make([]store.MCPSeedRow, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, store.MCPSeedRow{
			Name:           e.Name,
			URL:            e.URL,
			Transport:      e.Transport,
			Kind:           e.Kind,
			AuthConfigJSON: e.AuthConfigJSON,
			DefaultEnabled: e.DefaultEnabled,
			Description:    e.Description,
		})
	}
	_ = now
	return s.ApplyMCPSeed(rows)
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE") || strings.Contains(msg, "constraint failed")
}
