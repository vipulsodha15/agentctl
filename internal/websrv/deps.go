package websrv

import (
	"context"
	"io"

	"github.com/agentctl/agentctl/internal/fan"
	"github.com/agentctl/agentctl/internal/proto"
	"github.com/agentctl/agentctl/internal/sm"
)

// Manager is the subset of sm.Manager websrv depends on. Declared as an
// interface so the package compiles ahead of M3-B and tests can substitute
// a fake without dragging in the full session-manager surface.
type Manager interface {
	Create(ctx context.Context, req sm.CreateRequest) (sm.CreateResult, error)
	Send(ctx context.Context, req sm.SendRequest) (sm.SendResult, error)
	Interrupt(ctx context.Context, sessionID string, clearQueue bool) (sm.InterruptResult, error)
	Attach(ctx context.Context, sessionID string) (sm.Stream, error)
	List(ctx context.Context) ([]proto.SessionSummary, error)
	Get(ctx context.Context, sessionID string) (proto.SessionDetail, error)
	Terminate(ctx context.Context, sessionID string) error
	Diff(ctx context.Context, sessionID string, req sm.DiffRequest) (sm.DiffStream, error)
	ExportPatch(ctx context.Context, sessionID string, req sm.DiffRequest) (sm.DiffStream, error)
	ExportPush(ctx context.Context, sessionID string, req sm.PushRequest) (sm.PushResult, error)
	SessionRepos(ctx context.Context, sessionID string) ([]proto.RepoState, error)
}

// MCPRegistry is M3-B's territory; websrv only dispatches to it. Methods
// take and return raw JSON so the request/response shapes can evolve in
// internal/mcp without dragging websrv along.
//
// TODO(M3-B): tighten the contract to typed structs once internal/mcp lands.
type MCPRegistry interface {
	List(ctx context.Context) ([]byte, error)
	Add(ctx context.Context, body []byte) ([]byte, error)
	Update(ctx context.Context, name string, body []byte) ([]byte, error)
	Remove(ctx context.Context, name string, force bool) error
}

// SkillsService is M3-B / M4 territory; same JSON-pass-through pattern.
//
// TODO(M3-B): typed structs once internal/skills lands.
type SkillsService interface {
	ListInstalled(ctx context.Context) ([]byte, error)
	ListForSession(ctx context.Context, sessionID string) ([]byte, error)
	Add(ctx context.Context, contentType string, body io.Reader) ([]byte, error)
	Import(ctx context.Context, body []byte) ([]byte, error)
	Validate(ctx context.Context, name string) ([]byte, error)
	Export(ctx context.Context, name string, w io.Writer) error
	Remove(ctx context.Context, name string, force bool) error
}

// LogStreamer is internal/log's per-session log tail.
type LogStreamer interface {
	Stream(ctx context.Context, sessionID string, follow bool, send func(line []byte) error) error
}

// Doctor runs the install + connectivity self-test (M5 owns the full set).
type Doctor interface {
	Run(ctx context.Context) ([]byte, error)
}

// Updater is M4's update flow.
type Updater interface {
	Update(ctx context.Context, body []byte) ([]byte, error)
}

// UsageService is wired by M5-A; the handler calls into this for
// `/v1/usage`. JSON-pass-through pattern matches MCP and Skills, plus a
// typed RunningTotals fast-path used to enrich the session list with each
// session's cost-to-date.
type UsageService interface {
	GetUsage(ctx context.Context, since, sessionID string) ([]byte, error)
	RunningTotals(ctx context.Context, sessionIDs []string) (map[string]float64, error)
}

// TaskHub is the subset of fan.Hub that the task-stream handler needs.
type TaskHub interface {
	Subscribe(channel string) (fan.Stream, func(), error)
}
