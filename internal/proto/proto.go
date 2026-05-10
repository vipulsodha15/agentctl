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
	OpHealth           = "Health"
	OpCreateSession    = "CreateSession"
	OpListSessions     = "ListSessions"
	OpGetSession       = "GetSession"
	OpSendMessage      = "SendMessage"
	OpInterrupt        = "Interrupt"
	OpAttachStream     = "AttachStream"
	OpTerminateSession = "TerminateSession"
	OpGetLogs          = "GetLogs"
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
	Name          string   `json:"name,omitempty"`
	MCPs          []string `json:"mcps,omitempty"`
	ExcludeMCPs   []string `json:"exclude_mcps,omitempty"`
	Repos         []string `json:"repos,omitempty"`
	Model         string   `json:"model,omitempty"`
	MemLimitBytes int64    `json:"mem_limit_bytes,omitempty"`
	CPULimitCores float64  `json:"cpu_limit_cores,omitempty"`
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

type TerminateSessionRequest struct {
	SessionID string `json:"session_id"`
	Force     bool   `json:"force,omitempty"`
}

type TerminateSessionResponse struct {
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
}

type GetLogsRequest struct {
	SessionID string `json:"session_id"`
	Follow    bool   `json:"follow,omitempty"`
	SinceMS   int64  `json:"since_ms,omitempty"`
}

type SessionSummary struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
	LastActivityAt time.Time `json:"last_activity_at"`
	ImageID        string    `json:"image_id"`
	Model          string    `json:"model"`
	MCPs           []string  `json:"mcps"`
	Repos          []string  `json:"repos"`
	InFlight       bool      `json:"in_flight"`
	QueueDepth     int       `json:"queue_depth"`
	MemLimitBytes  int64     `json:"mem_limit_bytes"`
	CPULimitCores  float64   `json:"cpu_limit_cores"`
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
	TurnID string          `json:"turn_id"`
	Tool   string          `json:"tool"`
	Input  json.RawMessage `json:"input,omitempty"`
}

type ToolResultData struct {
	TurnID  string          `json:"turn_id"`
	Tool    string          `json:"tool"`
	Output  json.RawMessage `json:"output,omitempty"`
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
