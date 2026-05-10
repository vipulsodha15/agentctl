package doctor

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/agentctl/agentctl/internal/cm"
)

func checkNetworkPeerIsolation() Check {
	if _, err := exec.LookPath("docker"); err != nil {
		return Check{Name: "network.peer_isolation", Status: StatusWarn, Message: "docker not on PATH; skipping peer isolation probe"}
	}
	cli, err := cm.NewDockerSDKClient()
	if err != nil {
		return Check{Name: "network.peer_isolation", Status: StatusWarn, Message: "docker client unavailable", Detail: err.Error()}
	}
	mgr := cm.NewManager(cli)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return runPeerIsolationProbe(ctx, mgr)
}

func runPeerIsolationProbe(ctx context.Context, mgr cm.Manager) Check {
	const sessionA = "doctor-a"
	const sessionB = "doctor-b"
	netA, errA := mgr.NetworkCreate(ctx, sessionA, "agentctl-doctor-a-"+probeSuffix())
	if errA != nil {
		return Check{Name: "network.peer_isolation", Status: StatusFail, Message: "network create failed", Detail: errA.Error()}
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = mgr.NetworkRemove(cleanupCtx, netA.ID)
		cancel()
	}()
	netB, errB := mgr.NetworkCreate(ctx, sessionB, "agentctl-doctor-b-"+probeSuffix())
	if errB != nil {
		return Check{Name: "network.peer_isolation", Status: StatusFail, Message: "network create failed", Detail: errB.Error()}
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = mgr.NetworkRemove(cleanupCtx, netB.ID)
		cancel()
	}()
	if err := verifyICCDisabled(netA.Name); err != nil {
		return Check{Name: "network.peer_isolation", Status: StatusFail, Message: "network A missing enable_icc=false", Detail: err.Error()}
	}
	if err := verifyICCDisabled(netB.Name); err != nil {
		return Check{Name: "network.peer_isolation", Status: StatusFail, Message: "network B missing enable_icc=false", Detail: err.Error()}
	}
	return Check{
		Name:    "network.peer_isolation",
		Status:  StatusOK,
		Message: "two ephemeral session networks created with enable_icc=false",
		Detail:  "live peer-to-peer probe is exercised in DinD scenario 08",
	}
}

func verifyICCDisabled(name string) error {
	out, err := exec.Command("docker", "network", "inspect", name, "--format", "{{ index .Options \"com.docker.network.bridge.enable_icc\" }}").CombinedOutput()
	if err != nil {
		return fmt.Errorf("inspect %s: %v: %s", name, err, string(out))
	}
	got := trimNewline(string(out))
	if got != "false" {
		return fmt.Errorf("inspect %s: enable_icc=%q (want false)", name, got)
	}
	return nil
}

func trimNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}

func probeSuffix() string {
	return fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000_000)
}
