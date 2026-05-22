package agentd

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/agentctl/agentctl/internal/fan"
	"github.com/agentctl/agentctl/internal/proto"
	"github.com/agentctl/agentctl/internal/sm"
	"github.com/agentctl/agentctl/internal/tm"
	"github.com/agentctl/agentctl/internal/ttl"
)

// TestNewTaskRuntime_WiresProviderResolver locks the wiring that
// f396977 left half-finished: the daemon's provider resolver must reach
// the task-chat SessionRuntime, or sm.Create fails with
// ErrProviderRequired for any agent whose YAML doesn't pin `provider:`
// (which is every built-in), the stage row's session_id stays NULL, and
// the task chat is dead on arrival. The test feeds an agent with no
// provider through newTaskRuntime's StartStage and asserts the resolver
// was consulted AND the resolved provider/model reached sm.Create.
func TestNewTaskRuntime_WiresProviderResolver(t *testing.T) {
	api := &stubSessionAPI{}
	var resolverCalls int
	resolver := func(cliProvider, cliModel string) (string, string, error) {
		resolverCalls++
		if cliProvider != "" || cliModel != "" {
			t.Errorf("resolver expected empty inputs from unpinned agent; got provider=%q model=%q", cliProvider, cliModel)
		}
		return "anthropic", "claude-resolved", nil
	}

	rt := newTaskRuntime(api, slog.Default(), resolver)
	res, err := rt.StartStage(context.Background(), tm.StartStageInput{
		TaskID:   "task-1",
		StageID:  "stg-1",
		Position: 1,
		Agent:    ttl.Agent{Name: "a", Prompt: "hi"},
		IssueMD:  "fix it",
	})
	if err != nil {
		t.Fatalf("StartStage: %v", err)
	}
	if res.SessionID == "" {
		t.Fatal("StartStage returned empty SessionID")
	}
	if resolverCalls != 1 {
		t.Fatalf("resolver called %d times, want 1", resolverCalls)
	}
	if len(api.created) != 1 {
		t.Fatalf("sm.Create called %d times, want 1", len(api.created))
	}
	if got := api.created[0].Provider; got != "anthropic" {
		t.Errorf("sm.Create Provider: got %q, want %q (from resolver)", got, "anthropic")
	}
	if got := api.created[0].Model; got != "claude-resolved" {
		t.Errorf("sm.Create Model: got %q, want %q (from resolver)", got, "claude-resolved")
	}

	_ = rt.StopStage(context.Background(), res.SessionID)
}

// stubSessionAPI records the sm.Manager calls SessionRuntime makes so the
// wiring test can assert on what reached sm.Create. The Attach stream is
// a no-op pipe — the runtime's runReader goroutine blocks on Recv() until
// StopStage closes the stream.
type stubSessionAPI struct {
	mu      sync.Mutex
	created []sm.CreateRequest
	sent    []sm.SendRequest
	streams []*stubStream
}

func (s *stubSessionAPI) Create(_ context.Context, req sm.CreateRequest) (sm.CreateResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.created = append(s.created, req)
	return sm.CreateResult{SessionID: "sess-1"}, nil
}

func (s *stubSessionAPI) Send(_ context.Context, req sm.SendRequest) (sm.SendResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, req)
	return sm.SendResult{MessageID: "mid-1"}, nil
}

func (s *stubSessionAPI) Attach(_ context.Context, _ string) (fan.Stream, error) {
	st := &stubStream{done: make(chan struct{})}
	s.mu.Lock()
	s.streams = append(s.streams, st)
	s.mu.Unlock()
	return st, nil
}

func (s *stubSessionAPI) Terminate(_ context.Context, _ string) error { return nil }

func (s *stubSessionAPI) ListMCPNames(_ context.Context) ([]string, error) { return nil, nil }

type stubStream struct {
	once sync.Once
	done chan struct{}
}

func (s *stubStream) Recv() (proto.Event, bool, string) {
	<-s.done
	return proto.Event{}, false, ""
}

func (s *stubStream) Close() {
	s.once.Do(func() { close(s.done) })
}
