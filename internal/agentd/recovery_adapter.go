package agentd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"

	"github.com/agentctl/agentctl/internal/recovery"
)

type recoveryAdapter struct {
	cli *client.Client
}

func newRecoveryAdapter(cli *client.Client) *recoveryAdapter {
	return &recoveryAdapter{cli: cli}
}

func (a *recoveryAdapter) List(ctx context.Context, labelKey string) ([]recovery.ContainerRef, error) {
	if a == nil || a.cli == nil {
		return nil, nil
	}
	args := filters.NewArgs()
	args.Add("label", labelKey)
	cs, err := a.cli.ContainerList(ctx, container.ListOptions{All: true, Filters: args})
	if err != nil {
		return nil, fmt.Errorf("docker container list: %w", err)
	}
	out := make([]recovery.ContainerRef, 0, len(cs))
	for _, c := range cs {
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		sid := c.Labels[labelKey]
		out = append(out, recovery.ContainerRef{
			ID:        c.ID,
			Name:      name,
			SessionID: sid,
			Running:   strings.EqualFold(c.State, "running"),
			State:     c.State,
		})
	}
	return out, nil
}

func (a *recoveryAdapter) Inspect(ctx context.Context, id string) (recovery.Status, error) {
	if a == nil || a.cli == nil {
		return recovery.Status{}, errors.New("docker client unavailable")
	}
	js, err := a.cli.ContainerInspect(ctx, id)
	if err != nil {
		return recovery.Status{}, err
	}
	st := recovery.Status{}
	if js.State != nil {
		st.State = js.State.Status
		st.Running = js.State.Running
		st.ExitCode = js.State.ExitCode
	}
	return st, nil
}

func (a *recoveryAdapter) Stop(ctx context.Context, id string, grace time.Duration) error {
	if a == nil || a.cli == nil {
		return nil
	}
	secs := int(grace.Seconds())
	return a.cli.ContainerStop(ctx, id, container.StopOptions{Timeout: &secs})
}

func (a *recoveryAdapter) Remove(ctx context.Context, id string, force bool) error {
	if a == nil || a.cli == nil {
		return nil
	}
	return a.cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: force})
}

func (a *recoveryAdapter) NetworkList(ctx context.Context, labelKey string) ([]recovery.NetworkRef, error) {
	if a == nil || a.cli == nil {
		return nil, nil
	}
	args := filters.NewArgs()
	args.Add("label", labelKey)
	nets, err := a.cli.NetworkList(ctx, dockertypes.NetworkListOptions{Filters: args})
	if err != nil {
		return nil, fmt.Errorf("docker network list: %w", err)
	}
	out := make([]recovery.NetworkRef, 0, len(nets))
	for _, n := range nets {
		sid := ""
		if n.Labels != nil {
			sid = n.Labels[labelKey]
		}
		out = append(out, recovery.NetworkRef{
			ID:        n.ID,
			Name:      n.Name,
			SessionID: sid,
		})
	}
	return out, nil
}

func (a *recoveryAdapter) NetworkRemove(ctx context.Context, id string) error {
	if a == nil || a.cli == nil {
		return nil
	}
	return a.cli.NetworkRemove(ctx, id)
}

func (a *recoveryAdapter) Adopt(_ context.Context, _, sockPath string, timeout time.Duration) error {
	if sockPath == "" {
		return errors.New("recovery adopt: empty sock path")
	}
	conn, err := net.DialTimeout("unix", sockPath, timeout)
	if err != nil {
		return fmt.Errorf("dial %s: %w", sockPath, err)
	}
	_ = conn.Close()
	return nil
}
