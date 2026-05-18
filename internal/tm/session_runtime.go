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

// ProviderResolver mirrors socksrv.ProviderResolver — see ADR 0020 §3.
// SessionRuntime calls it for each stage so an agent's `provider:` /
// `model:` are merged with config defaults and workspace sticky state
// through the same algorithm CLI/web go through. When nil (legacy tests),
// the agent's fields pass straight to sm.Create; the manager will reject
// them if Provider is empty.
type ProviderResolver func(cliProvider, cliModel string) (provider, model string, err error)

// SessionRuntime drives each task-chat stage as a real container-backed
// session via sm.Manager, instead of POSTing to Anthropic directly. Each
// stage is one fresh session: own container, own empty workspace volume,
// agent's prompt as the SDK's system_prompt, agent's MCPs allowed, no
// repos cloned. When the user clicks "Hand off" we send the synthesis
// auto-prompt and lock onto the assistant reply that corresponds to it
// (via message-id → turn-id correlation), so an unrelated in-flight reply
// can't be mistaken for the synthesis.
type SessionRuntime struct {
	sm      SessionAPI
	resolve ProviderResolver
	logger  *slog.Logger

	mu     sync.Mutex
	stages map[string]*sessionStage // keyed by stage ID
}

type sessionStage struct {
	stageID   string
	sessionID string

	cbAssistant func(content string)
	cbTool      func(toolUseID, tool string, input json.RawMessage)
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

// WithResolver wires the (provider, model) resolver used per stage. The
// resolver is decoupled from NewSessionRuntime so the existing test
// fixtures that construct SessionRuntime without it keep compiling; in
// production agentd calls this after NewSessionRuntime.
func (r *SessionRuntime) WithResolver(resolve ProviderResolver) *SessionRuntime {
	r.resolve = resolve
	return r
}

// HasCredential mirrors SimRuntime's startup-time credential probe. With the
// session-backed runtime, auth lives in sm.Manager (via the bundled claude
// CLI + bind-mounted credentials file), so we always report true and let
// session creation surface any auth failure at call time.
func (r *SessionRuntime) HasCredential() bool { return true }

func (r *SessionRuntime) StartStage(ctx context.Context, in StartStageInput) (StartStageResult, error) {
	// Per-stage assembly-line pins take precedence over agent-level pins
	// (ADR 0020 §3 — mixed-provider lines). The same agent YAML can run on
	// different providers in different lines without forking, because the
	// line's stage entry overrides what the agent itself says.
	provider := in.StageProvider
	if provider == "" {
		provider = in.Agent.Provider
	}
	model := in.StageModel
	if model == "" {
		model = in.Agent.Model
	}
	if r.resolve != nil {
		// Funnel the agent's hints through the resolver so workspace-
		// sticky last-used-provider and per-provider default models
		// apply (ADR 0020 §3). When neither the stage nor the agent
		// pins either field, the resolver picks the workspace's
		// currently active provider + its configured default model —
		// the portability property that lets built-in agent YAMLs run
		// on whichever provider the user has configured.
		p, m, rerr := r.resolve(provider, model)
		if rerr != nil {
			return StartStageResult{}, fmt.Errorf("session runtime: resolve: %w", rerr)
		}
		provider = p
		model = m
	}
	res, err := r.sm.Create(ctx, sm.CreateRequest{
		Name:     fmt.Sprintf("task-%s-stage-%d", in.TaskID, in.Position),
		MCPs:     in.Agent.MCPsAllowed,
		Model:    model,
		Provider: provider,
		Repos:    nil,
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
func (r *SessionRuntime) attach(ctx context.Context, stageID, sessionID string, cbAssistant func(string), cbTool func(string, string, json.RawMessage), cbErr func(string)) (*sessionStage, error) {
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
			// The shim emits {turn_id, tool_use_id, name, input} but
			// proto.ToolCallData uses the older `tool` tag, so we decode
			// locally to pick up the populated `name` field. See the same
			// pattern in internal/cli/tui/model.go.
			var d struct {
				TurnID    string          `json:"turn_id"`
				ToolUseID string          `json:"tool_use_id"`
				Name      string          `json:"name"`
				Tool      string          `json:"tool"`
				Input     json.RawMessage `json:"input,omitempty"`
			}
			_ = json.Unmarshal(ev.Data, &d)
			name := d.Name
			if name == "" {
				name = d.Tool
			}
			s.cbTool(d.ToolUseID, name, d.Input)
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
// agent's own prompt, the multi-stage assembly line framing, and either the
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
	b.WriteString("\n\n---\nYou are part of a multi-stage assembly line.")
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
