package sm

import (
	"context"
	"encoding/json"
	"time"
)

// ContainerManager is the subset of internal/cm.Manager that sm uses.
// Defined here so sm can compile and test without importing M2-A's package.
type ContainerManager interface {
	Create(ctx context.Context, spec ContainerSpec) (ContainerHandle, error)
	Start(ctx context.Context, id string) error
	Stop(ctx context.Context, id string, grace time.Duration) error
	Remove(ctx context.Context, id string, force bool) error
}

type ContainerSpec struct {
	SessionID string
	ImageID   string
	Name      string
	Labels    map[string]string
	EnvFile   string
	Mounts    []ContainerMount
	MemBytes  int64
	CPUs      float64
	NetworkID string
}

type ContainerMount struct {
	Source   string
	Target   string
	ReadOnly bool
}

type ContainerHandle struct {
	ID    string
	Image string
}

// ControlServer is the subset of internal/cc.Server that sm uses. The session
// manager registers a callback that the control server calls when a connection
// arrives for a given session id.
type ControlServer interface {
	Listen(sessionID, sockPath, sessionToken string, handler ControlHandler) error
	Stop(sessionID string) error
}

type ControlFrame struct {
	V    int             `json:"v"`
	Seq  int64           `json:"seq"`
	Kind string          `json:"kind"`
	TS   time.Time       `json:"ts"`
	Data json.RawMessage `json:"data,omitempty"`
}

type ControlConn interface {
	Send(frame ControlFrame) error
	Recv() (ControlFrame, error)
	Close() error
}

type ControlHandler func(conn ControlConn)

const (
	RuntimeHello          = "runtime.hello"
	RuntimeReady          = "runtime.ready"
	RuntimeEvent          = "runtime.event"
	RuntimeError          = "runtime.error"
	RuntimeSessionID      = "runtime.session_id"
	RuntimeHeartbeat      = "runtime.heartbeat"
	RuntimeSnapshot       = "runtime.snapshot"
	RuntimeRepoChanged    = "repo.changed"
	AgentdGreet           = "agentd.greet"
	AgentdMessage         = "agentd.message"
	AgentdInterrupt       = "agentd.interrupt"
	AgentdSnapshotRequest = "agentd.snapshot_request"
	AgentdShutdown        = "agentd.shutdown"
)

// RuntimeEventData mirrors the inner shape the shim emits inside a
// runtime.event frame: { kind, ... }.
type RuntimeEventData struct {
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data,omitempty"`
}
