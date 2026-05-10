package cm

import (
	"time"
)

type Spec struct {
	SessionID      string
	ImageID        string
	Name           string
	Labels         map[string]string
	EnvFile        string
	Mounts         []Mount
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

type MountType string

const (
	MountBind   MountType = "bind"
	MountVolume MountType = "volume"
)

type Mount struct {
	Type     MountType
	Source   string
	Target   string
	ReadOnly bool
}

type Container struct {
	ID   string
	Name string
}

type Status struct {
	ID         string
	Name       string
	State      string
	ExitCode   int
	Running    bool
	OOMKilled  bool
	StartedAt  time.Time
	FinishedAt time.Time
}

type Info struct {
	OK      bool
	Version string
	Error   string
}
