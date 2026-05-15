// Package ttl ("task library") owns the persistence + validation of Agent
// and AssemblyLine definitions. Storage is sqlite-backed so the daemon stays
// stateless-container friendly: there are no on-disk YAML files to keep
// alive across pod restarts.
//
// Built-in YAMLs ship embedded in the binary (see embed.go) and are upserted
// into the agents/assembly_lines tables at boot via Materialize. Custom entries
// flow in through Put{Agent,AssemblyLine} from the CLI / HTTP API.
package ttl

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	MaxAgentBytes        = 16 * 1024
	MaxAssemblyLineBytes = 16 * 1024
	MaxStages            = 16

	SourceBuiltin = "builtin"
	SourceCustom  = "custom"
)

var validColours = map[string]bool{
	"blue": true, "purple": true, "green": true,
	"amber": true, "red": true, "slate": true,
}

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

var (
	ErrNotFound        = errors.New("ttl: not found")
	ErrBuiltinReadOnly = errors.New("ttl: built-in is read-only")
	ErrValidation      = errors.New("ttl: validation failed")
	ErrInUse           = errors.New("ttl: in use")
)

// Agent is the schema for an agent. The yaml tags are what the on-the-wire
// YAML body looks like; json tags govern the HTTP API. Source/LoadedAt are
// not serialized into the YAML body — they are computed from the row.
type Agent struct {
	Name          string   `yaml:"name" json:"name"`
	Description   string   `yaml:"description" json:"description"`
	Colour        string   `yaml:"colour" json:"colour"`
	Model         string   `yaml:"model,omitempty" json:"model,omitempty"`
	Prompt        string   `yaml:"prompt" json:"prompt"`
	MCPsAllowed   []string `yaml:"mcps_allowed,omitempty" json:"mcps_allowed,omitempty"`
	SkillsAllowed []string `yaml:"skills_allowed,omitempty" json:"skills_allowed,omitempty"`

	Source   string    `yaml:"-" json:"source,omitempty"`
	LoadedAt time.Time `yaml:"-" json:"loaded_at,omitempty"`
}

// AssemblyLineStage is one entry in an assembly line's `stages:` list.
type AssemblyLineStage struct {
	Agent string `yaml:"agent" json:"agent"`
}

// AssemblyLine is the schema for an assembly line.
type AssemblyLine struct {
	Name        string              `yaml:"name" json:"name"`
	Description string              `yaml:"description" json:"description"`
	Stages      []AssemblyLineStage `yaml:"stages" json:"stages"`

	Source   string    `yaml:"-" json:"source,omitempty"`
	LoadedAt time.Time `yaml:"-" json:"loaded_at,omitempty"`
}

// DB is the subset of *sql.DB the library needs. Declared as an interface so
// tests can pass an in-memory shim if they prefer.
type DB interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Library is the in-memory index of agents + assembly lines, backed by sqlite.
// All mutations write the DB row first; the in-memory cache is rebuilt from
// the DB on every change so the cache cannot drift from canonical state.
type Library struct {
	db DB

	mu            sync.RWMutex
	agents        map[string]*Agent
	assemblyLines map[string]*AssemblyLine
}

type Options struct {
	DB DB
}

func New(opts Options) *Library {
	return &Library{
		db:            opts.DB,
		agents:        map[string]*Agent{},
		assemblyLines: map[string]*AssemblyLine{},
	}
}

// LoadIssues collects per-name parse/validation errors encountered while
// loading rows from the DB. Bad rows are skipped (not loaded into the cache)
// so a single malformed entry cannot poison the rest of the library.
type LoadIssues struct {
	AgentErrors        map[string]string
	AssemblyLineErrors map[string]string
}

// Load rebuilds the in-memory index from the agents/assembly_lines tables.
func (l *Library) Load(ctx context.Context) (LoadIssues, error) {
	issues := LoadIssues{
		AgentErrors:        map[string]string{},
		AssemblyLineErrors: map[string]string{},
	}

	agentRows, err := l.db.QueryContext(ctx,
		`SELECT name, source, yaml_body, updated_at FROM agents`)
	if err != nil {
		return issues, fmt.Errorf("query agents: %w", err)
	}
	defer agentRows.Close()

	agents := map[string]*Agent{}
	for agentRows.Next() {
		var name, source, body, updatedAt string
		if err := agentRows.Scan(&name, &source, &body, &updatedAt); err != nil {
			return issues, fmt.Errorf("scan agent: %w", err)
		}
		a, perr := ParseAgentYAML([]byte(body))
		if perr != nil {
			issues.AgentErrors[name] = perr.Error()
			continue
		}
		if verr := validateAgent(a); verr != nil {
			issues.AgentErrors[name] = verr.Error()
			continue
		}
		a.Source = source
		if t, err := time.Parse(time.RFC3339Nano, updatedAt); err == nil {
			a.LoadedAt = t
		}
		agents[a.Name] = a
	}
	if err := agentRows.Err(); err != nil {
		return issues, err
	}

	assemblyLineRows, err := l.db.QueryContext(ctx,
		`SELECT name, source, yaml_body, updated_at FROM assembly_lines`)
	if err != nil {
		return issues, fmt.Errorf("query assembly_lines: %w", err)
	}
	defer assemblyLineRows.Close()

	assemblyLines := map[string]*AssemblyLine{}
	for assemblyLineRows.Next() {
		var name, source, body, updatedAt string
		if err := assemblyLineRows.Scan(&name, &source, &body, &updatedAt); err != nil {
			return issues, fmt.Errorf("scan assembly_line: %w", err)
		}
		w, perr := ParseAssemblyLineYAML([]byte(body))
		if perr != nil {
			issues.AssemblyLineErrors[name] = perr.Error()
			continue
		}
		if verr := validateAssemblyLine(w); verr != nil {
			issues.AssemblyLineErrors[name] = verr.Error()
			continue
		}
		w.Source = source
		if t, err := time.Parse(time.RFC3339Nano, updatedAt); err == nil {
			w.LoadedAt = t
		}
		assemblyLines[w.Name] = w
	}
	if err := assemblyLineRows.Err(); err != nil {
		return issues, err
	}

	// Cross-reference: assembly lines that name a missing agent are loaded
	// but flagged so the UI can render the validation error rather than
	// silently hiding them.
	for name, w := range assemblyLines {
		for _, st := range w.Stages {
			if _, ok := agents[st.Agent]; !ok {
				issues.AssemblyLineErrors[name] = fmt.Sprintf("references missing agent %q", st.Agent)
			}
		}
	}

	l.mu.Lock()
	l.agents = agents
	l.assemblyLines = assemblyLines
	l.mu.Unlock()
	return issues, nil
}

// ParseAgentYAML decodes raw YAML bytes into an *Agent (no DB-side state).
func ParseAgentYAML(body []byte) (*Agent, error) {
	var a Agent
	if err := yaml.Unmarshal(body, &a); err != nil {
		return nil, fmt.Errorf("yaml: %w", err)
	}
	return &a, nil
}

// ParseAssemblyLineYAML decodes raw YAML bytes into an *AssemblyLine.
func ParseAssemblyLineYAML(body []byte) (*AssemblyLine, error) {
	var w AssemblyLine
	if err := yaml.Unmarshal(body, &w); err != nil {
		return nil, fmt.Errorf("yaml: %w", err)
	}
	return &w, nil
}

func validateAgent(a *Agent) error {
	if a.Name == "" {
		return fmt.Errorf("%w: agent name is required", ErrValidation)
	}
	if !nameRe.MatchString(a.Name) {
		return fmt.Errorf("%w: name %q must match [a-z][a-z0-9-]{0,62}", ErrValidation, a.Name)
	}
	if a.Description == "" {
		return fmt.Errorf("%w: description is required", ErrValidation)
	}
	if a.Colour == "" {
		a.Colour = "slate"
	}
	if !validColours[a.Colour] {
		return fmt.Errorf("%w: colour %q not in {blue,purple,green,amber,red,slate}", ErrValidation, a.Colour)
	}
	if strings.TrimSpace(a.Prompt) == "" {
		return fmt.Errorf("%w: prompt is required", ErrValidation)
	}
	return nil
}

func validateAssemblyLine(w *AssemblyLine) error {
	if w.Name == "" {
		return fmt.Errorf("%w: assembly line name is required", ErrValidation)
	}
	if !nameRe.MatchString(w.Name) {
		return fmt.Errorf("%w: name %q must match [a-z][a-z0-9-]{0,62}", ErrValidation, w.Name)
	}
	if w.Description == "" {
		return fmt.Errorf("%w: description is required", ErrValidation)
	}
	if len(w.Stages) == 0 {
		return fmt.Errorf("%w: assembly line needs at least one stage", ErrValidation)
	}
	if len(w.Stages) > MaxStages {
		return fmt.Errorf("%w: assembly line has more than %d stages", ErrValidation, MaxStages)
	}
	for i, s := range w.Stages {
		if s.Agent == "" {
			return fmt.Errorf("%w: stage %d has no agent", ErrValidation, i+1)
		}
	}
	return nil
}

// ListAgents returns agents sorted by name.
func (l *Library) ListAgents() []Agent {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]Agent, 0, len(l.agents))
	for _, a := range l.agents {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (l *Library) GetAgent(name string) (Agent, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	a, ok := l.agents[name]
	if !ok {
		return Agent{}, ErrNotFound
	}
	return *a, nil
}

func (l *Library) ListAssemblyLines() []AssemblyLine {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]AssemblyLine, 0, len(l.assemblyLines))
	for _, w := range l.assemblyLines {
		out = append(out, *w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (l *Library) GetAssemblyLine(name string) (AssemblyLine, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	w, ok := l.assemblyLines[name]
	if !ok {
		return AssemblyLine{}, ErrNotFound
	}
	return *w, nil
}

// PutAgent writes a custom agent. If body is non-empty it is the canonical
// YAML; otherwise spec is re-serialized via yaml.Marshal. Built-in rows
// cannot be overwritten through this path — saving over a built-in always
// creates a new custom row (the in-memory index maps custom on top of the
// built-in on read).
func (l *Library) PutAgent(ctx context.Context, spec Agent, body []byte) (Agent, error) {
	if len(body) > 0 {
		parsed, err := ParseAgentYAML(body)
		if err != nil {
			return Agent{}, err
		}
		if spec.Name != "" && parsed.Name != "" && parsed.Name != spec.Name {
			return Agent{}, fmt.Errorf("%w: body name %q != path name %q", ErrValidation, parsed.Name, spec.Name)
		}
		spec = *parsed
	}
	if err := validateAgent(&spec); err != nil {
		return Agent{}, err
	}
	if len(body) == 0 {
		// Spec was provided typed — re-emit it as YAML for storage so the
		// "show this agent's YAML" path always has bytes to return.
		b, err := yaml.Marshal(&spec)
		if err != nil {
			return Agent{}, fmt.Errorf("yaml marshal: %w", err)
		}
		body = b
	}
	if len(body) > MaxAgentBytes {
		return Agent{}, fmt.Errorf("%w: yaml exceeds %d bytes", ErrValidation, MaxAgentBytes)
	}

	// Built-ins are not editable through this API. If a row with this name
	// already exists as a builtin, we refuse — the user must pick a new name
	// to fork it.
	l.mu.RLock()
	existing, exists := l.agents[spec.Name]
	l.mu.RUnlock()
	if exists && existing.Source == SourceBuiltin {
		return Agent{}, fmt.Errorf("%w: %q is a built-in; copy it under a new name to customize", ErrBuiltinReadOnly, spec.Name)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := l.db.ExecContext(ctx, `INSERT INTO agents (name, source, yaml_body, created_at, updated_at)
        VALUES (?, 'custom', ?, ?, ?)
        ON CONFLICT(name) DO UPDATE SET yaml_body=excluded.yaml_body, updated_at=excluded.updated_at`,
		spec.Name, string(body), now, now); err != nil {
		return Agent{}, fmt.Errorf("upsert agent: %w", err)
	}

	spec.Source = SourceCustom
	spec.LoadedAt = time.Now()
	l.mu.Lock()
	l.agents[spec.Name] = &spec
	l.mu.Unlock()
	return spec, nil
}

// RemoveAgent deletes a custom agent. Built-ins refuse; agents referenced
// by any assembly line refuse with ErrInUse.
func (l *Library) RemoveAgent(ctx context.Context, name string) error {
	l.mu.RLock()
	a, ok := l.agents[name]
	if !ok {
		l.mu.RUnlock()
		return ErrNotFound
	}
	if a.Source == SourceBuiltin {
		l.mu.RUnlock()
		return ErrBuiltinReadOnly
	}
	for _, w := range l.assemblyLines {
		for _, s := range w.Stages {
			if s.Agent == name {
				l.mu.RUnlock()
				return fmt.Errorf("%w: assembly line %q references agent %q", ErrInUse, w.Name, name)
			}
		}
	}
	l.mu.RUnlock()
	if _, err := l.db.ExecContext(ctx, `DELETE FROM agents WHERE name=? AND source='custom'`, name); err != nil {
		return fmt.Errorf("delete agent: %w", err)
	}
	l.mu.Lock()
	delete(l.agents, name)
	l.mu.Unlock()
	return nil
}

// PutAssemblyLine writes a custom assembly line.
func (l *Library) PutAssemblyLine(ctx context.Context, spec AssemblyLine, body []byte) (AssemblyLine, error) {
	if len(body) > 0 {
		parsed, err := ParseAssemblyLineYAML(body)
		if err != nil {
			return AssemblyLine{}, err
		}
		if spec.Name != "" && parsed.Name != "" && parsed.Name != spec.Name {
			return AssemblyLine{}, fmt.Errorf("%w: body name %q != path name %q", ErrValidation, parsed.Name, spec.Name)
		}
		spec = *parsed
	}
	if err := validateAssemblyLine(&spec); err != nil {
		return AssemblyLine{}, err
	}
	l.mu.RLock()
	for _, st := range spec.Stages {
		if _, ok := l.agents[st.Agent]; !ok {
			l.mu.RUnlock()
			return AssemblyLine{}, fmt.Errorf("%w: agent %q is not defined", ErrValidation, st.Agent)
		}
	}
	existing, exists := l.assemblyLines[spec.Name]
	l.mu.RUnlock()
	if exists && existing.Source == SourceBuiltin {
		return AssemblyLine{}, fmt.Errorf("%w: %q is a built-in; copy it under a new name to customize", ErrBuiltinReadOnly, spec.Name)
	}
	if len(body) == 0 {
		b, err := yaml.Marshal(&spec)
		if err != nil {
			return AssemblyLine{}, fmt.Errorf("yaml marshal: %w", err)
		}
		body = b
	}
	if len(body) > MaxAssemblyLineBytes {
		return AssemblyLine{}, fmt.Errorf("%w: yaml exceeds %d bytes", ErrValidation, MaxAssemblyLineBytes)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := l.db.ExecContext(ctx, `INSERT INTO assembly_lines (name, source, yaml_body, created_at, updated_at)
        VALUES (?, 'custom', ?, ?, ?)
        ON CONFLICT(name) DO UPDATE SET yaml_body=excluded.yaml_body, updated_at=excluded.updated_at`,
		spec.Name, string(body), now, now); err != nil {
		return AssemblyLine{}, fmt.Errorf("upsert assembly_line: %w", err)
	}

	spec.Source = SourceCustom
	spec.LoadedAt = time.Now()
	l.mu.Lock()
	l.assemblyLines[spec.Name] = &spec
	l.mu.Unlock()
	return spec, nil
}

// RemoveAssemblyLine deletes a custom assembly line.
func (l *Library) RemoveAssemblyLine(ctx context.Context, name string) error {
	l.mu.RLock()
	w, ok := l.assemblyLines[name]
	if !ok {
		l.mu.RUnlock()
		return ErrNotFound
	}
	if w.Source == SourceBuiltin {
		l.mu.RUnlock()
		return ErrBuiltinReadOnly
	}
	l.mu.RUnlock()
	if _, err := l.db.ExecContext(ctx, `DELETE FROM assembly_lines WHERE name=? AND source='custom'`, name); err != nil {
		return fmt.Errorf("delete assembly_line: %w", err)
	}
	l.mu.Lock()
	delete(l.assemblyLines, name)
	l.mu.Unlock()
	return nil
}

// YAMLForAgent returns the raw stored YAML body, useful for `agentctl agent
// show` which wants the canonical text not a re-serialized form.
func (l *Library) YAMLForAgent(ctx context.Context, name string) ([]byte, error) {
	var body string
	err := l.db.QueryRowContext(ctx,
		`SELECT yaml_body FROM agents WHERE name=?`, name).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return []byte(body), nil
}

// YAMLForAssemblyLine returns the raw stored YAML body.
func (l *Library) YAMLForAssemblyLine(ctx context.Context, name string) ([]byte, error) {
	var body string
	err := l.db.QueryRowContext(ctx,
		`SELECT yaml_body FROM assembly_lines WHERE name=?`, name).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return []byte(body), nil
}
