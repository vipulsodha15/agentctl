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
	res, err := r.sm.Create(ctx, sm.CreateRequest{
		Name:  fmt.Sprintf("task-%s-stage-%d", in.TaskID, in.Position),
		MCPs:  in.Agent.MCPsAllowed,
		Model: in.Agent.Model,
		Repos: nil,
	})
	if err != nil {
		return StartStageResult{}, fmt.Errorf("session runtime: create: %w", err)
	}

	stream, err := r.sm.Attach(ctx, res.SessionID)
	if err != nil {
		_ = r.sm.Terminate(context.Background(), res.SessionID)
		return StartStageResult{}, fmt.Errorf("session runtime: attach: %w", err)
	}

	stage := &sessionStage{
		stageID:     in.StageID,
		sessionID:   res.SessionID,
		cbAssistant: in.OnAssistantMessage,
		cbTool:      in.OnToolUse,
		cbErr:       in.OnError,
		stream:      stream,
	}
	r.mu.Lock()
	r.stages[in.StageID] = stage
	r.mu.Unlock()

	go r.runReader(stage)

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

func (r *SessionRuntime) SendUserMessage(ctx context.Context, in SendMessageInput) error {
	stage, err := r.lookupStage(in.StageID)
	if err != nil {
		return err
	}
	_, err = r.sm.Send(ctx, sm.SendRequest{
		SessionID: stage.sessionID,
		Content:   in.Content,
		ClientID:  in.StageID,
	})
	return err
}

func (r *SessionRuntime) Synthesize(ctx context.Context, in SendMessageInput) (string, error) {
	stage, err := r.lookupStage(in.StageID)
	if err != nil {
		return "", err
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
		return nil, errors.New("session runtime: stage not found")
	}
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

// buildStageSeedMessage composes the first user message in the new stage's
// session. It carries everything the agent needs to take on its role: the
// agent's own prompt, the multi-stage workflow framing, and either the
// original task brief (stage 1) or the prior stage's synthesis (stage N>1).
//
// We deliberately do NOT set sm.CreateRequest.SystemPrompt: passing a custom
// string to ClaudeAgentOptions(system_prompt=…) switches the SDK out of its
// Claude Code preset, which is the only mode that writes the JSONL
// transcript the shim mirrors into SQLite. Without that mirror, refresh
// loses history. So the agent role lives in the seed message instead and
// the SDK stays in Claude Code mode.
func buildStageSeedMessage(in StartStageInput) string {
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
	b.WriteString("\n\n---\n\n")

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
