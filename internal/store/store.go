package store

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

const SchemaMaxVersion = 5

type Store struct {
	db   *sql.DB
	path string
}

type Options struct {
	Path string
}

func Open(opts Options) (*Store, error) {
	if opts.Path == "" {
		return nil, errors.New("store: empty path")
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)", opts.Path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", opts.Path, err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	return &Store{db: db, path: opts.Path}, nil
}

func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) Path() string { return s.path }

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Migrate() error {
	current, err := s.SchemaVersion()
	if err != nil {
		return err
	}
	if current > SchemaMaxVersion {
		return fmt.Errorf("schema_too_new: db_version=%d binary_max_version=%d", current, SchemaMaxVersion)
	}
	files, err := listMigrations()
	if err != nil {
		return err
	}
	for _, f := range files {
		if f.version <= current {
			continue
		}
		if err := s.applyMigration(f); err != nil {
			return fmt.Errorf("apply migration %s: %w", f.name, err)
		}
	}
	return nil
}

func (s *Store) SchemaVersion() (int, error) {
	var hasTable int
	err := s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='schema_version'`).Scan(&hasTable)
	if err != nil {
		return 0, fmt.Errorf("probe schema_version: %w", err)
	}
	if hasTable == 0 {
		return 0, nil
	}
	var v int
	if err := s.db.QueryRow(`SELECT max(version) FROM schema_version`).Scan(&v); err != nil {
		return 0, fmt.Errorf("read schema_version: %w", err)
	}
	return v, nil
}

type migrationFile struct {
	name    string
	version int
	body    []byte
}

func listMigrations() ([]migrationFile, error) {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return nil, err
	}
	var out []migrationFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		ver, err := parseMigrationVersion(e.Name())
		if err != nil {
			return nil, err
		}
		body, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, migrationFile{name: e.Name(), version: ver, body: body})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

func parseMigrationVersion(name string) (int, error) {
	if len(name) < 5 {
		return 0, fmt.Errorf("migration name too short: %s", name)
	}
	prefix := name[:4]
	v := 0
	for _, c := range prefix {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("non-numeric migration prefix: %s", name)
		}
		v = v*10 + int(c-'0')
	}
	return v, nil
}

func (s *Store) applyMigration(f migrationFile) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(string(f.body)); err != nil {
		return fmt.Errorf("exec %s: %w", f.name, err)
	}
	return tx.Commit()
}

type MCPSeedRow struct {
	Name           string
	URL            string
	Transport      string
	Kind           string
	AuthConfigJSON string
	DefaultEnabled bool
	Description    string
}

func (s *Store) ApplyMCPSeed(rows []MCPSeedRow) (inserted int, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO mcp_registry
        (name, url, transport, kind, auth_config_json, default_enabled, description, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer func() { _ = stmt.Close() }()
	for _, r := range rows {
		var ac any
		if r.AuthConfigJSON != "" {
			ac = r.AuthConfigJSON
		}
		var de int
		if r.DefaultEnabled {
			de = 1
		}
		res, ierr := stmt.Exec(r.Name, r.URL, r.Transport, r.Kind, ac, de, nullableString(r.Description), now, now)
		if ierr != nil {
			return inserted, fmt.Errorf("seed mcp %s: %w", r.Name, ierr)
		}
		n, _ := res.RowsAffected()
		inserted += int(n)
	}
	if err := tx.Commit(); err != nil {
		return inserted, err
	}
	return inserted, nil
}

func (s *Store) CountMCPs() (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM mcp_registry`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// WorkspaceState returns the value for `key` in the workspace_state kv
// table, or empty string if the key is missing. The table is a per-daemon
// behavioural-state bag, not configuration; see ADR 0020 §3 (sticky
// last-used-provider lives here, not in config.toml).
func (s *Store) WorkspaceState(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM workspace_state WHERE key = ?`, key).Scan(&v)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return v, nil
}

func (s *Store) IntegrityCheck() (string, error) {
	var result string
	if err := s.db.QueryRow(`PRAGMA integrity_check`).Scan(&result); err != nil {
		return "", err
	}
	return result, nil
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
