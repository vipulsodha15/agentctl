package proto

import (
	"encoding/json"
	"time"
)

const ProtocolVersion = 1

const (
	KindRequest     = "request"
	KindResponse    = "response"
	KindEvent       = "event"
	KindStreamChunk = "stream_chunk"
	KindStreamEnd   = "stream_end"
	KindError       = "error"
)

const (
	OpHealth              = "Health"
	OpCreateSession       = "CreateSession"
	OpListSessions        = "ListSessions"
	OpGetSession          = "GetSession"
	OpSendMessage         = "SendMessage"
	OpInterrupt           = "Interrupt"
	OpAttachStream        = "AttachStream"
	OpTerminateSession    = "TerminateSession"
	OpRestartSession      = "RestartSession"
	OpGetLogs             = "GetLogs"
	OpGetContainerLogs    = "GetContainerLogs"
	OpListMCPs            = "ListMCPs"
	OpAddMCP              = "AddMCP"
	OpUpdateMCP           = "UpdateMCP"
	OpRemoveMCP           = "RemoveMCP"
	OpSetDefaultMCP       = "SetDefaultMCP"
	OpListInstalledSkills = "ListInstalledSkills"
	OpAddSkill            = "AddSkill"
	OpRemoveSkill         = "RemoveSkill"
	OpImportSkill         = "ImportSkill"
	OpExportSkill         = "ExportSkill"
	OpValidateSkill       = "ValidateSkill"
	OpGetCost             = "GetCost"
	OpDiff                = "Diff"
	OpExportPatch         = "ExportPatch"
	OpExportPush          = "ExportPush"
	OpListSessionRepos    = "ListSessionRepos"
	// OpUpdateSession applies mid-session mutations (ADR 0020 §2 / §4 —
	// today: model swap only). The request body shape lives in
	// UpdateSessionRequest; the response carries the post-update
	// SessionSummary so callers can echo the canonical model id back to
	// the user.
	OpUpdateSession = "UpdateSession"
)

type Frame struct {
	V    int             `json:"v"`
	ID   string          `json:"id"`
	Kind string          `json:"kind"`
	Op   string          `json:"op,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

type ErrorData struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details,omitempty"`
}

const (
	ErrBadRequest         = "bad_request"
	ErrNotFound           = "not_found"
	ErrConflict           = "conflict"
	ErrPreconditionFailed = "precondition_failed"
	ErrUnauthenticated    = "unauthenticated"
	ErrForbidden          = "forbidden"
	ErrUnavailable        = "unavailable"
	ErrRateLimited        = "rate_limited"
	ErrSnapshotFailed     = "snapshot_failed"
	ErrInternal           = "internal"
	ErrRuntimeError       = "runtime_error"
	ErrVersionMismatch    = "version_mismatch"
)

type HealthRequest struct{}

type HealthResponse struct {
	OK          bool         `json:"ok"`
	Version     string       `json:"version"`
	Build       string       `json:"build"`
	Reconciling bool         `json:"reconciling"`
	Docker      DockerHealth `json:"docker"`
	UptimeS     int64        `json:"uptime_s"`
}

type DockerHealth struct {
	OK      bool   `json:"ok"`
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
}

type CreateSessionRequest struct {
	Name        string   `json:"name,omitempty"`
	MCPs        []string `json:"mcps,omitempty"`
	ExcludeMCPs []string `json:"exclude_mcps,omitempty"`
	Repos       []string `json:"repos,omitempty"`
	Model       string   `json:"model,omitempty"`
	// Provider is the agent runtime the session runs on (`anthropic` or
	// `openai`). When empty the daemon's resolver picks one — see
	// secrets.ResolveProvider and ADR 0020 §3. Set-once at create; never
	// mutated afterward.
	Provider      string  `json:"provider,omitempty"`
	MemLimitBytes int64   `json:"mem_limit_bytes,omitempty"`
	CPULimitCores float64 `json:"cpu_limit_cores,omitempty"`
}

type CreateSessionResponse struct {
	SessionID string         `json:"session_id"`
	Status    string         `json:"status"`
	WebURL    string         `json:"web_url"`
	Attach    AttachPointer  `json:"attach"`
	Session   SessionSummary `json:"session"`
}

type AttachPointer struct {
	StreamOp string `json:"stream_op"`
}

type SendMessageRequest struct {
	SessionID      string `json:"session_id"`
	Content        string `json:"content"`
	ClientID       string `json:"client_id,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

type SendMessageResponse struct {
	MessageID  string `json:"message_id"`
	Queued     bool   `json:"queued"`
	QueueDepth int    `json:"queue_depth"`
	Idempotent bool   `json:"idempotent,omitempty"`
}

type InterruptRequest struct {
	SessionID  string `json:"session_id"`
	ClearQueue bool   `json:"clear_queue,omitempty"`
}

type InterruptResponse struct {
	Interrupted       bool `json:"interrupted"`
	ClearedQueueDepth int  `json:"cleared_queue_depth"`
}

type AttachStreamRequest struct {
	SessionID string `json:"session_id"`
}

type ListSessionsRequest struct{}

type ListSessionsResponse struct {
	Sessions []SessionSummary `json:"sessions"`
}

type GetSessionRequest struct {
	SessionID string `json:"session_id"`
}

type GetSessionResponse struct {
	Session SessionDetail `json:"session"`
}

// UpdateSessionRequest is the CLI/RPC mirror of the web's
// PATCH /v1/sessions/<id> body. The only field today is `model` — the
// mid-session model switch added in ADR 0020 §2 — but the struct is named
// generically so future mutable fields (e.g. caller-supplied display name)
// land here without proliferating ops.
type UpdateSessionRequest struct {
	SessionID string  `json:"session_id"`
	Model     *string `json:"model,omitempty"`
}

// UpdateSessionResponse returns the post-update summary so the CLI can
// echo the canonical model id (in case the resolver normalized it, e.g.
// a future fuzzy-match step) back to the user.
type UpdateSessionResponse struct {
	Session SessionSummary `json:"session"`
}

type TerminateSessionRequest struct {
	SessionID string `json:"session_id"`
	Force     bool   `json:"force,omitempty"`
}

type TerminateSessionResponse struct {
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
}

type RestartSessionRequest struct {
	SessionID string `json:"session_id"`
}

type RestartSessionResponse struct {
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
	ImageID   string `json:"image_id"`
}

type GetLogsRequest struct {
	SessionID string `json:"session_id"`
	Follow    bool   `json:"follow,omitempty"`
	SinceMS   int64  `json:"since_ms,omitempty"`
}

type GetContainerLogsRequest struct {
	SessionID string `json:"session_id"`
	Follow    bool   `json:"follow,omitempty"`
}

type SessionSummary struct {
	ID             string    `json:"session_id"`
	Name           string    `json:"name"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
	LastActivityAt time.Time `json:"last_activity_at"`
	ImageID        string    `json:"image_id"`
	Model          string    `json:"model"`
	// Provider is the agent runtime backing this session — `anthropic` or
	// `openai`. Set-once at create per ADR 0020 §1. Older clients that
	// don't know about provider get the empty string back; the web SPA and
	// CLI renderer both treat "" as `anthropic` for one release.
	Provider      string   `json:"provider,omitempty"`
	MCPs          []string `json:"mcps"`
	Repos         []string `json:"repos"`
	InFlight      bool     `json:"in_flight"`
	QueueDepth    int      `json:"queue_depth"`
	MemLimitBytes int64    `json:"mem_limit_bytes"`
	CPULimitCores float64  `json:"cpu_limit_cores"`
	CostUSD       *float64 `json:"cost_usd,omitempty"`
}

type SessionDetail struct {
	SessionSummary
	ContainerID  string            `json:"container_id,omitempty"`
	NetworkID    string            `json:"network_id,omitempty"`
	VolumePath   string            `json:"volume_path,omitempty"`
	MCPStatus    map[string]string `json:"mcp_status,omitempty"`
	SDKSessionID string            `json:"sdk_session_id,omitempty"`
	SkillsHash   string            `json:"skills_snapshot_hash,omitempty"`
	LastError    string            `json:"last_error,omitempty"`
}

type Event struct {
	EventID   string          `json:"event_id"`
	Kind      string          `json:"kind"`
	SessionID string          `json:"session_id"`
	TS        time.Time       `json:"ts"`
	Data      json.RawMessage `json:"data,omitempty"`
}

const (
	EventSessionSnapshot   = "session.snapshot"
	EventSessionStarting   = "session.starting"
	EventSessionRunning    = "session.running"
	EventSessionStopping   = "session.stopping"
	EventSessionStopped    = "session.stopped"
	EventSessionResumed    = "session.resumed"
	EventSessionTerminated = "session.terminated"
	EventSessionError      = "session.error"
	EventTurnStart         = "turn.start"
	EventTurnEnd           = "turn.end"
	EventTurnCancelled     = "turn.cancelled"
	EventAssistantDelta    = "assistant.delta"
	EventAssistantMessage  = "assistant.message"
	EventToolCall          = "tool.call"
	EventToolResult        = "tool.result"
	EventUserMessage       = "user.message"
	EventUsage             = "usage"
	EventQueueDepth        = "queue.depth"
	EventRepoChanged       = "repo.changed"
	EventSkillsChanged     = "skills.changed"
	EventSkillCollision    = "skill.collision"
	EventRuntimeThrottled  = "runtime.throttled"
	EventLogLine           = "log.line"
	EventMCPUnreachable    = "mcp.unreachable"
	EventMCPSkipped        = "mcp.skipped"
)

type SessionSnapshotData struct {
	Session      SessionSummary    `json:"session"`
	Conversation json.RawMessage   `json:"conversation"`
	QueueDepth   int               `json:"queue_depth"`
	InFlight     string            `json:"in_flight,omitempty"`
	MCPsStatus   map[string]string `json:"mcps_status,omitempty"`
	Repos        []RepoState       `json:"repos,omitempty"`
}

type RepoState struct {
	Name    string `json:"name"`
	URL     string `json:"url,omitempty"`
	BaseSHA string `json:"base_sha,omitempty"`
	Branch  string `json:"branch,omitempty"`
}

type TurnStartData struct {
	TurnID    string `json:"turn_id"`
	MessageID string `json:"message_id"`
	Model     string `json:"model,omitempty"`
}

type TurnEndData struct {
	TurnID string `json:"turn_id"`
	Status string `json:"status"`
}

type TurnCancelledData struct {
	TurnID string `json:"turn_id"`
	Reason string `json:"reason"`
}

type AssistantDeltaData struct {
	TurnID string `json:"turn_id"`
	Delta  string `json:"delta"`
}

type AssistantMessageData struct {
	TurnID  string `json:"turn_id"`
	Content string `json:"content"`
}

type ToolCallData struct {
	TurnID    string          `json:"turn_id"`
	Tool      string          `json:"tool"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
}

type ToolResultData struct {
	TurnID    string `json:"turn_id"`
	Tool      string `json:"tool"`
	ToolUseID string `json:"tool_use_id,omitempty"`
	// Shim emits the body as `content`; older daemons used `output`. Accept
	// both so a shim/agentd version skew doesn't blank the result panel.
	Output  json.RawMessage `json:"output,omitempty"`
	Content json.RawMessage `json:"content,omitempty"`
	IsError bool            `json:"is_error"`
}

type UserMessageData struct {
	MessageID string `json:"message_id"`
	Content   string `json:"content"`
	ClientID  string `json:"client_id,omitempty"`
}

type UsageData struct {
	TurnID           string  `json:"turn_id"`
	Model            string  `json:"model"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	CostUSD          float64 `json:"cost_usd,omitempty"`
}

type QueueDepthData struct {
	Depth int `json:"depth"`
}

type SessionStoppedData struct {
	Reason   string `json:"reason"`
	ExitCode int    `json:"exit_code,omitempty"`
}

type LogLineData struct {
	Raw string `json:"raw"`
}

type SkillCollisionData struct {
	Name      string `json:"name"`
	Overrides string `json:"overrides"`
}

type MCPEntry struct {
	Name           string    `json:"name"`
	URL            string    `json:"url"`
	Transport      string    `json:"transport"`
	Kind           string    `json:"kind"`
	AuthConfigJSON string    `json:"auth_config_json,omitempty"`
	DefaultEnabled bool      `json:"default_enabled"`
	Description    string    `json:"description,omitempty"`
	CreatedAt      time.Time `json:"created_at,omitempty"`
	UpdatedAt      time.Time `json:"updated_at,omitempty"`
}

type ListMCPsRequest struct{}

type ListMCPsResponse struct {
	MCPs []MCPEntry `json:"mcps"`
}

type AddMCPRequest struct {
	Name           string `json:"name"`
	URL            string `json:"url"`
	Transport      string `json:"transport,omitempty"`
	Kind           string `json:"kind,omitempty"`
	AuthConfigJSON string `json:"auth_config_json,omitempty"`
	DefaultEnabled bool   `json:"default_enabled,omitempty"`
	Description    string `json:"description,omitempty"`
}

type AddMCPResponse struct {
	MCP MCPEntry `json:"mcp"`
}

type UpdateMCPRequest struct {
	Name           string  `json:"name"`
	URL            *string `json:"url,omitempty"`
	Transport      *string `json:"transport,omitempty"`
	Kind           *string `json:"kind,omitempty"`
	AuthConfigJSON *string `json:"auth_config_json,omitempty"`
	DefaultEnabled *bool   `json:"default_enabled,omitempty"`
	Description    *string `json:"description,omitempty"`
}

type UpdateMCPResponse struct {
	MCP MCPEntry `json:"mcp"`
}

type RemoveMCPRequest struct {
	Name  string `json:"name"`
	Force bool   `json:"force,omitempty"`
}

type RemoveMCPResponse struct {
	Removed bool `json:"removed"`
}

type SetDefaultMCPRequest struct {
	Name           string `json:"name"`
	DefaultEnabled bool   `json:"default_enabled"`
}

type SetDefaultMCPResponse struct {
	MCP MCPEntry `json:"mcp"`
}

type SkillEntry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
	Path        string `json:"path,omitempty"`
	Overrides   bool   `json:"overrides,omitempty"`
}

type ListInstalledSkillsRequest struct{}

type ListInstalledSkillsResponse struct {
	Skills []SkillEntry `json:"skills"`
}

type ImportSkillRequest struct {
	SourcePath string `json:"source_path"`
	Name       string `json:"name,omitempty"`
	Force      bool   `json:"force,omitempty"`
	DryRun     bool   `json:"dry_run,omitempty"`
}

type ImportSkillResponse struct {
	Imported       []string `json:"imported"`
	Skipped        []string `json:"skipped,omitempty"`
	SkippedReasons []string `json:"skipped_reasons,omitempty"`
	Shadowed       []string `json:"shadowed_builtins,omitempty"`
}

type AddSkillRequest struct {
	Path  string `json:"path"`
	Force bool   `json:"force,omitempty"`
}

type AddSkillResponse struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type RemoveSkillRequest struct {
	Name string `json:"name"`
}

type RemoveSkillResponse struct {
	Removed bool `json:"removed"`
}

type ValidateSkillRequest struct {
	Name string `json:"name,omitempty"`
	Path string `json:"path,omitempty"`
}

type ValidateSkillResponse struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	OK          bool     `json:"ok"`
	Issues      []string `json:"issues,omitempty"`
}

type ExportSkillRequest struct {
	Name string `json:"name"`
}

type ExportSkillResponse struct {
	Name    string `json:"name"`
	Tarball []byte `json:"tarball"`
}

type GetCostRequest struct {
	SessionID string `json:"session_id,omitempty"`
	Since     string `json:"since,omitempty"`
}

type CostModelTotals struct {
	Model            string  `json:"model"`
	Turns            int     `json:"turns"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	CostUSD          float64 `json:"cost_usd"`
	HasUnknown       bool    `json:"has_unknown_model,omitempty"`
}

type CostTurnRow struct {
	TurnID           string    `json:"turn_id"`
	At               time.Time `json:"at"`
	Model            string    `json:"model"`
	InputTokens      int64     `json:"input_tokens"`
	OutputTokens     int64     `json:"output_tokens"`
	CacheReadTokens  int64     `json:"cache_read_tokens"`
	CacheWriteTokens int64     `json:"cache_write_tokens"`
	CostUSD          *float64  `json:"cost_usd,omitempty"`
}

type SessionCostTotals struct {
	SessionID        string            `json:"session_id"`
	Turns            int               `json:"turns"`
	InputTokens      int64             `json:"input_tokens"`
	OutputTokens     int64             `json:"output_tokens"`
	CacheReadTokens  int64             `json:"cache_read_tokens"`
	CacheWriteTokens int64             `json:"cache_write_tokens"`
	CostUSD          float64           `json:"cost_usd"`
	HasUnknown       bool              `json:"has_unknown_model,omitempty"`
	ByModel          []CostModelTotals `json:"by_model"`
	Timeline         []CostTurnRow     `json:"timeline"`
}

type RangeSessionTotals struct {
	SessionID    string  `json:"session_id"`
	Name         string  `json:"name,omitempty"`
	Status       string  `json:"status,omitempty"`
	Turns        int     `json:"turns"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	HasUnknown   bool    `json:"has_unknown_model,omitempty"`
}

type RangeCostTotals struct {
	Start        time.Time            `json:"start"`
	End          time.Time            `json:"end"`
	Turns        int                  `json:"turns"`
	InputTokens  int64                `json:"input_tokens"`
	OutputTokens int64                `json:"output_tokens"`
	CostUSD      float64              `json:"cost_usd"`
	HasUnknown   bool                 `json:"has_unknown_model,omitempty"`
	BySession    []RangeSessionTotals `json:"by_session"`
}

type GetCostResponse struct {
	PerSession *SessionCostTotals `json:"per_session,omitempty"`
	Range      *RangeCostTotals   `json:"range,omitempty"`
}

type DiffRequest struct {
	SessionID string `json:"session_id"`
	Repo      string `json:"repo,omitempty"`
	Format    string `json:"format,omitempty"`
}

type DiffChunkData struct {
	Repo     string `json:"repo,omitempty"`
	Data     []byte `json:"data,omitempty"`
	End      bool   `json:"end,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`
	BaseSHA  string `json:"base_sha,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Note     string `json:"note,omitempty"`
	Error    string `json:"error,omitempty"`
}

type ExportPatchRequest struct {
	SessionID string `json:"session_id,omitempty"`
	Repo      string `json:"repo,omitempty"`
}

type ExportPushRequest struct {
	SessionID string `json:"session_id,omitempty"`
	Repo      string `json:"repo,omitempty"`
	Branch    string `json:"branch"`
	Message   string `json:"message,omitempty"`
}

type ExportPushResponse struct {
	Success bool   `json:"success"`
	Repo    string `json:"repo,omitempty"`
	Branch  string `json:"branch,omitempty"`
	Output  string `json:"output,omitempty"`
	Error   string `json:"error,omitempty"`
}

type ListSessionReposRequest struct {
	SessionID string `json:"session_id"`
}

type ListSessionReposResponse struct {
	Repos []RepoState `json:"repos"`
}

type MCPSkippedData struct {
	Name      string `json:"name"`
	Transport string `json:"transport"`
	Kind      string `json:"kind"`
	Reason    string `json:"reason"`
}

type MCPUnreachableData struct {
	Name      string `json:"name"`
	Transport string `json:"transport"`
	Error     string `json:"error"`
}
