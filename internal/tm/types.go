// Package tm is the task manager — assembly-lines-task-management-architecture.md §9.1.
//
// It owns the task / stage state machine and persists it to sqlite. Each
// stage is backed by exactly one session created through the existing sm
// machinery; this module composes sessions, it does not replace them.
package tm

import (
	"errors"
	"time"
)

const (
	TaskStatusNotStarted = "not-started"
	TaskStatusWorking    = "working"
	TaskStatusDone       = "done"
	TaskStatusAbandoned  = "abandoned"

	StageStatusPending = "pending"
	StageStatusActive  = "active"
	StageStatusDone    = "done"

	SourceGithubIssue = "github_issue"
	SourceFreeform    = "freeform"

	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleSystem    = "system"
	RoleSeam      = "seam"
	RoleSynthesis = "synthesis"
	RoleError     = "error"
)

// Task is the in-memory view of a task row joined with its stages.
type Task struct {
	ID               string    `json:"task_id"`
	Name             string    `json:"name"`
	AssemblyLineName string    `json:"assembly_line_name,omitempty"`
	RepoURL          string    `json:"repo_url,omitempty"`
	BaseSHA          string    `json:"base_sha,omitempty"`
	SourceKind       string    `json:"source_kind"`
	SourceURL        string    `json:"source_url,omitempty"`
	IssueMD          string    `json:"issue_md"`
	CurrentStageID   string    `json:"current_stage_id,omitempty"`
	Status           string    `json:"status"`
	CreatedAt        time.Time `json:"created_at"`
	StartedAt        time.Time `json:"started_at,omitzero"`
	EndedAt          time.Time `json:"ended_at,omitzero"`
	Stages           []Stage   `json:"stages,omitempty"`
}

type Stage struct {
	ID         string `json:"stage_id"`
	TaskID     string `json:"task_id"`
	Position   int    `json:"position"`
	AgentName  string `json:"agent_name"`
	Colour     string `json:"colour,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	VolumeName string `json:"volume_name,omitempty"`
	Synthesis  string `json:"synthesis,omitempty"`
	Status     string `json:"status"`
	// Provider and Model surface the runtime identity the stage's session
	// is running on (or did run on, for done stages). Joined from the
	// sessions table in loadStages — the run-view UI uses these to
	// render the per-stage provider/model chip introduced in ADR 0020 §3
	// (orchestration as the headline). Empty when the stage hasn't been
	// spawned yet or the session row has been GC'd.
	Provider  string    `json:"provider,omitempty"`
	Model     string    `json:"model,omitempty"`
	StartedAt time.Time `json:"started_at,omitzero"`
	EndedAt   time.Time `json:"ended_at,omitzero"`
}

// Message is the row form for an entry in task_messages.
type Message struct {
	TaskID    string    `json:"task_id"`
	Seq       int64     `json:"seq"`
	StageID   string    `json:"stage_id,omitempty"`
	AgentName string    `json:"agent_name,omitempty"`
	At        time.Time `json:"at"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
}

// CreateTaskRequest is the input to NewTask. Either AssemblyLineName or
// AgentName may be set (not both): AssemblyLineName runs the named
// multi-stage assembly line, AgentName runs a single-stage task with just
// that agent.
type CreateTaskRequest struct {
	Name             string `json:"name,omitempty"`
	AssemblyLineName string `json:"assembly_line_name,omitempty"`
	AgentName        string `json:"agent_name,omitempty"`
	RepoURL          string `json:"repo_url,omitempty"`
	SourceKind       string `json:"source_kind,omitempty"`
	SourceURL        string `json:"source_url,omitempty"`
	IssueMD          string `json:"issue_md"`
}

type SendMessageRequest struct {
	TaskID  string `json:"task_id"`
	Content string `json:"content"`
}

// Errors surfaced over the API.
var (
	ErrTaskNotFound         = errors.New("tm: task not found")
	ErrAgentNotFound        = errors.New("tm: agent not found")
	ErrAssemblyLineNotFound = errors.New("tm: assembly line not found")
	ErrPreconditionFailed   = errors.New("tm: precondition failed")
	ErrTerminal             = errors.New("tm: task is terminal")
	ErrStageBusy            = errors.New("tm: stage is busy")
	ErrValidation           = errors.New("tm: validation failed")
	ErrSourceUnreachable    = errors.New("tm: source unreachable")
)
