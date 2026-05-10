package cm

import (
	"context"
	"errors"
	"testing"
)

func TestNetworkCreateRequestShape(t *testing.T) {
	fd := &fakeDocker{}
	mgr := NewManager(fd)
	ref, err := mgr.NetworkCreate(context.Background(), "01HSESS", "agentctl-01HSESS")
	if err != nil {
		t.Fatalf("network create: %v", err)
	}
	if fd.netCreateReq == nil {
		t.Fatalf("docker.NetworkCreate not invoked")
	}
	req := *fd.netCreateReq
	if req.Driver != "bridge" {
		t.Errorf("Driver: got %q want bridge", req.Driver)
	}
	if req.Name != "agentctl-01HSESS" {
		t.Errorf("Name: got %q want agentctl-01HSESS", req.Name)
	}
	if got := req.Labels["agentctl.session"]; got != "01HSESS" {
		t.Errorf("Label agentctl.session: got %q want 01HSESS", got)
	}
	if got := req.Options["com.docker.network.bridge.enable_icc"]; got != "false" {
		t.Errorf("Option enable_icc: got %q want false", got)
	}
	if req.EnableIPv6 {
		t.Errorf("EnableIPv6 must be false")
	}
	if ref.ID == "" || ref.Name != "agentctl-01HSESS" || ref.Label != "01HSESS" {
		t.Errorf("returned ref: %+v", ref)
	}
}

func TestNetworkCreateValidatesArgs(t *testing.T) {
	mgr := NewManager(&fakeDocker{})
	if _, err := mgr.NetworkCreate(context.Background(), "", "n"); err == nil {
		t.Errorf("expected error for empty session")
	}
	if _, err := mgr.NetworkCreate(context.Background(), "s", ""); err == nil {
		t.Errorf("expected error for empty name")
	}
}

func TestNetworkCreatePropagatesError(t *testing.T) {
	fd := &fakeDocker{netCreateErr: errors.New("nope")}
	mgr := NewManager(fd)
	if _, err := mgr.NetworkCreate(context.Background(), "s", "n"); err == nil {
		t.Errorf("expected propagated error")
	}
}

func TestNetworkRemoveCallsDocker(t *testing.T) {
	fd := &fakeDocker{}
	mgr := NewManager(fd)
	if err := mgr.NetworkRemove(context.Background(), "net-1"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if len(fd.netRemoveCalls) != 1 || fd.netRemoveCalls[0] != "net-1" {
		t.Errorf("docker.NetworkRemove calls: %v", fd.netRemoveCalls)
	}
	if err := mgr.NetworkRemove(context.Background(), ""); err == nil {
		t.Errorf("expected error for empty id")
	}
}

func TestNetworkListPassesFilter(t *testing.T) {
	fd := &fakeDocker{netListResult: []NetworkRef{{ID: "n1", Name: "agentctl-aa", Label: "01"}}}
	mgr := NewManager(fd)
	out, err := mgr.NetworkList(context.Background(), "agentctl.session", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if fd.netListLabelKey != "agentctl.session" || fd.netListLabelVal != "" {
		t.Errorf("filter: key=%q val=%q", fd.netListLabelKey, fd.netListLabelVal)
	}
	if len(out) != 1 || out[0].ID != "n1" {
		t.Errorf("result: %+v", out)
	}
}

func TestBuildCreateRequestUsesNetworkID(t *testing.T) {
	spec, _ := sampleSpec(t)
	spec.NetworkID = "agentctl-01HSESS"
	req, err := BuildCreateRequest(spec)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if req.NetworkMode != "agentctl-01HSESS" {
		t.Errorf("NetworkMode: got %q want agentctl-01HSESS", req.NetworkMode)
	}
}
