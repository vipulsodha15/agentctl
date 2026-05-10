package cm

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

type Manager interface {
	Create(ctx context.Context, spec Spec) (Container, error)
	Start(ctx context.Context, id string) error
	Stop(ctx context.Context, id string, grace time.Duration) error
	Kill(ctx context.Context, id string) error
	Remove(ctx context.Context, id string, force bool) error
	Inspect(ctx context.Context, id string) (Status, error)
	DockerInfo(ctx context.Context) (Info, error)
	NetworkCreate(ctx context.Context, sessionID, name string) (NetworkRef, error)
	NetworkRemove(ctx context.Context, networkID string) error
	NetworkList(ctx context.Context, labelKey, labelValue string) ([]NetworkRef, error)
}

type CreateRequest struct {
	Name            string
	Image           string
	Labels          map[string]string
	Env             []string
	WorkingDir      string
	User            string
	Mounts          []Mount
	MemoryBytes     int64
	MemorySwapBytes int64
	NanoCPUs        int64
	NetworkMode     string
	RestartPolicy   string
	AutoRemove      bool
	ReadOnlyRootFS  bool
	CapDrop         []string
	SecurityOpt     []string
	PidsLimit       int64
	Tmpfs           map[string]string
}

type DockerClient interface {
	Create(ctx context.Context, req CreateRequest) (string, error)
	Start(ctx context.Context, id string) error
	Stop(ctx context.Context, id string, grace time.Duration) error
	Kill(ctx context.Context, id string, signal string) error
	Remove(ctx context.Context, id string, force bool) error
	Inspect(ctx context.Context, id string) (Status, error)
	Info(ctx context.Context) (Info, error)
	NetworkCreate(ctx context.Context, req NetworkCreateRequest) (NetworkRef, error)
	NetworkRemove(ctx context.Context, networkID string) error
	NetworkList(ctx context.Context, labelKey, labelValue string) ([]NetworkRef, error)
}

type managerImpl struct {
	docker DockerClient
}

func NewManager(docker DockerClient) Manager {
	return &managerImpl{docker: docker}
}

func (m *managerImpl) Create(ctx context.Context, spec Spec) (Container, error) {
	if err := validateSpec(spec); err != nil {
		return Container{}, err
	}
	req, err := BuildCreateRequest(spec)
	if err != nil {
		return Container{}, err
	}
	id, err := m.docker.Create(ctx, req)
	if err != nil {
		return Container{}, fmt.Errorf("create %s: %w", spec.Name, err)
	}
	return Container{ID: id, Name: spec.Name}, nil
}

func (m *managerImpl) Start(ctx context.Context, id string) error {
	return m.docker.Start(ctx, id)
}

func (m *managerImpl) Stop(ctx context.Context, id string, grace time.Duration) error {
	return m.docker.Stop(ctx, id, grace)
}

func (m *managerImpl) Kill(ctx context.Context, id string) error {
	return m.docker.Kill(ctx, id, "SIGKILL")
}

func (m *managerImpl) Remove(ctx context.Context, id string, force bool) error {
	return m.docker.Remove(ctx, id, force)
}

func (m *managerImpl) Inspect(ctx context.Context, id string) (Status, error) {
	return m.docker.Inspect(ctx, id)
}

func (m *managerImpl) DockerInfo(ctx context.Context) (Info, error) {
	info, err := m.docker.Info(ctx)
	if err != nil {
		return Info{OK: false, Error: err.Error()}, nil
	}
	return info, nil
}

func (m *managerImpl) NetworkCreate(ctx context.Context, sessionID, name string) (NetworkRef, error) {
	if sessionID == "" {
		return NetworkRef{}, errors.New("network: session id required")
	}
	if name == "" {
		return NetworkRef{}, errors.New("network: name required")
	}
	req := NetworkCreateRequest{
		Name:   name,
		Driver: "bridge",
		Labels: map[string]string{"agentctl.session": sessionID},
		Options: map[string]string{
			"com.docker.network.bridge.enable_icc": "false",
		},
		EnableIPv6: false,
	}
	ref, err := m.docker.NetworkCreate(ctx, req)
	if err != nil {
		return NetworkRef{}, fmt.Errorf("network create %s: %w", name, err)
	}
	if ref.Name == "" {
		ref.Name = name
	}
	if ref.Label == "" {
		ref.Label = sessionID
	}
	return ref, nil
}

func (m *managerImpl) NetworkRemove(ctx context.Context, networkID string) error {
	if networkID == "" {
		return errors.New("network: id required")
	}
	return m.docker.NetworkRemove(ctx, networkID)
}

func (m *managerImpl) NetworkList(ctx context.Context, labelKey, labelValue string) ([]NetworkRef, error) {
	return m.docker.NetworkList(ctx, labelKey, labelValue)
}

func validateSpec(spec Spec) error {
	if spec.SessionID == "" {
		return errors.New("spec: SessionID required")
	}
	if spec.ImageID == "" {
		return errors.New("spec: ImageID required")
	}
	if spec.Name == "" {
		return errors.New("spec: Name required")
	}
	if spec.MemBytes <= 0 {
		return errors.New("spec: MemBytes must be positive")
	}
	if spec.CPUs <= 0 {
		return errors.New("spec: CPUs must be positive")
	}
	for _, mt := range spec.Mounts {
		if mt.Source == "" || mt.Target == "" {
			return fmt.Errorf("spec: mount missing source or target: %+v", mt)
		}
	}
	return nil
}

// BuildCreateRequest renders Spec into the documented Docker engine
// container-create body (container-and-image.md §2). Exposed for golden tests.
func BuildCreateRequest(spec Spec) (CreateRequest, error) {
	const billion = int64(1_000_000_000)
	env, err := readEnvFile(spec.EnvFile)
	if err != nil {
		return CreateRequest{}, err
	}
	nanoCPUs := int64(spec.CPUs * float64(billion))
	memSwap := spec.MemorySwap
	if memSwap == 0 {
		memSwap = spec.MemBytes
	}
	req := CreateRequest{
		Name:            spec.Name,
		Image:           spec.ImageID,
		Labels:          spec.Labels,
		Env:             env,
		WorkingDir:      "/work",
		User:            "1000:1000",
		Mounts:          append([]Mount(nil), spec.Mounts...),
		MemoryBytes:     spec.MemBytes,
		MemorySwapBytes: memSwap,
		NanoCPUs:        nanoCPUs,
		NetworkMode:     spec.NetworkID,
		RestartPolicy:   "no",
		AutoRemove:      false,
		ReadOnlyRootFS:  spec.ReadOnlyRootFS,
		CapDrop:         append([]string(nil), spec.CapDrop...),
		SecurityOpt:     append([]string(nil), spec.SecurityOpts...),
		PidsLimit:       spec.PidsLimit,
		Tmpfs:           copyStringMap(spec.Tmpfs),
	}
	if req.NetworkMode == "" {
		req.NetworkMode = "bridge"
	}
	return req, nil
}

func copyStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func readEnvFile(path string) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open env file %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	var env []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 4096), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.Contains(line, "=") {
			return nil, fmt.Errorf("env file %s: line missing '=': %q", path, line)
		}
		env = append(env, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read env file %s: %w", path, err)
	}
	return env, nil
}
