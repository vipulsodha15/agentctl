// Package ttl ("task library") is the in-memory index of Agent and Workflow
// YAML definitions on disk. See workflows-task-management-architecture.md §9.4.
//
// Agents and workflows are file-backed: each lives in its own YAML file under
// ~/.local/share/agentctl/{agents,workflows}/<name>.yaml. ttl loads the
// directories at startup and watches them for changes; mutation methods (Put,
// Remove) update both the on-disk file and the in-memory index in lockstep.
package ttl

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	MaxAgentBytes    = 16 * 1024
	MaxWorkflowBytes = 16 * 1024
	MaxStages        = 16

	SourceBuiltin = "builtin"
	SourceCustom  = "custom"
)

// validColours mirrors workflows-task-management.md §R1 stage palette + §R5.
var validColours = map[string]bool{
	"blue": true, "purple": true, "green": true,
	"amber": true, "red": true, "slate": true,
}

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

var (
	ErrNotFound        = errors.New("ttl: not found")
	ErrAlreadyExists   = errors.New("ttl: already exists")
	ErrBuiltinReadOnly = errors.New("ttl: built-in is read-only")
	ErrValidation      = errors.New("ttl: validation failed")
	ErrInUse           = errors.New("ttl: in use")
)

// Agent is the on-disk schema for an agent. See arch §4.3.
type Agent struct {
	Name          string   `yaml:"name" json:"name"`
	Description   string   `yaml:"description" json:"description"`
	Colour        string   `yaml:"colour" json:"colour"`
	Model         string   `yaml:"model,omitempty" json:"model,omitempty"`
	Prompt        string   `yaml:"prompt" json:"prompt"`
	MCPsAllowed   []string `yaml:"mcps_allowed,omitempty" json:"mcps_allowed,omitempty"`
	SkillsAllowed []string `yaml:"skills_allowed,omitempty" json:"skills_allowed,omitempty"`

	// Source is "builtin" or "custom"; not serialized into YAML files.
	Source string `yaml:"-" json:"source,omitempty"`
	// Path is the file path the agent was loaded from.
	Path string `yaml:"-" json:"path,omitempty"`
	// LoadedAt is set by ttl on load.
	LoadedAt time.Time `yaml:"-" json:"loaded_at,omitempty"`
}

// WorkflowStage is one entry in a workflow's `stages:` list.
type WorkflowStage struct {
	Agent string `yaml:"agent" json:"agent"`
}

// Workflow is the on-disk schema for a workflow. See arch §4.3.
type Workflow struct {
	Name        string          `yaml:"name" json:"name"`
	Description string          `yaml:"description" json:"description"`
	Stages      []WorkflowStage `yaml:"stages" json:"stages"`

	Source   string    `yaml:"-" json:"source,omitempty"`
	Path     string    `yaml:"-" json:"path,omitempty"`
	LoadedAt time.Time `yaml:"-" json:"loaded_at,omitempty"`
}

// Library indexes agents and workflows.
type Library struct {
	builtinAgents    string
	customAgents     string
	builtinWorkflows string
	customWorkflows  string

	mu        sync.RWMutex
	agents    map[string]*Agent
	workflows map[string]*Workflow
}

type Options struct {
	BuiltinAgentsDir    string
	CustomAgentsDir     string
	BuiltinWorkflowsDir string
	CustomWorkflowsDir  string
}

func New(opts Options) *Library {
	return &Library{
		builtinAgents:    opts.BuiltinAgentsDir,
		customAgents:     opts.CustomAgentsDir,
		builtinWorkflows: opts.BuiltinWorkflowsDir,
		customWorkflows:  opts.CustomWorkflowsDir,
		agents:           map[string]*Agent{},
		workflows:        map[string]*Workflow{},
	}
}

// Load (re)reads both agent and workflow directories from disk. Errors from
// individual files are logged via the issues slice — they do not abort the
// whole load, mirroring the skills library's permissive posture.
type LoadIssues struct {
	AgentErrors    map[string]string
	WorkflowErrors map[string]string
}

func (l *Library) Load() (LoadIssues, error) {
	issues := LoadIssues{
		AgentErrors:    map[string]string{},
		WorkflowErrors: map[string]string{},
	}
	agents, agentErrs := loadAgentsDir(l.builtinAgents, SourceBuiltin)
	customAgents, customAgentErrs := loadAgentsDir(l.customAgents, SourceCustom)
	for n, e := range agentErrs {
		issues.AgentErrors[n] = e
	}
	for n, e := range customAgentErrs {
		issues.AgentErrors[n] = e
	}
	// Custom overrides built-in if names collide.
	for _, a := range customAgents {
		agents[a.Name] = a
	}

	workflows, wfErrs := loadWorkflowsDir(l.builtinWorkflows, SourceBuiltin)
	customWorkflows, customWfErrs := loadWorkflowsDir(l.customWorkflows, SourceCustom)
	for n, e := range wfErrs {
		issues.WorkflowErrors[n] = e
	}
	for n, e := range customWfErrs {
		issues.WorkflowErrors[n] = e
	}
	for _, w := range customWorkflows {
		workflows[w.Name] = w
	}

	// Cross-reference: workflows that name a missing agent are loaded but
	// flagged; we do not drop them so the UI can render the validation
	// error instead of silently hiding them.
	for name, w := range workflows {
		for _, st := range w.Stages {
			if _, ok := agents[st.Agent]; !ok {
				issues.WorkflowErrors[name] = fmt.Sprintf("references missing agent %q", st.Agent)
			}
		}
	}

	l.mu.Lock()
	l.agents = agents
	l.workflows = workflows
	l.mu.Unlock()
	return issues, nil
}

func loadAgentsDir(dir, source string) (map[string]*Agent, map[string]string) {
	out := map[string]*Agent{}
	errs := map[string]string{}
	if dir == "" {
		return out, errs
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			errs["__dir__"] = err.Error()
		}
		return out, errs
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		a, err := readAgentFile(path)
		stem := strings.TrimSuffix(e.Name(), ".yaml")
		if err != nil {
			errs[stem] = err.Error()
			continue
		}
		a.Source = source
		a.Path = path
		a.LoadedAt = time.Now()
		out[a.Name] = a
	}
	return out, errs
}

func loadWorkflowsDir(dir, source string) (map[string]*Workflow, map[string]string) {
	out := map[string]*Workflow{}
	errs := map[string]string{}
	if dir == "" {
		return out, errs
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			errs["__dir__"] = err.Error()
		}
		return out, errs
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		w, err := readWorkflowFile(path)
		stem := strings.TrimSuffix(e.Name(), ".yaml")
		if err != nil {
			errs[stem] = err.Error()
			continue
		}
		w.Source = source
		w.Path = path
		w.LoadedAt = time.Now()
		out[w.Name] = w
	}
	return out, errs
}

func readAgentFile(path string) (*Agent, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > MaxAgentBytes {
		return nil, fmt.Errorf("agent yaml exceeds %d bytes", MaxAgentBytes)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	a, err := ParseAgentYAML(body)
	if err != nil {
		return nil, err
	}
	stem := strings.TrimSuffix(filepath.Base(path), ".yaml")
	if a.Name != stem {
		return nil, fmt.Errorf("name %q must match filename stem %q", a.Name, stem)
	}
	if err := validateAgent(a); err != nil {
		return nil, err
	}
	return a, nil
}

func readWorkflowFile(path string) (*Workflow, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > MaxWorkflowBytes {
		return nil, fmt.Errorf("workflow yaml exceeds %d bytes", MaxWorkflowBytes)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	w, err := ParseWorkflowYAML(body)
	if err != nil {
		return nil, err
	}
	stem := strings.TrimSuffix(filepath.Base(path), ".yaml")
	if w.Name != stem {
		return nil, fmt.Errorf("name %q must match filename stem %q", w.Name, stem)
	}
	if err := validateWorkflow(w); err != nil {
		return nil, err
	}
	return w, nil
}

// ParseAgentYAML decodes raw YAML bytes into an *Agent without any disk-side
// validation (filename match, path). Used by Put when accepting bodies over
// the API.
func ParseAgentYAML(body []byte) (*Agent, error) {
	var a Agent
	if err := yaml.Unmarshal(body, &a); err != nil {
		return nil, fmt.Errorf("yaml: %w", err)
	}
	return &a, nil
}

// ParseWorkflowYAML decodes raw YAML bytes into a *Workflow.
func ParseWorkflowYAML(body []byte) (*Workflow, error) {
	var w Workflow
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

func validateWorkflow(w *Workflow) error {
	if w.Name == "" {
		return fmt.Errorf("%w: workflow name is required", ErrValidation)
	}
	if !nameRe.MatchString(w.Name) {
		return fmt.Errorf("%w: name %q must match [a-z][a-z0-9-]{0,62}", ErrValidation, w.Name)
	}
	if w.Description == "" {
		return fmt.Errorf("%w: description is required", ErrValidation)
	}
	if len(w.Stages) == 0 {
		return fmt.Errorf("%w: workflow needs at least one stage", ErrValidation)
	}
	if len(w.Stages) > MaxStages {
		return fmt.Errorf("%w: workflow has more than %d stages", ErrValidation, MaxStages)
	}
	for i, s := range w.Stages {
		if s.Agent == "" {
			return fmt.Errorf("%w: stage %d has no agent", ErrValidation, i+1)
		}
	}
	return nil
}

// ListAgents returns agents sorted by name. The slice is a copy; callers may
// mutate it freely.
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

// GetAgent returns a copy of the agent by name.
func (l *Library) GetAgent(name string) (Agent, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	a, ok := l.agents[name]
	if !ok {
		return Agent{}, ErrNotFound
	}
	return *a, nil
}

func (l *Library) ListWorkflows() []Workflow {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]Workflow, 0, len(l.workflows))
	for _, w := range l.workflows {
		out = append(out, *w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (l *Library) GetWorkflow(name string) (Workflow, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	w, ok := l.workflows[name]
	if !ok {
		return Workflow{}, ErrNotFound
	}
	return *w, nil
}

// PutAgent writes a custom agent to disk and reindexes. If body is non-empty
// it overrides the in-memory spec entirely; otherwise the caller-provided
// spec is used.
func (l *Library) PutAgent(spec Agent, body []byte) (Agent, error) {
	if l.customAgents == "" {
		return Agent{}, fmt.Errorf("custom agents dir is not configured")
	}
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
	l.mu.RLock()
	existing, exists := l.agents[spec.Name]
	l.mu.RUnlock()
	if exists && existing.Source == SourceBuiltin {
		// Saving over a built-in produces a custom shadow.
	}
	if err := os.MkdirAll(l.customAgents, 0o700); err != nil {
		return Agent{}, fmt.Errorf("mkdir custom agents: %w", err)
	}
	path := filepath.Join(l.customAgents, spec.Name+".yaml")
	yamlBytes, err := yaml.Marshal(&spec)
	if err != nil {
		return Agent{}, fmt.Errorf("yaml marshal: %w", err)
	}
	if len(yamlBytes) > MaxAgentBytes {
		return Agent{}, fmt.Errorf("%w: yaml exceeds %d bytes", ErrValidation, MaxAgentBytes)
	}
	if err := writeFile(path, yamlBytes); err != nil {
		return Agent{}, err
	}
	spec.Source = SourceCustom
	spec.Path = path
	spec.LoadedAt = time.Now()
	l.mu.Lock()
	l.agents[spec.Name] = &spec
	l.mu.Unlock()
	return spec, nil
}

// RemoveAgent deletes a custom agent. Built-ins refuse.
func (l *Library) RemoveAgent(name string) error {
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
	// Refuse if any workflow references it.
	for _, w := range l.workflows {
		for _, s := range w.Stages {
			if s.Agent == name {
				l.mu.RUnlock()
				return fmt.Errorf("%w: workflow %q references agent %q", ErrInUse, w.Name, name)
			}
		}
	}
	path := a.Path
	l.mu.RUnlock()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	l.mu.Lock()
	delete(l.agents, name)
	l.mu.Unlock()
	return nil
}

// PutWorkflow writes a custom workflow.
func (l *Library) PutWorkflow(spec Workflow, body []byte) (Workflow, error) {
	if l.customWorkflows == "" {
		return Workflow{}, fmt.Errorf("custom workflows dir is not configured")
	}
	if len(body) > 0 {
		parsed, err := ParseWorkflowYAML(body)
		if err != nil {
			return Workflow{}, err
		}
		if spec.Name != "" && parsed.Name != "" && parsed.Name != spec.Name {
			return Workflow{}, fmt.Errorf("%w: body name %q != path name %q", ErrValidation, parsed.Name, spec.Name)
		}
		spec = *parsed
	}
	if err := validateWorkflow(&spec); err != nil {
		return Workflow{}, err
	}
	// Validate referenced agents exist.
	l.mu.RLock()
	for _, st := range spec.Stages {
		if _, ok := l.agents[st.Agent]; !ok {
			l.mu.RUnlock()
			return Workflow{}, fmt.Errorf("%w: agent %q is not defined", ErrValidation, st.Agent)
		}
	}
	l.mu.RUnlock()
	if err := os.MkdirAll(l.customWorkflows, 0o700); err != nil {
		return Workflow{}, fmt.Errorf("mkdir custom workflows: %w", err)
	}
	path := filepath.Join(l.customWorkflows, spec.Name+".yaml")
	yamlBytes, err := yaml.Marshal(&spec)
	if err != nil {
		return Workflow{}, fmt.Errorf("yaml marshal: %w", err)
	}
	if err := writeFile(path, yamlBytes); err != nil {
		return Workflow{}, err
	}
	spec.Source = SourceCustom
	spec.Path = path
	spec.LoadedAt = time.Now()
	l.mu.Lock()
	l.workflows[spec.Name] = &spec
	l.mu.Unlock()
	return spec, nil
}

// RemoveWorkflow deletes a custom workflow.
func (l *Library) RemoveWorkflow(name string) error {
	l.mu.RLock()
	w, ok := l.workflows[name]
	if !ok {
		l.mu.RUnlock()
		return ErrNotFound
	}
	if w.Source == SourceBuiltin {
		l.mu.RUnlock()
		return ErrBuiltinReadOnly
	}
	path := w.Path
	l.mu.RUnlock()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	l.mu.Lock()
	delete(l.workflows, name)
	l.mu.Unlock()
	return nil
}

// CopyBuiltins ensures built-in agent and workflow files from `image/builtin-*`
// directories are present in the user's builtin directories. Used by
// agentctl init (idempotent).
func CopyBuiltins(srcDir, dstDir string) (copied int, err error) {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	if err := os.MkdirAll(dstDir, 0o700); err != nil {
		return 0, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		src := filepath.Join(srcDir, e.Name())
		dst := filepath.Join(dstDir, e.Name())
		// Re-copy unconditionally on init; user customizations live under
		// custom/.
		body, err := os.ReadFile(src)
		if err != nil {
			return copied, err
		}
		if err := writeFile(dst, body); err != nil {
			return copied, err
		}
		copied++
	}
	return copied, nil
}

func writeFile(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o600)
}
