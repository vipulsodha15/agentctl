package tui

import (
	"encoding/json"
	"testing"
)

func TestParseSnapshotPairsToolUseAndResult(t *testing.T) {
	raw := json.RawMessage(`[
        {"type":"user","uuid":"u1","message":{"role":"user","content":"hi"}},
        {"type":"assistant","uuid":"a1","message":{"role":"assistant","content":[
            {"type":"text","text":"hello"},
            {"type":"tool_use","id":"tu_1","name":"Read","input":{"file_path":"foo.go"}}
        ]}},
        {"type":"user","uuid":"u2","message":{"role":"user","content":[
            {"type":"tool_result","tool_use_id":"tu_1","content":"line1\nline2","is_error":false}
        ]}}
    ]`)
	items := parseSnapshot(raw)
	if len(items) != 3 {
		t.Fatalf("want 3 items, got %d", len(items))
	}
	u, ok := items[0].(*userItem)
	if !ok || u.content != "hi" {
		t.Fatalf("item[0] not user 'hi': %#v", items[0])
	}
	a, ok := items[1].(*assistantItem)
	if !ok || a.content != "hello" {
		t.Fatalf("item[1] not assistant 'hello': %#v", items[1])
	}
	ti, ok := items[2].(*toolItem)
	if !ok {
		t.Fatalf("item[2] not toolItem: %#v", items[2])
	}
	if ti.tool != "Read" || !ti.done || ti.useID != "tu_1" {
		t.Fatalf("tool item wrong: %+v", ti)
	}
	if got := toolResultSummary(ti.output, ti.isError, 10); got != "line1\nline2" {
		t.Fatalf("result summary: %q", got)
	}
}

func TestToolSummaryShapes(t *testing.T) {
	cases := []struct {
		tool string
		in   string
		want string
	}{
		{"Read", `{"file_path":"a/b.go"}`, "a/b.go"},
		{"Edit", `{"file_path":"a/b.go","old_string":"x","new_string":"y"}`, "a/b.go"},
		{"Bash", `{"command":"go test ./...\necho hi"}`, "$ go test ./..."},
		{"Grep", `{"pattern":"foo","path":"internal/"}`, `"foo" in internal/`},
		{"Glob", `{"pattern":"**/*.go"}`, "**/*.go"},
		{"WebFetch", `{"url":"https://example.com"}`, "https://example.com"},
	}
	for _, tc := range cases {
		got := toolSummary(tc.tool, json.RawMessage(tc.in))
		if got != tc.want {
			t.Errorf("toolSummary(%s, %s) = %q, want %q", tc.tool, tc.in, got, tc.want)
		}
	}
}

func TestToolResultOneLine(t *testing.T) {
	cases := []struct {
		name     string
		tool     string
		out      string
		isErr    bool
		want     string
		wantMore bool
	}{
		{"read multi-line", "Read", `"1\tline a\n2\tline b\n3\tline c"`, false, "read 3 lines", true},
		{"read empty", "Read", `""`, false, "empty", false},
		{"grep zero", "Grep", `""`, false, "no matches", false},
		{"grep one", "Grep", `"only-match"`, false, "1 match", true},
		{"grep many", "Grep", `"a\nb\nc"`, false, "3 matches", true},
		{"edit ok", "Edit", `"File created"`, false, "File created", false},
		{"bash first line", "Bash", `"hello\nworld"`, false, "hello", true},
		{"error", "Read", `"open foo: no such file"`, true, "error: open foo: no such file", false},
		{"empty output", "Bash", ``, false, "no output", false},
	}
	for _, tc := range cases {
		got, more := toolResultOneLine(tc.tool, json.RawMessage(tc.out), tc.isErr)
		if got != tc.want || more != tc.wantMore {
			t.Errorf("%s: got (%q, %v), want (%q, %v)", tc.name, got, more, tc.want, tc.wantMore)
		}
	}
}

func TestExtractTextFromBlocks(t *testing.T) {
	raw := json.RawMessage(`[{"type":"text","text":"first"},{"type":"text","text":"second"}]`)
	got := extractText(raw)
	if got != "first\nsecond" {
		t.Fatalf("extractText: %q", got)
	}
}
