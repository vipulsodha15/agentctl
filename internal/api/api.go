package api

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/agentctl/agentctl/internal/proto"
	"github.com/agentctl/agentctl/internal/version"
)

type DockerProbe interface {
	Info(ctx context.Context) (proto.DockerHealth, error)
}

type Server struct {
	startedAt   time.Time
	reconciling atomic.Bool
	docker      DockerProbe
}

type Options struct {
	Docker DockerProbe
}

func New(opts Options) *Server {
	return &Server{
		startedAt: time.Now(),
		docker:    opts.Docker,
	}
}

func (s *Server) SetReconciling(v bool) {
	s.reconciling.Store(v)
}

func (s *Server) Health(ctx context.Context) proto.HealthResponse {
	uptime := int64(time.Since(s.startedAt).Seconds())
	resp := proto.HealthResponse{
		OK:          true,
		Version:     version.Version,
		Build:       version.Build,
		Reconciling: s.reconciling.Load(),
		UptimeS:     uptime,
	}
	if s.docker != nil {
		dh, err := s.docker.Info(ctx)
		if err != nil {
			resp.Docker = proto.DockerHealth{OK: false, Error: err.Error()}
			resp.OK = false
		} else {
			resp.Docker = dh
			if !dh.OK {
				resp.OK = false
			}
		}
	}
	if resp.Reconciling {
		resp.OK = false
	}
	return resp
}
