package proto

import (
	"encoding/json"
	"testing"
)

// The shim emits tool.call payloads with `name` (mirroring the SDK's
// tool_use block), while older agentd payloads used `tool`. ToolCallData
// must decode both so the tool label survives a shim/agentd version skew —
// otherwise it ends up empty and gets rendered as "?" after a refresh
// rehydrates it from task_messages.
func TestToolCallDataToolName(t *testing.T) {
	cases := []struct {
		name     string
		payload  string
		expected string
	}{
		{
			name:     "shim_emits_name",
			payload:  `{"turn_id":"T1","tool_use_id":"tu_1","name":"Bash","input":{"command":"ls"}}`,
			expected: "Bash",
		},
		{
			name:     "legacy_tool_field",
			payload:  `{"turn_id":"T1","tool_use_id":"tu_1","tool":"Read","input":{}}`,
			expected: "Read",
		},
		{
			name:     "tool_wins_over_name",
			payload:  `{"turn_id":"T1","tool_use_id":"tu_1","tool":"Edit","name":"Read"}`,
			expected: "Edit",
		},
		{
			name:     "missing_both",
			payload:  `{"turn_id":"T1","tool_use_id":"tu_1"}`,
			expected: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var d ToolCallData
			if err := json.Unmarshal([]byte(tc.payload), &d); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := d.ToolName(); got != tc.expected {
				t.Errorf("ToolName() = %q, want %q", got, tc.expected)
			}
		})
	}
}
