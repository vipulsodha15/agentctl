package cm

import (
	"context"

	"github.com/agentctl/agentctl/internal/proto"
)

// HealthProbe adapts a Manager to the api.DockerProbe surface so the daemon's
// Health endpoint can report docker.ok / docker.version using the SDK
// connection rather than the M1 CLI fallback.
type HealthProbe struct {
	Manager Manager
}

func (p HealthProbe) Info(ctx context.Context) (proto.DockerHealth, error) {
	info, err := p.Manager.DockerInfo(ctx)
	if err != nil {
		return proto.DockerHealth{OK: false, Error: err.Error()}, nil
	}
	return proto.DockerHealth{OK: info.OK, Version: info.Version, Error: info.Error}, nil
}
