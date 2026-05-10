package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/agentctl/agentctl/internal/cliclient"
	"github.com/agentctl/agentctl/internal/proto"
)

type streamRenderer struct {
	stdout io.Writer
	stderr io.Writer
	open   bool
}

func newRenderer(stdout, stderr io.Writer) *streamRenderer {
	return &streamRenderer{stdout: stdout, stderr: stderr}
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
		fmt.Fprintf(r.stderr, "[session %s status=%s queue=%d in_flight=%t]\n",
			d.Session.ID, d.Session.Status, d.QueueDepth, d.InFlight != "")
	case proto.EventSessionStarting:
		var d struct {
			Phase string `json:"phase"`
		}
		_ = json.Unmarshal(ev.Data, &d)
		fmt.Fprintf(r.stderr, "[starting: %s]\n", d.Phase)
	case proto.EventSessionRunning:
		fmt.Fprintln(r.stderr, "[running]")
	case proto.EventSessionStopping:
		fmt.Fprintln(r.stderr, "[stopping]")
	case proto.EventSessionStopped:
		fmt.Fprintln(r.stderr, "[stopped]")
	case proto.EventSessionTerminated:
		fmt.Fprintln(r.stderr, "[terminated]")
	case proto.EventSessionError:
		fmt.Fprintf(r.stderr, "[error] %s\n", string(ev.Data))
	case proto.EventUserMessage:
		var d proto.UserMessageData
		_ = json.Unmarshal(ev.Data, &d)
		r.closeAssistantLine()
		fmt.Fprintf(r.stdout, "user: %s\n", d.Content)
	case proto.EventTurnStart:
		r.closeAssistantLine()
		fmt.Fprint(r.stdout, "assistant: ")
		r.open = true
	case proto.EventAssistantDelta:
		var d proto.AssistantDeltaData
		_ = json.Unmarshal(ev.Data, &d)
		fmt.Fprint(r.stdout, d.Delta)
	case proto.EventAssistantMessage:
		// final text already streamed via deltas; close the line.
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
	default:
	}
}

func attachAndRender(ctx context.Context, c *cliclient.Client, sessionID string, env *Env) int {
	stream, err := c.StartStream(proto.OpAttachStream, proto.AttachStreamRequest{SessionID: sessionID})
	if err != nil {
		fmt.Fprintf(env.Stderr, "attach: %v\n", err)
		return ExitGeneric
	}
	r := newRenderer(env.Stdout, env.Stderr)
	for {
		select {
		case <-ctx.Done():
			stream.Close()
			return ExitOK
		default:
		}
		fr := stream.Recv()
		if fr.Err != nil {
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
