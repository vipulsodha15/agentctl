package tm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/agentctl/agentctl/internal/fan"
	"github.com/agentctl/agentctl/internal/proto"
	"github.com/agentctl/agentctl/internal/sm"
)

// SessionAPI is the subset of sm.Manager that SessionRuntime needs. Declared
// as an interface so tests can drive the runtime without spinning up the
// full session manager + container stack.
type SessionAPI interface {
	Create(ctx context.Context, req sm.CreateRequest) (sm.CreateResult, error)
	Send(ctx context.Context, req sm.SendRequest) (sm.SendResult, error)
	Attach(ctx context.Context, sessionID string) (fan.Stream, error)
	Terminate(ctx context.Context, sessionID string) error
}

// SessionRuntime drives each task-chat stage as a real container-backed
// session via sm.Manager, instead of POSTing to Anthropic directly. Each
// stage is one fresh session: own container, own empty workspace volume,
// agent's prompt as the SDK's system_prompt, agent's MCPs allowed, no
// repos cloned. When the user clicks "Hand off" we send the synthesis
// auto-prompt and lock onto the assistant reply that corresponds to it
// (via message-id → turn-id correlation), so an unrelated in-flight reply
// can't be mistaken for the synthesis.
type SessionRuntime struct {
	sm     SessionAPI
	logger *slog.Logger

	mu     sync.Mutex
	stages map[string]*sessionStage // keyed by stage ID
}

type sessionStage struct {
	stageID   string
	sessionID string

	cbAssistant func(content string)
	cbTool      func(tool string, input json.RawMessage)
	cbErr       func(message string)

	stream fan.Stream

	// synthMu serializes Synthesize calls per stage so the synth-correlation
	// fields below have a single writer at a time.
	synthMu sync.Mutex

	mu       sync.Mutex
	busy     bool
	synthMID string      // message id of the in-flight Synthesize send
	synthTID string      // turn id once observed via EventTurnStart
	synthCh  chan string // reader writes the synth reply here

	// lastTurnMID / lastTurnTID record the most recent TurnStart observed
	// by the reader. sm.Manager broadcasts TurnStart *before* Send returns
	// its MessageID, so the reader can process the synth turn's TurnStart
	// before Synthesize has had a chance to set synthMID. Synthesize
	// performs a retroactive lookup against these after Send returns to
	// recover the synth TurnID even when the events lapped the goroutine.
	lastTurnMID string
	lastTurnTID string
}

func NewSessionRuntime(api SessionAPI, logger *slog.Logger) *SessionRuntime {
	if logger == nil {
		logger = slog.Default()
	}
	return &SessionRuntime{
		sm:     api,
		logger: logger,
		stages: map[string]*sessionStage{},
	}
}

// HasCredential mirrors SimRuntime's startup-time credential probe. With the
// session-backed runtime, auth lives in sm.Manager (via the bundled claude
// CLI + bind-mounted credentials file), so we always report true and let
// session creation surface any auth failure at call time.
func (r *SessionRuntime) HasCredential() bool { return true }

func (r *SessionRuntime) StartStage(ctx context.Context, in StartStageInput) (StartStageResult, error) {
	system := buildStageSystemPrompt(in)
	res, err := r.sm.Create(ctx, sm.CreateRequest{
		Name:         fmt.Sprintf("task-%s-stage-%d", in.TaskID, in.Position),
		MCPs:         in.Agent.MCPsAllowed,
		Model:        in.Agent.Model,
		Repos:        nil,
		SystemPrompt: system,
	})
	if err != nil {
		return StartStageResult{}, fmt.Errorf("session runtime: create: %w", err)
	}

	if _, err := r.attach(ctx, in.StageID, res.SessionID, in.OnAssistantMessage, in.OnToolUse, in.OnError); err != nil {
		_ = r.sm.Terminate(context.Background(), res.SessionID)
		return StartStageResult{}, err
	}

	// Seed the conversation: the agent introduces itself in response to either
	// the task brief (stage 1) or the prior stage's synthesis (stage N>1).
	// The reader goroutine will deliver the assistant's intro to
	// OnAssistantMessage when it arrives.
	seed := buildStageSeedMessage(in)
	if _, err := r.sm.Send(ctx, sm.SendRequest{
		SessionID: res.SessionID,
		Content:   seed,
		ClientID:  in.StageID,
	}); err != nil {
		_ = r.StopStage(context.Background(), res.SessionID)
		return StartStageResult{}, fmt.Errorf("session runtime: seed: %w", err)
	}

	return StartStageResult{SessionID: res.SessionID}, nil
}

// SendUserMessage routes the message straight to sm.Manager by SessionID.
// It deliberately does not consult r.stages: the map is a streaming-attachment
// cache, not the source of truth. sm.Manager auto-restarts a stopped container
// on Send, so a message that arrives after an idle sweep — or after agentd
// restarted and dropped its in-memory caches — still lands on the agent.
func (r *SessionRuntime) SendUserMessage(ctx context.Context, in SendMessageInput) error {
	if in.SessionID == "" {
		return errors.New("session runtime: send requires session id")
	}
	_, err := r.sm.Send(ctx, sm.SendRequest{
		SessionID: in.SessionID,
		Content:   in.Content,
		ClientID:  in.StageID,
	})
	return err
}

func (r *SessionRuntime) Synthesize(ctx context.Context, in SendMessageInput) (string, error) {
	if in.SessionID == "" {
		return "", errors.New("session runtime: synth requires session id")
	}
	// Synthesize needs the runReader running so it can correlate the
	// assistant reply to this Send's turn id. The manager is expected to
	// EnsureAttached before calling Handoff, but guard here too in case the
	// stage was evicted for any reason.
	stage, err := r.lookupStage(in.StageID)
	if err != nil {
		return "", fmt.Errorf("session runtime: synth requires attached stage; call EnsureAttached first: %w", err)
	}

	stage.synthMu.Lock()
	defer stage.synthMu.Unlock()

	replyCh := make(chan string, 1)
	stage.mu.Lock()
	stage.synthCh = replyCh
	stage.synthMID = ""
	stage.synthTID = ""
	stage.mu.Unlock()
	defer func() {
		stage.mu.Lock()
		stage.synthCh = nil
		stage.synthMID = ""
		stage.synthTID = ""
		stage.mu.Unlock()
	}()

	res, sendErr := r.sm.Send(ctx, sm.SendRequest{
		SessionID: stage.sessionID,
		Content:   in.Content,
		ClientID:  in.StageID,
	})
	if sendErr != nil {
		return "", fmt.Errorf("session runtime: synth send: %w", sendErr)
	}
	stage.mu.Lock()
	stage.synthMID = res.MessageID
	// TurnStart for this Send is broadcast before Send returns, so the
	// reader may have already processed it with an empty synthMID. Reconcile
	// against the last-observed TurnStart so the synth TurnID is captured.
	if stage.synthTID == "" && stage.lastTurnMID == res.MessageID {
		stage.synthTID = stage.lastTurnTID
	}
	stage.mu.Unlock()

	select {
	case content := <-replyCh:
		return content, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (r *SessionRuntime) IsBusy(stageID string) bool {
	r.mu.Lock()
	stage, ok := r.stages[stageID]
	r.mu.Unlock()
	if !ok {
		return false
	}
	stage.mu.Lock()
	defer stage.mu.Unlock()
	return stage.busy
}

// StopStage tears the stage's session down. tm.Manager passes the SessionID
// here (not the stage ID), so we look up the stage by session.
func (r *SessionRuntime) StopStage(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	r.mu.Lock()
	var found *sessionStage
	for id, s := range r.stages {
		if s.sessionID == sessionID {
			found = s
			delete(r.stages, id)
			break
		}
	}
	r.mu.Unlock()
	if found != nil && found.stream != nil {
		found.stream.Close()
	}
	return r.sm.Terminate(ctx, sessionID)
}

func (r *SessionRuntime) lookupStage(stageID string) (*sessionStage, error) {
	r.mu.Lock()
	stage, ok := r.stages[stageID]
	r.mu.Unlock()
	if !ok {
		return nil, errors.New("session runtime: stage not attached")
	}
	return stage, nil
}

// EnsureAttached idempotently attaches the event stream for a stage that
// already has a session id. The manager calls this on rehydrate and before
// any operation that requires the runReader to be running (Handoff/Synthesize).
func (r *SessionRuntime) EnsureAttached(ctx context.Context, in AttachInput) error {
	if in.SessionID == "" {
		return errors.New("session runtime: attach requires session id")
	}
	r.mu.Lock()
	if _, ok := r.stages[in.StageID]; ok {
		r.mu.Unlock()
		return nil
	}
	r.mu.Unlock()
	_, err := r.attach(ctx, in.StageID, in.SessionID, in.OnAssistantMessage, in.OnToolUse, in.OnError)
	return err
}

// attach subscribes to the sm fan hub for sessionID, records the stage in
// the in-memory map, and spawns runReader. It is the single place that
// creates a sessionStage entry. The map is not consulted for routing
// decisions — it exists solely so events from the container flow through
// the manager's callbacks into the chat thread.
func (r *SessionRuntime) attach(ctx context.Context, stageID, sessionID string, cbAssistant func(string), cbTool func(string, json.RawMessage), cbErr func(string)) (*sessionStage, error) {
	stream, err := r.sm.Attach(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("session runtime: attach: %w", err)
	}
	stage := &sessionStage{
		stageID:     stageID,
		sessionID:   sessionID,
		cbAssistant: cbAssistant,
		cbTool:      cbTool,
		cbErr:       cbErr,
		stream:      stream,
	}
	r.mu.Lock()
	// A racing EnsureAttached could have populated the map between our
	// check and the Attach above. Resolve by keeping the first attachment
	// and closing this one.
	if existing, ok := r.stages[stageID]; ok {
		r.mu.Unlock()
		stream.Close()
		return existing, nil
	}
	r.stages[stageID] = stage
	r.mu.Unlock()
	go r.runReader(stage)
	return stage, nil
}

// runReader forwards session events to the stage's callbacks. The synth
// correlation lives here so an assistant reply that belongs to the handoff
// auto-prompt is captured for Synthesize() instead of being broadcast as
// a normal chat message.
func (r *SessionRuntime) runReader(s *sessionStage) {
	for {
		ev, ok, _ := s.stream.Recv()
		if !ok {
			return
		}
		switch ev.Kind {
		case proto.EventTurnStart:
			var d proto.TurnStartData
			_ = json.Unmarshal(ev.Data, &d)
			s.mu.Lock()
			s.busy = true
			s.lastTurnMID = d.MessageID
			s.lastTurnTID = d.TurnID
			if s.synthMID != "" && s.synthMID == d.MessageID {
				s.synthTID = d.TurnID
			}
			s.mu.Unlock()
		case proto.EventTurnEnd, proto.EventTurnCancelled:
			s.mu.Lock()
			s.busy = false
			s.mu.Unlock()
		case proto.EventAssistantMessage:
			var d proto.AssistantMessageData
			_ = json.Unmarshal(ev.Data, &d)
			s.mu.Lock()
			isSynth := s.synthTID != "" && s.synthTID == d.TurnID
			synthCh := s.synthCh
			s.mu.Unlock()
			if isSynth && synthCh != nil {
				select {
				case synthCh <- d.Content:
				default:
				}
				continue
			}
			if s.cbAssistant != nil {
				s.cbAssistant(d.Content)
			}
		case proto.EventToolCall:
			if s.cbTool == nil {
				continue
			}
			var d proto.ToolCallData
			_ = json.Unmarshal(ev.Data, &d)
			s.cbTool(d.Tool, d.Input)
		case proto.EventSessionError:
			if s.cbErr == nil {
				continue
			}
			s.cbErr(string(ev.Data))
		}
	}
}

// buildStageSystemPrompt composes what the in-container SDK sees as
// `system_prompt` (forwarded via the agentd.greet payload). The agent's own
// prompt comes first; the multi-stage framing is appended so the agent
// knows its position in the workflow.
func buildStageSystemPrompt(in StartStageInput) string {
	var b strings.Builder
	b.WriteString(in.Agent.Prompt)
	b.WriteString("\n\n---\nYou are part of a multi-stage workflow.")
	if in.PrevAgent != "" {
		b.WriteString("\nThe previous agent was " + in.PrevAgent + ".")
	}
	if in.NextAgent != "" {
		b.WriteString("\nThe next agent will be " + in.NextAgent + ".")
	} else {
		b.WriteString("\nYou are the final stage.")
	}
	return b.String()
}

// buildStageSeedMessage composes the first user message in the new stage's
// session — either the original task brief (stage 1) or the prior stage's
// synthesis (stage N>1). The seed message goes through the chat thread so
// the user can see what the agent was handed.
func buildStageSeedMessage(in StartStageInput) string {
	var b strings.Builder
	if in.HandoffInMD == "" {
		b.WriteString("# Task\n\n")
		b.WriteString(in.IssueMD)
		b.WriteString("\n\n")
		b.WriteString("Introduce yourself briefly as the **" + in.Agent.Name + "**, restate the task in your own words, and ask the user one or two clarifying questions to advance the work. Keep your first reply concise. Do not produce a synthesis yet — the user must explicitly click 'Hand off'.")
		return b.String()
	}
	b.WriteString(fmt.Sprintf("# Handoff from %s\n\n", in.PrevAgent))
	b.WriteString(in.HandoffInMD)
	b.WriteString("\n\n# Original task\n\n")
	b.WriteString(in.IssueMD)
	b.WriteString("\n\n")
	b.WriteString("Introduce yourself briefly as the **" + in.Agent.Name + "**, acknowledge the prior agent's handoff, and propose what you'll do next. ")
	if in.NextAgent != "" {
		b.WriteString("The next agent will be **" + in.NextAgent + "**. ")
	} else {
		b.WriteString("You are the final stage. ")
	}
	b.WriteString("Keep your first reply concise. Do not produce a synthesis yet — the user must explicitly click 'Hand off'.")
	return b.String()
}
