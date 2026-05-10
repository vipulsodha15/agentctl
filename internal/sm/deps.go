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
	NetworkCreate(ctx context.Context, sessionID, name string) (NetworkRef, error)
	NetworkRemove(ctx context.Context, networkID string) error
}

type NetworkRef struct {
	ID    string
	Name  string
	Label string
}

type MountType string

const (
	MountBind   MountType = "bind"
	MountVolume MountType = "volume"
)

type ContainerSpec struct {
	SessionID      string
	ImageID        string
	Name           string
	Labels         map[string]string
	EnvFile        string
	Mounts         []ContainerMount
	MemBytes       int64
	CPUs           float64
	NetworkID      string
	ReadOnlyRootFS bool
	CapDrop        []string
	SecurityOpts   []string
	PidsLimit      int64
	Tmpfs          map[string]string
	MemorySwap     int64
}

type SkillsComposer interface {
	Compose(dest string) (SkillsComposeResult, error)
}

type SkillsComposeResult struct {
	Path       string
	Hash       string
	Skills     []string
	Collisions []string
}

type ContainerMount struct {
	Type     MountType
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

// UsageRecord is what the actor passes to a UsageRecorder when a
// runtime.event{kind=usage} arrives. The recorder owns insertion and cost
// computation; the actor owns broadcast.
type UsageRecord struct {
	SessionID        string
	TurnID           string
	At               time.Time
	Model            string
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
}

// UsageRecorder is the subset of internal/usage.Recorder + cost-computation
// the actor needs. Implemented by *usage.Service in production.
type UsageRecorder interface {
	OnUsage(ctx context.Context, ev UsageRecord) error
	CostFor(ev UsageRecord) (float64, bool)
}
