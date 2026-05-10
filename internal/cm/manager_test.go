package cm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

type fakeDocker struct {
	created       *CreateRequest
	createID      string
	createErr     error
	startCalls    []string
	stopCalls     []stopCall
	killCalls     []killCall
	removeCalls   []removeCall
	inspectStatus Status
	inspectErr    error
	infoResult    Info
	infoErr       error

	netCreateReq    *NetworkCreateRequest
	netCreateID     string
	netCreateErr    error
	netRemoveCalls  []string
	netListResult   []NetworkRef
	netListLabelKey string
	netListLabelVal string
	netListErr      error
}

type stopCall struct {
	id    string
	grace time.Duration
}

type killCall struct {
	id     string
	signal string
}

type removeCall struct {
	id    string
	force bool
}

func (f *fakeDocker) Create(_ context.Context, req CreateRequest) (string, error) {
	clone := req
	f.created = &clone
	if f.createErr != nil {
		return "", f.createErr
	}
	if f.createID == "" {
		return "container-abc", nil
	}
	return f.createID, nil
}

func (f *fakeDocker) Start(_ context.Context, id string) error {
	f.startCalls = append(f.startCalls, id)
	return nil
}

func (f *fakeDocker) Stop(_ context.Context, id string, grace time.Duration) error {
	f.stopCalls = append(f.stopCalls, stopCall{id: id, grace: grace})
	return nil
}

func (f *fakeDocker) Kill(_ context.Context, id, signal string) error {
	f.killCalls = append(f.killCalls, killCall{id: id, signal: signal})
	return nil
}

func (f *fakeDocker) Remove(_ context.Context, id string, force bool) error {
	f.removeCalls = append(f.removeCalls, removeCall{id: id, force: force})
	return nil
}

func (f *fakeDocker) Inspect(_ context.Context, _ string) (Status, error) {
	return f.inspectStatus, f.inspectErr
}

func (f *fakeDocker) Info(_ context.Context) (Info, error) {
	return f.infoResult, f.infoErr
}

func (f *fakeDocker) NetworkCreate(_ context.Context, req NetworkCreateRequest) (NetworkRef, error) {
	clone := req
	f.netCreateReq = &clone
	if f.netCreateErr != nil {
		return NetworkRef{}, f.netCreateErr
	}
	id := f.netCreateID
	if id == "" {
		id = "net-id"
	}
	label := ""
	if req.Labels != nil {
		label = req.Labels["agentctl.session"]
	}
	return NetworkRef{ID: id, Name: req.Name, Label: label}, nil
}

func (f *fakeDocker) NetworkRemove(_ context.Context, id string) error {
	f.netRemoveCalls = append(f.netRemoveCalls, id)
	return nil
}

func (f *fakeDocker) NetworkList(_ context.Context, key, value string) ([]NetworkRef, error) {
	f.netListLabelKey = key
	f.netListLabelVal = value
	return append([]NetworkRef(nil), f.netListResult...), f.netListErr
}

func sampleSpec(t *testing.T) (Spec, string) {
	t.Helper()
	dir := t.TempDir()
	envFile := filepath.Join(dir, "secrets.env")
	body := "ANTHROPIC_API_KEY=sk-test\nGITHUB_TOKEN=ghp_test\nSESSION_ID=01HABCDE\n# comment\n\nFOO=bar\n"
	if err := os.WriteFile(envFile, []byte(body), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	return Spec{
		SessionID: "01HABCDE",
		ImageID:   "sha256:cafebabe",
		Name:      "agentctl-01HABCDE",
		Labels: map[string]string{
			"agentctl.session":    "01HABCDE",
			"agentctl.image_id":   "sha256:cafebabe",
			"agentctl.created_at": "2026-05-10T00:00:00Z",
			"agentctl.user":       "tester",
		},
		EnvFile: envFile,
		Mounts: []Mount{
			{Type: MountBind, Source: "/host/sessions/01/volume", Target: "/work", ReadOnly: false},
			{Type: MountBind, Source: "/host/sessions/01/control", Target: "/run/agentctl/control", ReadOnly: false},
		},
		MemBytes: 4 << 30,
		CPUs:     2.0,
	}, envFile
}

func TestCreateGoldenRequestBody(t *testing.T) {
	spec, _ := sampleSpec(t)
	req, err := BuildCreateRequest(spec)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if req.Name != spec.Name {
		t.Errorf("Name: got %q want %q", req.Name, spec.Name)
	}
	if req.Image != spec.ImageID {
		t.Errorf("Image: got %q want %q", req.Image, spec.ImageID)
	}
	if req.WorkingDir != "/work" {
		t.Errorf("WorkingDir: got %q want /work", req.WorkingDir)
	}
	if req.User != "1000:1000" {
		t.Errorf("User: got %q want 1000:1000", req.User)
	}
	if req.MemoryBytes != spec.MemBytes {
		t.Errorf("MemoryBytes: got %d want %d", req.MemoryBytes, spec.MemBytes)
	}
	if req.MemorySwapBytes != spec.MemBytes {
		t.Errorf("MemorySwapBytes: got %d want %d (must equal MemoryBytes)", req.MemorySwapBytes, spec.MemBytes)
	}
	if req.NanoCPUs != int64(2_000_000_000) {
		t.Errorf("NanoCPUs: got %d want 2_000_000_000", req.NanoCPUs)
	}
	if req.NetworkMode != "bridge" {
		t.Errorf("NetworkMode: got %q want bridge (M2 default)", req.NetworkMode)
	}
	if req.RestartPolicy != "no" {
		t.Errorf("RestartPolicy: got %q want no", req.RestartPolicy)
	}
	if req.AutoRemove {
		t.Errorf("AutoRemove must be false")
	}
	if req.ReadOnlyRootFS {
		t.Errorf("ReadOnlyRootFS must be false in M2 (M4 enables it)")
	}
	if req.PidsLimit != 0 {
		t.Errorf("PidsLimit must be 0 in M2 (M4 sets 512)")
	}
	if len(req.CapDrop) != 0 {
		t.Errorf("CapDrop must be empty in M2 (M4 drops ALL)")
	}
	if len(req.SecurityOpt) != 0 {
		t.Errorf("SecurityOpt must be empty in M2 (M4 adds no-new-privileges)")
	}
	if len(req.Tmpfs) != 0 {
		t.Errorf("Tmpfs must be empty in M2 (M4 mounts /home/agent tmpfs)")
	}
	envWant := []string{"ANTHROPIC_API_KEY=sk-test", "FOO=bar", "GITHUB_TOKEN=ghp_test", "SESSION_ID=01HABCDE"}
	envGot := append([]string(nil), req.Env...)
	sort.Strings(envGot)
	if !reflect.DeepEqual(envGot, envWant) {
		t.Errorf("Env: got %v want %v", envGot, envWant)
	}
	if !reflect.DeepEqual(req.Labels, spec.Labels) {
		t.Errorf("Labels: got %v want %v", req.Labels, spec.Labels)
	}
	if len(req.Mounts) != 2 {
		t.Fatalf("Mounts count: got %d want 2", len(req.Mounts))
	}
	for _, m := range req.Mounts {
		if m.Type != MountBind {
			t.Errorf("mount %s: expected bind, got %s", m.Target, m.Type)
		}
	}
}

func TestCreateRejectsBadSpec(t *testing.T) {
	cases := []struct {
		name string
		mod  func(*Spec)
	}{
		{"missing session", func(s *Spec) { s.SessionID = "" }},
		{"missing image", func(s *Spec) { s.ImageID = "" }},
		{"missing name", func(s *Spec) { s.Name = "" }},
		{"zero memory", func(s *Spec) { s.MemBytes = 0 }},
		{"zero cpus", func(s *Spec) { s.CPUs = 0 }},
		{"bad mount", func(s *Spec) { s.Mounts = []Mount{{Source: "", Target: "/work"}} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec, _ := sampleSpec(t)
			tc.mod(&spec)
			fd := &fakeDocker{}
			mgr := NewManager(fd)
			if _, err := mgr.Create(context.Background(), spec); err == nil {
				t.Errorf("expected validation error")
			}
		})
	}
}

func TestManagerLifecyclePassthrough(t *testing.T) {
	spec, _ := sampleSpec(t)
	fd := &fakeDocker{createID: "ctr-1"}
	mgr := NewManager(fd)
	c, err := mgr.Create(context.Background(), spec)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if c.ID != "ctr-1" {
		t.Errorf("id: got %s want ctr-1", c.ID)
	}
	if fd.created == nil || fd.created.Name != spec.Name {
		t.Errorf("docker not called with spec name")
	}
	if err := mgr.Start(context.Background(), c.ID); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := mgr.Stop(context.Background(), c.ID, 5*time.Second); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := mgr.Kill(context.Background(), c.ID); err != nil {
		t.Fatalf("kill: %v", err)
	}
	if err := mgr.Remove(context.Background(), c.ID, true); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if got := fd.startCalls; !reflect.DeepEqual(got, []string{"ctr-1"}) {
		t.Errorf("start calls: %v", got)
	}
	if len(fd.stopCalls) != 1 || fd.stopCalls[0].grace != 5*time.Second {
		t.Errorf("stop calls: %+v", fd.stopCalls)
	}
	if len(fd.killCalls) != 1 || fd.killCalls[0].signal != "SIGKILL" {
		t.Errorf("kill calls: %+v", fd.killCalls)
	}
	if len(fd.removeCalls) != 1 || !fd.removeCalls[0].force {
		t.Errorf("remove calls: %+v", fd.removeCalls)
	}
}

func TestDockerInfoReportsError(t *testing.T) {
	mgr := NewManager(&fakeDocker{infoErr: errors.New("connection refused")})
	got, err := mgr.DockerInfo(context.Background())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.OK {
		t.Errorf("expected OK=false")
	}
	if got.Error == "" {
		t.Errorf("expected error message populated")
	}
}

func TestDockerInfoReportsOK(t *testing.T) {
	mgr := NewManager(&fakeDocker{infoResult: Info{OK: true, Version: "25.0.0"}})
	got, err := mgr.DockerInfo(context.Background())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !got.OK || got.Version != "25.0.0" {
		t.Errorf("unexpected info: %+v", got)
	}
}

func TestEnvFileMissing(t *testing.T) {
	spec, _ := sampleSpec(t)
	spec.EnvFile = filepath.Join(t.TempDir(), "does-not-exist")
	if _, err := BuildCreateRequest(spec); err == nil {
		t.Errorf("expected error for missing env file")
	}
}

func TestEnvFileMalformed(t *testing.T) {
	spec, envFile := sampleSpec(t)
	if err := os.WriteFile(envFile, []byte("no_equals_sign\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := BuildCreateRequest(spec); err == nil {
		t.Errorf("expected error for malformed env file")
	}
}

func TestHardeningFieldsPropagate(t *testing.T) {
	spec, _ := sampleSpec(t)
	spec.ReadOnlyRootFS = true
	spec.CapDrop = []string{"ALL"}
	spec.SecurityOpts = []string{"no-new-privileges"}
	spec.PidsLimit = 512
	spec.Tmpfs = map[string]string{"/home/agent": "rw,size=64m"}
	spec.MemorySwap = spec.MemBytes
	req, err := BuildCreateRequest(spec)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !req.ReadOnlyRootFS {
		t.Errorf("ReadOnlyRootFS not propagated")
	}
	if !reflect.DeepEqual(req.CapDrop, []string{"ALL"}) {
		t.Errorf("CapDrop: got %v want [ALL]", req.CapDrop)
	}
	if !reflect.DeepEqual(req.SecurityOpt, []string{"no-new-privileges"}) {
		t.Errorf("SecurityOpt: got %v", req.SecurityOpt)
	}
	if req.PidsLimit != 512 {
		t.Errorf("PidsLimit: got %d want 512", req.PidsLimit)
	}
	if got := req.Tmpfs["/home/agent"]; got != "rw,size=64m" {
		t.Errorf("Tmpfs[/home/agent]: got %q", got)
	}
	if req.MemorySwapBytes != spec.MemBytes {
		t.Errorf("MemorySwapBytes: got %d want %d", req.MemorySwapBytes, spec.MemBytes)
	}
}

func TestCustomNetworkOverridesDefault(t *testing.T) {
	spec, _ := sampleSpec(t)
	spec.NetworkID = "agentctl-01HABCDE"
	req, err := BuildCreateRequest(spec)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if req.NetworkMode != "agentctl-01HABCDE" {
		t.Errorf("NetworkMode: got %q want agentctl-01HABCDE", req.NetworkMode)
	}
}
