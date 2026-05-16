package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/agentctl/agentctl/internal/cli/tui"
	"github.com/agentctl/agentctl/internal/cliclient"
	"github.com/agentctl/agentctl/internal/proto"
)

type streamRenderer struct {
	stdout    io.Writer
	stderr    io.Writer
	open      bool
	deltaSeen bool

	// provider/model are captured from the first session.snapshot event so
	// later status lines (session.starting / session.running / stage
	// transitions) can be prefixed with [provider/model] when more than
	// one provider is in play (ADR 0020 §3 — orchestration as the
	// headline). Empty when the daemon hasn't broadcast a snapshot yet
	// or when the session predates the provider field (legacy rows).
	provider string
	model    string
	// showRuntime gates the [provider/model] prefix per ADR 0020 §UX
	// principles (provider invisibility). True when the runtime caller
	// has signalled that at least two providers are in play — either
	// configured on the daemon or surfaced across mixed-provider stages
	// of an assembly line. Single-provider setups see the legacy output.
	showRuntime bool
}

func newRenderer(stdout, stderr io.Writer) *streamRenderer {
	return &streamRenderer{stdout: stdout, stderr: stderr}
}

// withRuntimeVisible turns on the [provider/model] prefix on status lines.
// Callers compute the gate at the call site (e.g. by inspecting
// secrets.EnabledProviders or the resolved per-stage provider set across an
// assembly line). The renderer itself stays dumb about the gate so tests
// can flip it explicitly.
func (r *streamRenderer) withRuntimeVisible(show bool) *streamRenderer {
	r.showRuntime = show
	return r
}

// runtimePrefix builds the short "[provider/model] " prefix used on session
// status lines and stage transitions. Returns the empty string when the
// gate is off or when neither field is known yet, so callers can prepend it
// unconditionally without printing an empty bracket.
func (r *streamRenderer) runtimePrefix() string {
	if !r.showRuntime {
		return ""
	}
	switch {
	case r.provider != "" && r.model != "":
		return "[" + r.provider + "/" + r.model + "] "
	case r.provider != "":
		return "[" + r.provider + "] "
	case r.model != "":
		return "[" + r.model + "] "
	}
	return ""
}

func (r *streamRenderer) closeAssistantLine() {
	if r.open {
		fmt.Fprintln(r.stdout, "")
		r.open = false
	}
}

func (r *streamRenderer) handle(ev proto.Event) {
	switch ev.Kind {
	case proto.EventSessionSnapshot:
		var d proto.SessionSnapshotData
		_ = json.Unmarshal(ev.Data, &d)
		// Capture provider/model so subsequent session.* status lines and
		// any task.stage_advanced events can be prefixed with
		// [provider/model] when multi-provider visibility is on (ADR
		// 0020 §3, §UX principles). The snapshot is broadcast on attach,
		// so this fires before any status transitions in practice.
		r.provider = d.Session.Provider
		r.model = d.Session.Model
		fmt.Fprintf(r.stderr, "%s[session %s status=%s queue=%d in_flight=%t]\n",
			r.runtimePrefix(), d.Session.ID, d.Session.Status, d.QueueDepth, d.InFlight != "")
	case proto.EventSessionStarting:
		var d struct {
			Phase string `json:"phase"`
		}
		_ = json.Unmarshal(ev.Data, &d)
		fmt.Fprintf(r.stderr, "%s[starting: %s]\n", r.runtimePrefix(), d.Phase)
	case proto.EventSessionRunning:
		fmt.Fprintf(r.stderr, "%s[running]\n", r.runtimePrefix())
	case proto.EventSessionStopping:
		fmt.Fprintf(r.stderr, "%s[stopping]\n", r.runtimePrefix())
	case proto.EventSessionStopped:
		fmt.Fprintf(r.stderr, "%s[stopped]\n", r.runtimePrefix())
	case proto.EventSessionTerminated:
		fmt.Fprintf(r.stderr, "%s[terminated]\n", r.runtimePrefix())
	case proto.EventSessionError:
		fmt.Fprintf(r.stderr, "%s[error] %s\n", r.runtimePrefix(), string(ev.Data))
	case proto.EventUserMessage:
		var d proto.UserMessageData
		_ = json.Unmarshal(ev.Data, &d)
		r.closeAssistantLine()
		fmt.Fprintf(r.stdout, "user: %s\n", d.Content)
	case proto.EventTurnStart:
		r.closeAssistantLine()
		fmt.Fprint(r.stdout, "assistant: ")
		r.open = true
		r.deltaSeen = false
	case proto.EventAssistantDelta:
		var d proto.AssistantDeltaData
		_ = json.Unmarshal(ev.Data, &d)
		if d.Delta != "" {
			fmt.Fprint(r.stdout, d.Delta)
			r.deltaSeen = true
		}
	case proto.EventAssistantMessage:
		// Final text — usually already streamed via deltas; print it here if
		// no deltas arrived (older runtimes that don't stream partials, or a
		// future shim that opts out of include_partial_messages).
		var d proto.AssistantMessageData
		_ = json.Unmarshal(ev.Data, &d)
		if !r.deltaSeen && d.Content != "" {
			fmt.Fprint(r.stdout, d.Content)
		}
		r.closeAssistantLine()
	case proto.EventTurnEnd:
		r.closeAssistantLine()
	case proto.EventTurnCancelled:
		r.closeAssistantLine()
		fmt.Fprintln(r.stderr, "[turn cancelled]")
	case proto.EventToolCall:
		var d proto.ToolCallData
		_ = json.Unmarshal(ev.Data, &d)
		r.closeAssistantLine()
		fmt.Fprintf(r.stdout, "tool> %s %s\n", d.Tool, string(d.Input))
	case proto.EventToolResult:
		var d proto.ToolResultData
		_ = json.Unmarshal(ev.Data, &d)
		r.closeAssistantLine()
		marker := "ok"
		if d.IsError {
			marker = "err"
		}
		fmt.Fprintf(r.stdout, "tool< %s [%s] %s\n", d.Tool, marker, string(d.Output))
	case proto.EventQueueDepth:
		var d proto.QueueDepthData
		_ = json.Unmarshal(ev.Data, &d)
		fmt.Fprintf(r.stderr, "[queue depth: %d]\n", d.Depth)
	case proto.EventUsage:
		var d proto.UsageData
		_ = json.Unmarshal(ev.Data, &d)
		fmt.Fprintf(r.stderr, "[usage: in=%d out=%d cache_r=%d cache_w=%d]\n",
			d.InputTokens, d.OutputTokens, d.CacheReadTokens, d.CacheWriteTokens)
	case proto.EventMCPUnreachable:
		fmt.Fprintf(r.stderr, "[mcp unreachable] %s\n", string(ev.Data))
	case "task.stage_advanced":
		// Stage transitions surface on the task channel (not the session
		// channel attached here), but render them anyway so a future
		// task-tail mode reuses the same renderer without extra wiring.
		// The [provider/model] prefix is gated the same way the session
		// status lines are — see ADR 0020 §3, §UX principles.
		var d struct {
			FromStageID string `json:"from_stage_id"`
			ToStageID   string `json:"to_stage_id"`
		}
		_ = json.Unmarshal(ev.Data, &d)
		fmt.Fprintf(r.stderr, "%s[stage advanced: %s -> %s]\n",
			r.runtimePrefix(), d.FromStageID, d.ToStageID)
	default:
	}
}

func attachAndRender(ctx context.Context, c *cliclient.Client, sessionID string, env *Env) int {
	stream, err := c.StartStream(proto.OpAttachStream, proto.AttachStreamRequest{SessionID: sessionID})
	if err != nil {
		fmt.Fprintf(env.Stderr, "attach: %v\n", err)
		return ExitGeneric
	}
	// stream.Recv blocks in a unix-socket read; the only reliable way to
	// unblock it on Ctrl+C is to close the underlying connection so the read
	// returns an error.
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		select {
		case <-ctx.Done():
			_ = c.Close()
		case <-stopWatch:
		}
	}()
	r := newRenderer(env.Stdout, env.Stderr).
		withRuntimeVisible(multiProviderEnabled(env))
	for {
		fr := stream.Recv()
		if fr.Err != nil {
			if ctx.Err() != nil {
				return ExitOK
			}
			fmt.Fprintf(env.Stderr, "attach: %v\n", fr.Err)
			return ExitGeneric
		}
		if fr.EndCode != "" {
			r.closeAssistantLine()
			fmt.Fprintf(env.Stderr, "[stream end: %s]\n", fr.EndCode)
			return ExitOK
		}
		var ev proto.Event
		if err := json.Unmarshal(fr.Data, &ev); err != nil {
			fmt.Fprintf(env.Stderr, "attach: malformed event: %v\n", err)
			continue
		}
		r.handle(ev)
	}
}

// multiProviderEnabled reports whether the user has at least two providers
// configured locally. The stream renderer uses this as the gate for the
// per-session [provider/model] prefix on status lines (ADR 0020 §UX
// principles — provider invisibility for single-provider setups). The
// gate intentionally errs toward hidden: when secrets.json is unreadable
// or only one provider resolves, the legacy unprefixed output wins.
func multiProviderEnabled(env *Env) bool {
	return localEnabledProviderCount(env) >= 2
}

// attachAndRunTUI runs the fullscreen Bubble Tea TUI. It owns its own RPC
// sender (separate connection from `c`, the attach-stream conn).
func attachAndRunTUI(ctx context.Context, c *cliclient.Client, sessionID string, env *Env) int {
	sender := newRPCSender(env.Layout.SocketFile, sessionID)
	defer sender.Close()
	code := tui.Run(ctx, c, sessionID, sender, env.Stderr)
	if code == 0 {
		return ExitOK
	}
	return ExitGeneric
}
