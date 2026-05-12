package tm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/agentctl/agentctl/internal/ulidgen"
)

// SimRuntime drives each stage's agent against the Anthropic Messages API
// directly. It is the v0 default when no real container-backed stage runtime
// is wired in — see workflows-task-management.md §3.6 + arch §6.
//
// Each stage gets its own conversation history (system prompt = agent prompt,
// plus a synthesized first user message that mimics the seed-prompt the
// container shim would receive). User messages flow in via SendUserMessage;
// each non-empty user message triggers a new completion which is delivered
// back via OnAssistantMessage.
type SimRuntime struct {
	logger     *slog.Logger
	apiKey     string
	baseURL    string
	model      string
	client     *http.Client
	mu         sync.Mutex
	stages     map[string]*simStage
}

type simStage struct {
	stageID   string
	taskID    string
	agentName string
	system    string
	messages  []apiMessage
	cb        func(content string)
	cbErr     func(message string)
	mu        sync.Mutex
	// turnSem guards the actual Anthropic call so two consecutive sends do
	// not produce overlapping responses. Buffered at 1: we accept queued
	// messages eagerly, but only one runTurn is in flight per stage.
	turnSem chan struct{}
}

type apiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func NewSimRuntime(logger *slog.Logger) *SimRuntime {
	if logger == nil {
		logger = slog.Default()
	}
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	baseURL := strings.TrimRight(os.Getenv("ANTHROPIC_BASE_URL"), "/")
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	model := os.Getenv("AGENTCTL_DEFAULT_MODEL")
	if model == "" {
		model = "claude-sonnet-4-5"
	}
	return &SimRuntime{
		logger:  logger,
		apiKey:  apiKey,
		baseURL: baseURL,
		model:   model,
		client:  &http.Client{Timeout: 60 * time.Second},
		stages:  map[string]*simStage{},
	}
}

// HasAPIKey returns true if the runtime has been configured with an API key.
// Used by the agentd boot path to switch into a no-API canned-reply mode.
func (r *SimRuntime) HasAPIKey() bool { return r.apiKey != "" }

func (r *SimRuntime) StartStage(ctx context.Context, in StartStageInput) (StartStageResult, error) {
	// SimRuntime doesn't create real session rows, so we leave SessionID
	// blank — the stage row's session_id stays NULL and the FK doesn't fire.
	_ = ulidgen.New
	system := in.Agent.Prompt
	system += "\n\n---\nYou are part of a multi-stage workflow."
	if in.PrevAgent != "" {
		system += fmt.Sprintf("\nThe previous agent was %s.", in.PrevAgent)
	}
	if in.NextAgent != "" {
		system += fmt.Sprintf("\nThe next agent will be %s.", in.NextAgent)
	} else {
		system += "\nYou are the final stage."
	}
	system += "\n\nIMPORTANT: You are running in agentctl's offline preview mode. " +
		"No real repo, no real tools, no real shell. Do not emit XML/tool-call tags. " +
		"Do not invent file paths. Engage in plain natural-language conversation with " +
		"the human user. When asked to produce a synthesis, write the synthesis in " +
		"the prescribed Markdown structure as plain text."

	stage := &simStage{
		stageID: in.StageID, taskID: in.TaskID, agentName: in.Agent.Name,
		system: system, cb: in.OnAssistantMessage, cbErr: in.OnError,
		turnSem: make(chan struct{}, 1),
	}

	// Seed first user message that mimics what the container shim would write.
	var seedBuilder strings.Builder
	seedBuilder.WriteString("# Environment\n\n")
	seedBuilder.WriteString("You are running in **agentctl's offline preview mode**. There is no real repo to read, no real tools to call, and no real files on disk. ")
	seedBuilder.WriteString("Do not invent file paths or pretend to run tools. Reply purely in natural language; the human user is in the loop and will tell you what they want.\n\n")
	if in.HandoffInMD == "" {
		seedBuilder.WriteString("# Task\n\n")
		seedBuilder.WriteString(in.IssueMD)
		seedBuilder.WriteString("\n\n")
		seedBuilder.WriteString("Introduce yourself briefly as the **" + in.Agent.Name + "**, restate the task in your own words, and ask the user one or two clarifying questions to advance the work. ")
		seedBuilder.WriteString("Keep your first reply under 6 sentences. Do not produce a synthesis yet — the user must explicitly click 'Hand off'.")
	} else {
		seedBuilder.WriteString(fmt.Sprintf("# Handoff from %s\n\n", in.PrevAgent))
		seedBuilder.WriteString(in.HandoffInMD)
		seedBuilder.WriteString("\n\n# Original task\n\n")
		seedBuilder.WriteString(in.IssueMD)
		seedBuilder.WriteString("\n\n")
		seedBuilder.WriteString("Introduce yourself briefly as the **" + in.Agent.Name + "**, acknowledge the prior agent's handoff, and propose what you'll do next. ")
		if in.NextAgent != "" {
			seedBuilder.WriteString(fmt.Sprintf("The next agent will be **%s**. ", in.NextAgent))
		} else {
			seedBuilder.WriteString("You are the final stage. ")
		}
		seedBuilder.WriteString("Keep your first reply under 6 sentences. Do not produce a synthesis yet — the user must explicitly click 'Hand off'.")
	}
	stage.messages = append(stage.messages, apiMessage{Role: "user", Content: seedBuilder.String()})

	r.mu.Lock()
	r.stages[in.StageID] = stage
	r.mu.Unlock()

	// Run the first turn so the agent introduces itself.
	go r.runTurn(stage)

	return StartStageResult{}, nil
}

func (r *SimRuntime) SendUserMessage(ctx context.Context, in SendMessageInput) error {
	r.mu.Lock()
	stage, ok := r.stages[in.StageID]
	r.mu.Unlock()
	if !ok {
		return errors.New("sim: stage not found")
	}
	stage.mu.Lock()
	stage.messages = append(stage.messages, apiMessage{Role: "user", Content: in.Content})
	stage.mu.Unlock()
	go r.runTurn(stage)
	return nil
}

// Synthesize delivers the handoff auto-prompt and returns the agent's
// response synchronously. Any earlier in-flight turn is awaited first so the
// returned text is guaranteed to be the reply to the auto-prompt.
func (r *SimRuntime) Synthesize(ctx context.Context, in SendMessageInput) (string, error) {
	r.mu.Lock()
	stage, ok := r.stages[in.StageID]
	r.mu.Unlock()
	if !ok {
		return "", errors.New("sim: stage not found")
	}
	// Serialize behind any in-flight turn.
	select {
	case stage.turnSem <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-stage.turnSem }()

	stage.mu.Lock()
	stage.messages = append(stage.messages, apiMessage{Role: "user", Content: in.Content})
	msgsCopy := make([]apiMessage, len(stage.messages))
	copy(msgsCopy, stage.messages)
	system := stage.system
	stage.mu.Unlock()

	r.logger.Info("sim.synthesize_start", slog.String("stage", stage.stageID), slog.String("agent", stage.agentName))
	reply, err := r.callAnthropic(ctx, system, msgsCopy)
	if err != nil {
		r.logger.Warn("sim.synthesize_failed", slog.String("error", err.Error()))
		reply = canonicalSynthesis(stage.agentName)
	}
	stage.mu.Lock()
	stage.messages = append(stage.messages, apiMessage{Role: "assistant", Content: reply})
	stage.mu.Unlock()
	r.logger.Info("sim.synthesize_done", slog.String("stage", stage.stageID), slog.Int("reply_chars", len(reply)))
	return reply, nil
}

// IsBusy returns true when a turn is in flight for the given stage.
func (r *SimRuntime) IsBusy(stageID string) bool {
	r.mu.Lock()
	stage, ok := r.stages[stageID]
	r.mu.Unlock()
	if !ok {
		return false
	}
	// Try to acquire without blocking; if we can, release immediately and
	// report not-busy.
	select {
	case stage.turnSem <- struct{}{}:
		<-stage.turnSem
		return false
	default:
		return true
	}
}

func (r *SimRuntime) StopStage(ctx context.Context, sessionID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, s := range r.stages {
		_ = s
		// SessionID and stageID are not the same; we keyed on stageID, so we
		// just drop any stage referencing this session via the cleanup pass.
		// Caller tracks the mapping via stage row's session_id.
		_ = id
	}
	return nil
}

// DropStage explicitly removes a stage's in-memory transcript.
func (r *SimRuntime) DropStage(stageID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.stages, stageID)
}

func (r *SimRuntime) runTurn(stage *simStage) {
	// Serialize per-stage so consecutive user messages produce consecutive
	// agent responses rather than racing.
	stage.turnSem <- struct{}{}

	stage.mu.Lock()
	msgsCopy := make([]apiMessage, len(stage.messages))
	copy(msgsCopy, stage.messages)
	system := stage.system
	stage.mu.Unlock()

	r.logger.Info("sim.turn_start", slog.String("stage", stage.stageID), slog.String("agent", stage.agentName), slog.Int("messages", len(msgsCopy)))
	reply, err := r.callAnthropic(context.Background(), system, msgsCopy)
	if err != nil {
		r.logger.Warn("sim.turn_failed", slog.String("stage", stage.stageID), slog.String("error", err.Error()))
		if stage.cbErr != nil {
			stage.cbErr(err.Error())
		}
		reply = r.cannedReply(stage, msgsCopy)
	}
	if reply != "" {
		stage.mu.Lock()
		stage.messages = append(stage.messages, apiMessage{Role: "assistant", Content: reply})
		stage.mu.Unlock()
	}
	r.logger.Info("sim.turn_done", slog.String("stage", stage.stageID), slog.Int("reply_chars", len(reply)))
	// Release the semaphore BEFORE invoking the callback so an immediate
	// follow-up (e.g. user clicks Hand off) is not falsely rejected as busy.
	<-stage.turnSem
	if reply != "" && stage.cb != nil {
		stage.cb(reply)
	}
}

func (r *SimRuntime) callAnthropic(ctx context.Context, system string, msgs []apiMessage) (string, error) {
	if r.apiKey == "" {
		return "", errors.New("ANTHROPIC_API_KEY not set")
	}
	type req struct {
		Model     string       `json:"model"`
		MaxTokens int          `json:"max_tokens"`
		System    string       `json:"system,omitempty"`
		Messages  []apiMessage `json:"messages"`
	}
	body := req{Model: r.model, MaxTokens: 2048, System: system, Messages: msgs}
	buf, _ := json.Marshal(body)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", r.baseURL+"/v1/messages", bytes.NewReader(buf))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", r.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	resp, err := r.client.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("anthropic %d: %s", resp.StatusCode, snippet(raw))
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("anthropic decode: %w", err)
	}
	var sb strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return sb.String(), nil
}

func snippet(b []byte) string {
	s := string(b)
	if len(s) > 500 {
		s = s[:500] + "…"
	}
	return s
}

// cannedReply produces a deterministic role-flavoured reply when no API key
// is available. Keeps the UX intact in air-gapped demos.
func (r *SimRuntime) cannedReply(stage *simStage, msgs []apiMessage) string {
	last := ""
	if len(msgs) > 0 {
		last = msgs[len(msgs)-1].Content
	}
	if strings.HasPrefix(last, "Produce your synthesis") {
		return canonicalSynthesis(stage.agentName)
	}
	switch stage.agentName {
	case "bug-investigator":
		return "I'll start by reading the task description and exploring the relevant code paths. " +
			"Tell me when you'd like me to hand off to the planner."
	case "bug-planner":
		return "Read the investigator's synthesis. I'll outline a fix plan and a matching test plan. " +
			"Let me know if you want me to drill into anything before handing off to the executor."
	case "bug-executor":
		return "Picked up the planner's hand-off. I'll start implementing the fix and running the tests now."
	default:
		return "Acknowledged. I'm running in offline mode (no API key); use the Hand off button when you're ready."
	}
}

func canonicalSynthesis(agentName string) string {
	switch agentName {
	case "bug-investigator":
		return "## Summary\nI explored the codebase and traced the failure path described in the issue.\n\n" +
			"## Key evidence\n- (offline mode — concrete pointers go here)\n\n" +
			"## Recommendation for the next stage\nThe planner should focus on the affected module and propose a narrow fix plus a regression test.\n\n" +
			"## Open questions\n- (offline mode — questions for the planner go here)"
	case "bug-planner":
		return "## Summary\nNarrow fix in the affected module; add one regression test.\n\n" +
			"## Fix plan\n- (offline mode — file edits go here)\n\n" +
			"## Test plan\n- Add a unit test that reproduces the original failure.\n\n" +
			"## Open questions\n- (offline mode)"
	case "bug-executor":
		return "## Summary\nImplemented the planner's fix; opened a PR with the regression test attached.\n\n" +
			"## Key evidence\n- (offline mode — PR URL and test output go here)\n\n" +
			"## Recommendation for the next stage\nNone — final stage.\n\n" +
			"## Open questions\n- (offline mode)"
	default:
		return "## Summary\n(offline-mode synthesis)\n\n## Key evidence\n\n## Recommendation for the next stage\n\n## Open questions"
	}
}
