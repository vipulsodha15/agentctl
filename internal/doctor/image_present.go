package doctor

import (
	"context"
	"os/exec"
	"time"

	"github.com/agentctl/agentctl/internal/config"
)

func checkImagePresent(configPath string) Check {
	cfg, err := config.Load(configPath)
	if err != nil {
		return Check{Name: "image.present", Status: StatusFail, Message: err.Error()}
	}
	if cfg.Image.PinnedID == "" {
		return Check{
			Name:    "image.present",
			Status:  StatusFail,
			Message: "no pinned image id (run agentctl init)",
		}
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return Check{
			Name:    "image.present",
			Status:  StatusWarn,
			Message: "docker not on PATH; cannot verify image",
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", cfg.Image.PinnedID)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return Check{
			Name:    "image.present",
			Status:  StatusFail,
			Message: "image missing; run `agentctl init --repair` to rebuild",
			Detail:  string(out),
		}
	}
	got := trimNewline(string(out))
	if got != cfg.Image.PinnedID {
		return Check{
			Name:    "image.present",
			Status:  StatusFail,
			Message: "image id drift",
			Detail:  "have=" + got + " pinned=" + cfg.Image.PinnedID,
		}
	}
	return Check{
		Name:    "image.present",
		Status:  StatusOK,
		Message: cfg.Image.LocalTag + " id=" + truncID(cfg.Image.PinnedID),
	}
}
