package cm

import (
	"context"
	"fmt"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
)

type dockerSDK struct {
	cli *client.Client
}

func NewDockerSDKClient() (DockerClient, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &dockerSDK{cli: cli}, nil
}

func (d *dockerSDK) Create(ctx context.Context, req CreateRequest) (string, error) {
	cfg := &container.Config{
		Image:      req.Image,
		Env:        req.Env,
		Labels:     req.Labels,
		WorkingDir: req.WorkingDir,
		User:       req.User,
	}
	mounts := make([]mount.Mount, 0, len(req.Mounts))
	for _, m := range req.Mounts {
		mounts = append(mounts, mount.Mount{
			Type:     mount.Type(m.Type),
			Source:   m.Source,
			Target:   m.Target,
			ReadOnly: m.ReadOnly,
		})
	}
	host := &container.HostConfig{
		Mounts:      mounts,
		NetworkMode: container.NetworkMode(req.NetworkMode),
		AutoRemove:  req.AutoRemove,
		RestartPolicy: container.RestartPolicy{
			Name: container.RestartPolicyMode(req.RestartPolicy),
		},
		ReadonlyRootfs: req.ReadOnlyRootFS,
		CapDrop:        req.CapDrop,
		SecurityOpt:    req.SecurityOpt,
		Tmpfs:          req.Tmpfs,
		Resources: container.Resources{
			Memory:     req.MemoryBytes,
			MemorySwap: req.MemorySwapBytes,
			NanoCPUs:   req.NanoCPUs,
		},
	}
	if req.PidsLimit > 0 {
		pl := req.PidsLimit
		host.Resources.PidsLimit = &pl
	}
	netCfg := &network.NetworkingConfig{}
	resp, err := d.cli.ContainerCreate(ctx, cfg, host, netCfg, nil, req.Name)
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

func (d *dockerSDK) Start(ctx context.Context, id string) error {
	return d.cli.ContainerStart(ctx, id, container.StartOptions{})
}

func (d *dockerSDK) Stop(ctx context.Context, id string, grace time.Duration) error {
	secs := int(grace.Seconds())
	return d.cli.ContainerStop(ctx, id, container.StopOptions{Timeout: &secs})
}

func (d *dockerSDK) Kill(ctx context.Context, id string, signal string) error {
	return d.cli.ContainerKill(ctx, id, signal)
}

func (d *dockerSDK) Remove(ctx context.Context, id string, force bool) error {
	return d.cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: force, RemoveVolumes: false})
}

func (d *dockerSDK) Inspect(ctx context.Context, id string) (Status, error) {
	js, err := d.cli.ContainerInspect(ctx, id)
	if err != nil {
		return Status{}, err
	}
	return statusFromInspect(js), nil
}

func (d *dockerSDK) Info(ctx context.Context) (Info, error) {
	info, err := d.cli.Info(ctx)
	if err != nil {
		return Info{OK: false, Error: err.Error()}, nil
	}
	return Info{OK: true, Version: info.ServerVersion}, nil
}

func statusFromInspect(js types.ContainerJSON) Status {
	st := Status{ID: js.ID, Name: js.Name}
	if js.State != nil {
		st.State = js.State.Status
		st.Running = js.State.Running
		st.OOMKilled = js.State.OOMKilled
		st.ExitCode = js.State.ExitCode
		if t, err := time.Parse(time.RFC3339Nano, js.State.StartedAt); err == nil {
			st.StartedAt = t
		}
		if t, err := time.Parse(time.RFC3339Nano, js.State.FinishedAt); err == nil {
			st.FinishedAt = t
		}
	}
	return st
}
