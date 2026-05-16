package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/agentctl/agentctl/internal/proto"
	"github.com/agentctl/agentctl/internal/tm"
)

// TestRenderer_StatusLinesGateOnRuntimeVisibility — when the renderer is
// configured with multi-provider visibility (ADR 0020 §3, §UX
// principles), session.* status lines are prefixed with [provider/model].
// When the gate is off (single-provider setup), output stays byte-for-
// byte identical to the pre-ADR 0020 renderer.
func TestRenderer_StatusLinesGateOnRuntimeVisibility(t *testing.T) {
	t.Run("gate off — no prefix", func(t *testing.T) {
		stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
		r := newRenderer(stdout, stderr) // visibility defaults to false
		feedSnapshot(t, r, "anthropic", "claude-sonnet-4-6")
		r.handle(proto.Event{Kind: proto.EventSessionRunning})
		got := stderr.String()
		if strings.Contains(got, "[anthropic") {
			t.Errorf("single-provider mode must not surface a runtime prefix; got %q", got)
		}
		if !strings.Contains(got, "[running]") {
			t.Errorf("expected legacy [running] line; got %q", got)
		}
	})

	t.Run("gate on — prefix on snapshot and status lines", func(t *testing.T) {
		stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
		r := newRenderer(stdout, stderr).withRuntimeVisible(true)
		feedSnapshot(t, r, "openai", "gpt-5.5")
		r.handle(proto.Event{Kind: proto.EventSessionRunning})
		r.handle(proto.Event{Kind: proto.EventSessionStopped})
		got := stderr.String()
		// Each gated line carries the runtime prefix.
		wantSubs := []string{
			"[openai/gpt-5.5] [session ",
			"[openai/gpt-5.5] [running]",
			"[openai/gpt-5.5] [stopped]",
		}
		for _, want := range wantSubs {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in output:\n%s", want, got)
			}
		}
	})
}

// TestRenderer_StageAdvancedCarriesPrefix — the task.stage_advanced event
// is rendered with the same [provider/model] prefix as session.* status
// lines, so a tail of a mixed-provider assembly-line run shows the
// runtime change-over inline.
func TestRenderer_StageAdvancedCarriesPrefix(t *testing.T) {
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	r := newRenderer(stdout, stderr).withRuntimeVisible(true)
	feedSnapshot(t, r, "anthropic", "claude-opus-4-7")
	data, _ := json.Marshal(map[string]string{
		"from_stage_id": "stg-1",
		"to_stage_id":   "stg-2",
	})
	r.handle(proto.Event{Kind: "task.stage_advanced", Data: data})
	got := stderr.String()
	if !strings.Contains(got, "[anthropic/claude-opus-4-7] [stage advanced: stg-1 -> stg-2]") {
		t.Errorf("stage_advanced line missing prefix; got %q", got)
	}
}

// TestStagesMixProviders covers the visibility gate the task-show table
// (and indirectly the web SPA chip) uses to decide whether to surface the
// per-stage RUNTIME column / chip. ADR 0020 §UX principles say the chip
// only earns its place once two distinct providers are in play across
// the stages we actually have data for.
func TestStagesMixProviders(t *testing.T) {
	cases := []struct {
		name   string
		stages []tm.Stage
		want   bool
	}{
		{"empty", nil, false},
		{"single anthropic", []tm.Stage{{Provider: "anthropic"}}, false},
		{"all anthropic", []tm.Stage{{Provider: "anthropic"}, {Provider: "anthropic"}}, false},
		{"pending then anthropic", []tm.Stage{{}, {Provider: "anthropic"}}, false},
		{"anthropic + openai", []tm.Stage{{Provider: "anthropic"}, {Provider: "openai"}}, true},
		{"openai + pending + anthropic", []tm.Stage{{Provider: "openai"}, {}, {Provider: "anthropic"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stagesMixProviders(tc.stages); got != tc.want {
				t.Errorf("stagesMixProviders = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFormatStageRuntime(t *testing.T) {
	cases := []struct {
		provider, model, want string
	}{
		{"anthropic", "claude-opus-4-7", "anthropic/claude-opus-4-7"},
		{"openai", "", "openai"},
		{"", "gpt-5.5", "gpt-5.5"},
		{"", "", "-"},
	}
	for _, tc := range cases {
		if got := formatStageRuntime(tc.provider, tc.model); got != tc.want {
			t.Errorf("formatStageRuntime(%q,%q)=%q want %q", tc.provider, tc.model, got, tc.want)
		}
	}
}

func feedSnapshot(t *testing.T, r *streamRenderer, provider, model string) {
	t.Helper()
	d := proto.SessionSnapshotData{
		Session: proto.SessionSummary{
			ID:       "sess-test",
			Status:   "running",
			Provider: provider,
			Model:    model,
		},
		QueueDepth: 0,
	}
	data, _ := json.Marshal(d)
	r.handle(proto.Event{Kind: proto.EventSessionSnapshot, Data: data})
}
