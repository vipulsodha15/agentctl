package agentd

import (
	"log/slog"

	"github.com/agentctl/agentctl/internal/tm"
)

// newTaskRuntime builds the task-chat SessionRuntime with the daemon's
// provider resolver attached. Lives in its own helper so the resolver
// wiring has a single, testable construction site: a regression where the
// resolver isn't attached lets sm.Create fail with ErrProviderRequired on
// any agent whose YAML doesn't pin `provider:`, which silently broke task
// chat until f396977's two halves were reconciled. See
// TestNewTaskRuntime_WiresProviderResolver.
func newTaskRuntime(api tm.SessionAPI, logger *slog.Logger, resolver func(cliProvider, cliModel string) (string, string, error)) *tm.SessionRuntime {
	return tm.NewSessionRuntime(api, logger).
		WithResolver(tm.ProviderResolver(resolver))
}
