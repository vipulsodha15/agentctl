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

	"github.com/agentctl/agentctl/internal/secrets"
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
	auth       *AuthSource
	defaultURL string
	model      string
	client     *http.Client
	mu         sync.Mutex
	stages     map[string]*simStage
}

// AuthSource resolves an Anthropic credential at call time so that:
//  1. `agentctl init` rewriting secrets.json picks up without a daemon
//     restart.
//  2. `agentctl auth login` refreshing the OAuth credentials file is
//     observed on the next call.
//
// Resolution order (first non-empty wins):
//  1. secrets.json AnthropicAuthToken (+ AnthropicBaseURL) → Bearer
//  2. secrets.json AnthropicAPIKey → x-api-key
//  3. OAuth credentials file at ClaudeCredsFile → Bearer
//  4. env var ANTHROPIC_API_KEY → x-api-key (test/CI fallback)
type AuthSource struct {
	SecretsPath string
	CredsFile   string
}

type resolvedAuth struct {
	kind    string // "x-api-key" | "bearer"
	value   string
	baseURL string // overrides default if set
	source  string // human-readable origin for logs/errors
	// oauth is true only when value is a Claude subscription OAuth access
	// token read from .credentials.json. Such tokens require the
	// `anthropic-beta: oauth-2025-04-20` header and a system prompt that
	// starts with the Claude Code identity string — otherwise Anthropic
	// rejects the request (typically as a 429 rate_limit_error with a
	// generic "Error" message). A custom-endpoint bearer from secrets.json
	// is a plain proxy token and must NOT trigger that path.
	oauth bool
}

// resolve reads disk on every call. Cheap: secrets.json is small and we only
// hit it on user-initiated turns.
func (a *AuthSource) resolve(defaultURL string) (resolvedAuth, error) {
	if a != nil && a.SecretsPath != "" {
		if sec, err := secrets.Load(a.SecretsPath); err == nil {
			if sec.AnthropicAuthToken != "" {
				base := sec.AnthropicBaseURL
				if base == "" {
					base = defaultURL
				}
				return resolvedAuth{kind: "bearer", value: sec.AnthropicAuthToken, baseURL: base, source: "secrets.json (auth_token)"}, nil
			}
			if sec.AnthropicAPIKey != "" {
				return resolvedAuth{kind: "x-api-key", value: sec.AnthropicAPIKey, baseURL: defaultURL, source: "secrets.json (api_key)"}, nil
			}
			if sec.ResolvedAuthMode() == secrets.AuthModeOAuth && a.CredsFile != "" {
				if tok, err := readOAuthAccessToken(a.CredsFile); err == nil && tok != "" {
					return resolvedAuth{kind: "bearer", value: tok, baseURL: defaultURL, source: "oauth credentials file", oauth: true}, nil
				}
			}
		}
	}
	if env := os.Getenv("ANTHROPIC_API_KEY"); env != "" {
		base := strings.TrimRight(os.Getenv("ANTHROPIC_BASE_URL"), "/")
		if base == "" {
			base = defaultURL
		}
		return resolvedAuth{kind: "x-api-key", value: env, baseURL: base, source: "env (ANTHROPIC_API_KEY)"}, nil
	}
	return resolvedAuth{}, errors.New("no Anthropic credential configured — run `agentctl init` to add an API key, `agentctl auth login` for OAuth, or set ANTHROPIC_API_KEY")
}

// readOAuthAccessToken pulls the bearer token out of the credentials.json
// the bundled `claude` CLI writes after `agentctl auth login`. The shape
// looks like:
//
//	{ "claudeAiOauth": { "accessToken": "sk-ant-oat...", ... } }
//
// We tolerate alternative key names that older builds of the CLI may have
// emitted (accessToken, oauth_access_token), so a credentials file written
// by a slightly different claude version still works.
func readOAuthAccessToken(path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", err
	}
	if v, ok := raw["claudeAiOauth"].(map[string]any); ok {
		if tok, ok := v["accessToken"].(string); ok && tok != "" {
			return tok, nil
		}
	}
	for _, k := range []string{"accessToken", "access_token", "oauth_access_token"} {
		if v, ok := raw[k].(string); ok && v != "" {
			return v, nil
		}
	}
	return "", errors.New("no accessToken in credentials file")
}

// SimRuntimeOptions configures NewSimRuntime. Production wiring populates
// Auth from the agentd layout so the runtime tracks daemon-managed secrets
// rather than its own env-var path.
type SimRuntimeOptions struct {
	Logger *slog.Logger
	Auth   *AuthSource
	Model  string
}

func NewSimRuntime(logger *slog.Logger) *SimRuntime {
	return NewSimRuntimeWithOptions(SimRuntimeOptions{Logger: logger})
}

func NewSimRuntimeWithOptions(opts SimRuntimeOptions) *SimRuntime {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	model := opts.Model
	if model == "" {
		model = os.Getenv("AGENTCTL_DEFAULT_MODEL")
	}
	if model == "" {
		model = "claude-sonnet-4-5"
	}
	defaultURL := strings.TrimRight(os.Getenv("ANTHROPIC_BASE_URL"), "/")
	if defaultURL == "" {
		defaultURL = "https://api.anthropic.com"
	}
	return &SimRuntime{
		logger:     opts.Logger,
		auth:       opts.Auth,
		defaultURL: defaultURL,
		model:      model,
		client:     &http.Client{Timeout: 60 * time.Second},
		stages:     map[string]*simStage{},
	}
}

// HasCredential returns true if at least one credential source resolves now.
// Used by the agentd boot path to log whether the runtime can actually talk
// to Anthropic at start time.
func (r *SimRuntime) HasCredential() bool {
	_, err := r.resolveAuth()
	return err == nil
}

func (r *SimRuntime) resolveAuth() (resolvedAuth, error) {
	if r.auth != nil {
		return r.auth.resolve(r.defaultURL)
	}
	// No AuthSource wired — fall back to env-only.
	stub := &AuthSource{}
	return stub.resolve(r.defaultURL)
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
	// authErrOnce ensures the "no credential configured" banner is surfaced
	// only once per stage rather than on every turn.
	authErrOnce sync.Once
}

// isAuthConfigError is true if err is the synthetic "no credential
// configured" sentinel from resolveAuth, as opposed to a real network or
// HTTP-4xx error from Anthropic.
func isAuthConfigError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "no Anthropic credential configured")
}

type apiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (r *SimRuntime) StartStage(ctx context.Context, in StartStageInput) (StartStageResult, error) {
	// SimRuntime doesn't create real session rows, so we leave SessionID
	// blank — the stage row's session_id stays NULL and the FK doesn't fire.
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
		if !isAuthConfigError(err) && stage.cbErr != nil {
			stage.cbErr("Synthesis call failed: " + err.Error())
		}
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
		// Only surface real API errors inline as a red bubble. Auth-config
		// errors are reported once per stage (suppressed afterwards) so the
		// chat doesn't fill up with the same banner on every turn.
		if !isAuthConfigError(err) && stage.cbErr != nil {
			stage.cbErr(err.Error())
		} else if isAuthConfigError(err) && stage.cbErr != nil {
			stage.authErrOnce.Do(func() {
				stage.cbErr("Anthropic auth is not configured. Run `agentctl init` to add an API key, or `agentctl auth login` for an OAuth subscription. While unconfigured, the agent will use canned replies.")
			})
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

// claudeCodeSystemPrefix is the identity string Anthropic requires at the
// start of the system prompt when authenticating with a Claude OAuth
// subscription bearer token. Without it the API rejects the request — most
// commonly as a 429 rate_limit_error with a generic "Error" message rather
// than an obvious 401, which makes the failure mode easy to misread as a
// real rate limit.
const claudeCodeSystemPrefix = "You are Claude Code, Anthropic's official CLI for Claude."

func (r *SimRuntime) callAnthropic(ctx context.Context, system string, msgs []apiMessage) (string, error) {
	auth, err := r.resolveAuth()
	if err != nil {
		return "", err
	}
	if auth.oauth {
		// OAuth subscription tokens are gated on the Claude Code identity
		// being the first thing in the system prompt. Prepend rather than
		// replace so the agent's own role/framing still shapes the reply.
		if system == "" {
			system = claudeCodeSystemPrefix
		} else if !strings.HasPrefix(system, claudeCodeSystemPrefix) {
			system = claudeCodeSystemPrefix + "\n\n" + system
		}
	}
	type req struct {
		Model     string       `json:"model"`
		MaxTokens int          `json:"max_tokens"`
		System    string       `json:"system,omitempty"`
		Messages  []apiMessage `json:"messages"`
	}
	body := req{Model: r.model, MaxTokens: 2048, System: system, Messages: msgs}
	buf, _ := json.Marshal(body)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", auth.baseURL+"/v1/messages", bytes.NewReader(buf))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if auth.kind == "bearer" {
		httpReq.Header.Set("Authorization", "Bearer "+auth.value)
	} else {
		httpReq.Header.Set("x-api-key", auth.value)
	}
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	if auth.oauth {
		httpReq.Header.Set("anthropic-beta", "oauth-2025-04-20")
	}
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
