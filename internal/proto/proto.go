package proto

import "encoding/json"

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
	OpHealth = "Health"
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
