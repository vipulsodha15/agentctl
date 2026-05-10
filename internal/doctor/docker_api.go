package doctor

import (
	"context"
	"time"

	"github.com/agentctl/agentctl/internal/cm"
)

const sessionLabel = "agentctl.session"

func checkDockerAPI() Check {
	cli, err := cm.NewDockerSDKClient()
	if err != nil {
		return Check{
			Name:    "docker.api",
			Status:  StatusFail,
			Message: "agentd lacks Docker access; check group membership",
			Detail:  err.Error(),
		}
	}
	mgr := cm.NewManager(cli)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	list, err := mgr.ContainerList(ctx, sessionLabel, "")
	if err != nil {
		return Check{
			Name:    "docker.api",
			Status:  StatusFail,
			Message: "agentd lacks Docker access; check group membership",
			Detail:  err.Error(),
		}
	}
	return Check{
		Name:    "docker.api",
		Status:  StatusOK,
		Message: countWithLabel(len(list)),
	}
}

func countWithLabel(n int) string {
	if n == 1 {
		return "1 container labelled agentctl.session"
	}
	return formatInt(n) + " containers labelled agentctl.session"
}

func formatInt(n int) string {
	if n == 0 {
		return "0"
	}
	if n < 0 {
		return "-" + formatInt(-n)
	}
	digits := make([]byte, 0, 6)
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
