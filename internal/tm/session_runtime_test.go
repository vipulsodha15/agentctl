package tm

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentctl/agentctl/internal/fan"
	"github.com/agentctl/agentctl/internal/proto"
	"github.com/agentctl/agentctl/internal/sm"
	"github.com/agentctl/agentctl/internal/ttl"
)

// fakeSessionAPI lets the runtime tests assert on the sm.Manager calls
// SessionRuntime makes without spinning up the full session manager. The
// stream returned from Attach is a channel-backed proto.Event pipe; the
// test pushes events into it to simulate the in-container SDK's replies.
type fakeSessionAPI struct {
	mu sync.Mutex

	created   []sm.CreateRequest
	sent      []sm.SendRequest
	terminate []string
	streams   map[string]*fakeStream

	nextSession  int
	nextMID      int
	createErr    error
	sendErr      error
	attachErr    error
	terminateErr error

	// beforeSendReturn, if set, is invoked while Send is executing — after
	// the MessageID has been allocated but before Send returns. Tests use
	// this to reproduce the sm.Manager production timing, where TurnStart
	// is broadcast on the subscriber stream before the SendResult is
	// returned to the caller.
	beforeSendReturn func(req sm.SendRequest, messageID string)
}

func newFakeSessionAPI() *fakeSessionAPI {
	return &fakeSessionAPI{streams: map[string]*fakeStream{}}
}

func (f *fakeSessionAPI) Create(_ context.Context, req sm.CreateRequest) (sm.CreateResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return sm.CreateResult{}, f.createErr
	}
	f.nextSession++
	sid := "sess-" + strconv.Itoa(f.nextSession)
	f.created = append(f.created, req)
	return sm.CreateResult{SessionID: sid, Status: "starting"}, nil
}

func (f *fakeSessionAPI) Send(_ context.Context, req sm.SendRequest) (sm.SendResult, error) {
	f.mu.Lock()
	if f.sendErr != nil {
		f.mu.Unlock()
		return sm.SendResult{}, f.sendErr
	}
	f.nextMID++
	mid := "msg-" + strconv.Itoa(f.nextMID)
	f.sent = append(f.sent, req)
	hook := f.beforeSendReturn
	f.mu.Unlock()
	if hook != nil {
		hook(req, mid)
	}
	return sm.SendResult{MessageID: mid}, nil
}

func (f *fakeSessionAPI) Attach(_ context.Context, sessionID string) (fan.Stream, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.attachErr != nil {
		return nil, f.attachErr
	}
	s, ok := f.streams[sessionID]
	if !ok {
		s = newFakeStream()
		f.streams[sessionID] = s
	}
	return s, nil
}

func (f *fakeSessionAPI) Terminate(_ context.Context, sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.terminate = append(f.terminate, sessionID)
	if s, ok := f.streams[sessionID]; ok {
		s.Close()
	}
	return f.terminateErr
}

func (f *fakeSessionAPI) stream(sessionID string) *fakeStream {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.streams[sessionID]
}

func (f *fakeSessionAPI) lastCreate() sm.CreateRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.created) == 0 {
		return sm.CreateRequest{}
	}
	return f.created[len(f.created)-1]
}

func (f *fakeSessionAPI) lastSent() sm.SendRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		return sm.SendRequest{}
	}
	return f.sent[len(f.sent)-1]
}

type fakeStream struct {
	ch     chan proto.Event
	once   sync.Once
	closed chan struct{}
}

func newFakeStream() *fakeStream {
	return &fakeStream{ch: make(chan proto.Event, 64), closed: make(chan struct{})}
}

func (s *fakeStream) Recv() (proto.Event, bool, string) {
	select {
	case ev, ok := <-s.ch:
		if !ok {
			return proto.Event{}, false, ""
		}
		return ev, true, ""
	case <-s.closed:
		return proto.Event{}, false, ""
	}
}

func (s *fakeStream) Close() {
	s.once.Do(func() { close(s.closed) })
}

func (s *fakeStream) push(kind string, data any) {
	raw, _ := json.Marshal(data)
	s.ch <- proto.Event{Kind: kind, Data: raw}
}

func waitFor(t *testing.T, fn func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timeout: %s", msg)
}

func TestSessionRuntime_StartStage_SeedCarriesAgentAndFraming(t *testing.T) {
	// Stage role + assembly line framing must travel in the SEED user message,
	// not via SystemPrompt — passing a custom system_prompt to the SDK
	// switches it out of Claude Code preset mode and breaks the JSONL
	// mirror that backs refresh history.
	api := newFakeSessionAPI()
	r := NewSessionRuntime(api, nil)
	_, err := r.StartStage(context.Background(), StartStageInput{
		TaskID:    "t1",
		StageID:   "s1",
		Position:  1,
		Agent:     ttl.Agent{Name: "bug-investigator", Prompt: "You are a bug investigator.", Model: "claude-sonnet-4-6", MCPsAllowed: []string{"github"}},
		NextAgent: "fixer",
		IssueMD:   "Fix the 429.",
	})
	if err != nil {
		t.Fatalf("StartStage: %v", err)
	}
	req := api.lastCreate()
	if req.SystemPrompt != "" {
		t.Errorf("SystemPrompt must be empty so the SDK keeps Claude Code preset; got %q", req.SystemPrompt)
	}
	seed := api.lastSent().Content
	if !strings.HasPrefix(seed, "You are a bug investigator.") {
		t.Errorf("seed must start with agent.Prompt; got %q", seed)
	}
	if !strings.Contains(seed, "The next agent will be fixer.") {
		t.Errorf("seed missing next-agent framing; got %q", seed)
	}
	if strings.Contains(seed, "previous agent") {
		t.Errorf("stage 1 must not advertise a previous agent; got %q", seed)
	}
	if req.Model != "claude-sonnet-4-6" {
		t.Errorf("Model not forwarded: %q", req.Model)
	}
	if len(req.MCPs) != 1 || req.MCPs[0] != "github" {
		t.Errorf("MCPs not forwarded: %+v", req.MCPs)
	}
	if len(req.Repos) != 0 {
		t.Errorf("each stage must run with no repos cloned; got %+v", req.Repos)
	}
}

func TestSessionRuntime_StartStage_FinalStageFramingAndSeed(t *testing.T) {
	api := newFakeSessionAPI()
	r := NewSessionRuntime(api, nil)
	_, err := r.StartStage(context.Background(), StartStageInput{
		TaskID:      "t1",
		StageID:     "s2",
		Position:    2,
		Agent:       ttl.Agent{Name: "fixer", Prompt: "You are a fixer."},
		PrevAgent:   "bug-investigator",
		IssueMD:     "Fix the 429.",
		HandoffInMD: "## Root cause\nMissing OAuth header.",
	})
	if err != nil {
		t.Fatalf("StartStage: %v", err)
	}
	if sys := api.lastCreate().SystemPrompt; sys != "" {
		t.Errorf("SystemPrompt must be empty so the SDK keeps Claude Code preset; got %q", sys)
	}
	seed := api.lastSent().Content
	if !strings.Contains(seed, "previous agent was bug-investigator") {
		t.Errorf("seed missing prev-agent framing: %q", seed)
	}
	if !strings.Contains(seed, "You are the final stage.") {
		t.Errorf("seed missing final-stage framing: %q", seed)
	}
	if !strings.Contains(seed, "# Handoff from bug-investigator") {
		t.Errorf("seed must surface the prior stage's handoff; got %q", seed)
	}
	if !strings.Contains(seed, "Missing OAuth header.") {
		t.Errorf("seed must include HandoffInMD body; got %q", seed)
	}
	if !strings.Contains(seed, "# Original task") {
		t.Errorf("seed must still include the original task brief; got %q", seed)
	}
}

// TestSessionRuntime_StartStage_StagePinsOverrideAgent — per-stage
// provider/model pins from the assembly-line YAML must take precedence
// over the agent's own Provider/Model. This is what lets a single
// `bug-investigator` agent run on Anthropic in `bug-multi-provider` and
// on OpenAI in some hypothetical openai-only line without forking the
// agent YAML (ADR 0020 §3).
func TestSessionRuntime_StartStage_StagePinsOverrideAgent(t *testing.T) {
	api := newFakeSessionAPI()
	r := NewSessionRuntime(api, nil)
	_, err := r.StartStage(context.Background(), StartStageInput{
		TaskID: "t1", StageID: "s1", Position: 1,
		Agent: ttl.Agent{
			Name:     "bug-investigator",
			Prompt:   "You are a bug investigator.",
			Provider: "anthropic", // would lose
			Model:    "claude-sonnet-4-6",
		},
		StageProvider: "openai", // wins
		StageModel:    "gpt-5.5",
		IssueMD:       "Fix the 429.",
	})
	if err != nil {
		t.Fatalf("StartStage: %v", err)
	}
	req := api.lastCreate()
	if req.Provider != "openai" {
		t.Errorf("stage provider override lost: got %q want openai", req.Provider)
	}
	if req.Model != "gpt-5.5" {
		t.Errorf("stage model override lost: got %q want gpt-5.5", req.Model)
	}
}

// TestSessionRuntime_StartStage_AgentFallback — when the stage entry
// carries no pins (the common case for portable built-in lines), the
// agent's Provider/Model still flow through.
func TestSessionRuntime_StartStage_AgentFallback(t *testing.T) {
	api := newFakeSessionAPI()
	r := NewSessionRuntime(api, nil)
	_, err := r.StartStage(context.Background(), StartStageInput{
		TaskID: "t1", StageID: "s1", Position: 1,
		Agent: ttl.Agent{
			Name:     "bug-investigator",
			Prompt:   "You are a bug investigator.",
			Provider: "anthropic",
			Model:    "claude-sonnet-4-6",
		},
		IssueMD: "Fix the 429.",
	})
	if err != nil {
		t.Fatalf("StartStage: %v", err)
	}
	req := api.lastCreate()
	if req.Provider != "anthropic" {
		t.Errorf("agent provider not forwarded: %q", req.Provider)
	}
	if req.Model != "claude-sonnet-4-6" {
		t.Errorf("agent model not forwarded: %q", req.Model)
	}
}

func TestSessionRuntime_AssistantMessage_FansToCallback(t *testing.T) {
	api := newFakeSessionAPI()
	r := NewSessionRuntime(api, nil)
	var gotMu sync.Mutex
	var got []string
	_, err := r.StartStage(context.Background(), StartStageInput{
		TaskID: "t1", StageID: "s1", Position: 1,
		Agent: ttl.Agent{Name: "a"}, IssueMD: "hi",
		OnAssistantMessage: func(c string) {
			gotMu.Lock()
			defer gotMu.Unlock()
			got = append(got, c)
		},
	})
	if err != nil {
		t.Fatalf("StartStage: %v", err)
	}
	st := api.stream("sess-1")
	st.push(proto.EventAssistantMessage, proto.AssistantMessageData{TurnID: "t-unrelated", Content: "hello world"})

	waitFor(t, func() bool {
		gotMu.Lock()
		defer gotMu.Unlock()
		return len(got) == 1 && got[0] == "hello world"
	}, "OnAssistantMessage callback")
}

func TestSessionRuntime_Synthesize_CorrelatesByMessageIDAndSkipsCallback(t *testing.T) {
	api := newFakeSessionAPI()
	r := NewSessionRuntime(api, nil)
	var leaked []string
	_, err := r.StartStage(context.Background(), StartStageInput{
		TaskID: "t1", StageID: "s1", Position: 1,
		Agent: ttl.Agent{Name: "a"}, IssueMD: "hi",
		OnAssistantMessage: func(c string) { leaked = append(leaked, c) },
	})
	if err != nil {
		t.Fatalf("StartStage: %v", err)
	}
	st := api.stream("sess-1")

	// Run Synthesize and have the fake fire the matching turn.start/
	// assistant.message after Send returns. The reader must route the
	// assistant.message to Synthesize's reply channel — NOT to the
	// OnAssistantMessage callback, since the synthesis is recorded as a
	// `RoleSynthesis` row separately by tm.Manager.Handoff.
	type result struct {
		text string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		text, err := r.Synthesize(context.Background(), SendMessageInput{StageID: "s1", SessionID: "sess-1", Content: "synthesize now"})
		done <- result{text, err}
	}()

	// Wait for the synthesize Send to land in the fake so we know which mid
	// the reader is looking for. The seed message is `sent[0]`; the synth
	// send is `sent[1]`.
	waitFor(t, func() bool {
		api.mu.Lock()
		defer api.mu.Unlock()
		return len(api.sent) >= 2
	}, "synth Send to land")
	mid := "msg-2"

	st.push(proto.EventTurnStart, proto.TurnStartData{TurnID: "T-synth", MessageID: mid})
	st.push(proto.EventAssistantMessage, proto.AssistantMessageData{TurnID: "T-synth", Content: "## Synthesis\n- root cause: …"})

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("Synthesize: %v", res.err)
		}
		if !strings.Contains(res.text, "root cause") {
			t.Errorf("Synthesize returned wrong text: %q", res.text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Synthesize did not return")
	}
	if len(leaked) != 0 {
		t.Errorf("synthesis must not also fire OnAssistantMessage; got %+v", leaked)
	}
}

// In production, sm.Manager.Send broadcasts EventTurnStart *before* it
// returns the SendResult to the caller (see internal/sm/actor.go handleSend).
// That means the runtime's reader goroutine can observe — and process —
// the synth turn's TurnStart while Synthesize is still inside Send, before
// synthMID has been assigned. The handoff button in the task chat hung in
// exactly this race: TurnStart was dropped, synthTID never set, the
// AssistantMessage was misrouted to OnAssistantMessage, and Synthesize
// blocked on replyCh forever. This test reproduces the race by having the
// fake Send push the events *during* the call, before returning.
func TestSessionRuntime_Synthesize_TurnStartBeforeSendReturns(t *testing.T) {
	api := newFakeSessionAPI()
	r := NewSessionRuntime(api, nil)
	var leaked []string
	var leakedMu sync.Mutex
	_, err := r.StartStage(context.Background(), StartStageInput{
		TaskID: "t1", StageID: "s1", Position: 1,
		Agent: ttl.Agent{Name: "a"}, IssueMD: "hi",
		OnAssistantMessage: func(c string) {
			leakedMu.Lock()
			defer leakedMu.Unlock()
			leaked = append(leaked, c)
		},
	})
	if err != nil {
		t.Fatalf("StartStage: %v", err)
	}
	st := api.stream("sess-1")

	// Arrange the fake to push the synth TurnStart from within Send (before
	// Send returns) and pause, so the reader processes TurnStart with
	// synthMID still empty — exactly the production race. The seed Send is
	// sent[0]; the synth is sent[1] — only inject for the synth send.
	api.beforeSendReturn = func(req sm.SendRequest, mid string) {
		if req.Content != "synthesize now" {
			return
		}
		st.push(proto.EventTurnStart, proto.TurnStartData{TurnID: "T-synth", MessageID: mid})
		// Wait long enough for the reader to dequeue and process TurnStart.
		time.Sleep(20 * time.Millisecond)
	}

	type result struct {
		text string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		text, err := r.Synthesize(context.Background(), SendMessageInput{StageID: "s1", SessionID: "sess-1", Content: "synthesize now"})
		done <- result{text, err}
	}()

	// Once retroactive reconciliation has run, synthTID is set. Without the
	// fix, synthTID stays empty and this waitFor times out the test.
	waitFor(t, func() bool {
		r.mu.Lock()
		stage, ok := r.stages["s1"]
		r.mu.Unlock()
		if !ok {
			return false
		}
		stage.mu.Lock()
		defer stage.mu.Unlock()
		return stage.synthTID != ""
	}, "synthTID reconciled retroactively after Send returns")

	st.push(proto.EventAssistantMessage, proto.AssistantMessageData{TurnID: "T-synth", Content: "## Synthesis\n- root cause: cache stampede"})

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("Synthesize: %v", res.err)
		}
		if !strings.Contains(res.text, "cache stampede") {
			t.Errorf("Synthesize returned wrong text: %q", res.text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Synthesize did not return — race regressed; TurnStart was processed before synthMID was set and the synth reply was misrouted")
	}
	leakedMu.Lock()
	defer leakedMu.Unlock()
	if len(leaked) != 0 {
		t.Errorf("synthesis must not also fire OnAssistantMessage; got %+v", leaked)
	}
}

func TestSessionRuntime_IsBusy_TracksTurnLifecycle(t *testing.T) {
	api := newFakeSessionAPI()
	r := NewSessionRuntime(api, nil)
	_, err := r.StartStage(context.Background(), StartStageInput{
		TaskID: "t1", StageID: "s1", Position: 1,
		Agent: ttl.Agent{Name: "a"}, IssueMD: "hi",
	})
	if err != nil {
		t.Fatalf("StartStage: %v", err)
	}
	if r.IsBusy("s1") {
		t.Fatal("must not be busy before any turn.start")
	}
	st := api.stream("sess-1")
	st.push(proto.EventTurnStart, proto.TurnStartData{TurnID: "T1", MessageID: "msg-1"})
	waitFor(t, func() bool { return r.IsBusy("s1") }, "busy after turn.start")
	st.push(proto.EventTurnEnd, proto.TurnEndData{TurnID: "T1", Status: "ok"})
	waitFor(t, func() bool { return !r.IsBusy("s1") }, "idle after turn.end")
}

func TestSessionRuntime_StopStage_TerminatesAndStopsReader(t *testing.T) {
	api := newFakeSessionAPI()
	r := NewSessionRuntime(api, nil)
	_, err := r.StartStage(context.Background(), StartStageInput{
		TaskID: "t1", StageID: "s1", Position: 1,
		Agent: ttl.Agent{Name: "a"}, IssueMD: "hi",
	})
	if err != nil {
		t.Fatalf("StartStage: %v", err)
	}
	if err := r.StopStage(context.Background(), "sess-1"); err != nil {
		t.Fatalf("StopStage: %v", err)
	}
	api.mu.Lock()
	terminated := append([]string(nil), api.terminate...)
	api.mu.Unlock()
	if len(terminated) != 1 || terminated[0] != "sess-1" {
		t.Errorf("Terminate not called with the right session: %+v", terminated)
	}
	if _, ok := r.stages["s1"]; ok {
		t.Errorf("stage must be removed from the map after StopStage")
	}
}

// TestSessionRuntime_Send_RoutesBySessionID_WithoutMapEntry guards the
// post-idle-sweep / post-agentd-restart path: SendUserMessage must route
// straight to sm.Send by SessionID without requiring the stages map to be
// populated. Regression test for the "container not restarted, message
// lost" bug.
func TestSessionRuntime_Send_RoutesBySessionID_WithoutMapEntry(t *testing.T) {
	api := newFakeSessionAPI()
	r := NewSessionRuntime(api, nil)
	if err := r.SendUserMessage(context.Background(), SendMessageInput{
		TaskID: "t1", StageID: "s1", SessionID: "sess-restored", Content: "hello",
	}); err != nil {
		t.Fatalf("SendUserMessage: %v", err)
	}
	sent := api.lastSent()
	if sent.SessionID != "sess-restored" {
		t.Errorf("send must target the SessionID from the input; got %q", sent.SessionID)
	}
	if sent.Content != "hello" {
		t.Errorf("content not forwarded: %q", sent.Content)
	}
}

func TestSessionRuntime_Send_RejectsEmptySessionID(t *testing.T) {
	api := newFakeSessionAPI()
	r := NewSessionRuntime(api, nil)
	err := r.SendUserMessage(context.Background(), SendMessageInput{
		TaskID: "t1", StageID: "s1", SessionID: "", Content: "hi",
	})
	if err == nil {
		t.Fatal("must error when SessionID is empty")
	}
}

func TestSessionRuntime_EnsureAttached_IdempotentAndStartsReader(t *testing.T) {
	api := newFakeSessionAPI()
	r := NewSessionRuntime(api, nil)

	var gotMu sync.Mutex
	var got []string
	cb := func(c string) {
		gotMu.Lock()
		defer gotMu.Unlock()
		got = append(got, c)
	}
	in := AttachInput{TaskID: "t1", StageID: "s1", SessionID: "sess-rehydrated", OnAssistantMessage: cb}
	if err := r.EnsureAttached(context.Background(), in); err != nil {
		t.Fatalf("EnsureAttached: %v", err)
	}
	// Second call is a no-op (no second Attach on the fake).
	if err := r.EnsureAttached(context.Background(), in); err != nil {
		t.Fatalf("EnsureAttached (second): %v", err)
	}
	api.mu.Lock()
	attaches := len(api.streams)
	api.mu.Unlock()
	if attaches != 1 {
		t.Errorf("EnsureAttached must be idempotent; saw %d Attach calls", attaches)
	}
	// Reader must be running: event pushed to the stream surfaces via the callback.
	api.stream("sess-rehydrated").push(proto.EventAssistantMessage, proto.AssistantMessageData{TurnID: "T1", Content: "post-rehydrate reply"})
	waitFor(t, func() bool {
		gotMu.Lock()
		defer gotMu.Unlock()
		return len(got) == 1 && got[0] == "post-rehydrate reply"
	}, "rehydrated reader must fan events to callback")
}
