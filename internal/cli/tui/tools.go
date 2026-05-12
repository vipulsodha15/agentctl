package tui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// toolSummary returns a short human-readable argument summary for a tool
// invocation. The goal is one line, eg:
//
//	Read   internal/http/router.go
//	Bash   $ go test ./...
//	Grep   "func Run" in internal/
//
// Anything we can't classify falls back to the input keys.
func toolSummary(tool string, input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(input, &m); err != nil {
		return truncOne(string(input), 80)
	}
	str := func(k string) (string, bool) {
		v, ok := m[k]
		if !ok {
			return "", false
		}
		s, ok := v.(string)
		return s, ok && s != ""
	}

	switch tool {
	case "Read":
		if p, ok := str("file_path"); ok {
			return truncOne(p, 200)
		}
	case "Edit", "Write", "NotebookEdit":
		if p, ok := str("file_path"); ok {
			return truncOne(p, 200)
		}
	case "Bash":
		if c, ok := str("command"); ok {
			return "$ " + truncOne(firstLine(c), 200)
		}
	case "Grep":
		pat, _ := str("pattern")
		path, _ := str("path")
		switch {
		case pat != "" && path != "":
			return fmt.Sprintf("%q in %s", pat, path)
		case pat != "":
			return fmt.Sprintf("%q", pat)
		case path != "":
			return path
		}
	case "Glob":
		if p, ok := str("pattern"); ok {
			return p
		}
	case "WebFetch":
		if u, ok := str("url"); ok {
			return truncOne(u, 200)
		}
	case "WebSearch":
		if q, ok := str("query"); ok {
			return truncOne(q, 200)
		}
	case "TodoWrite":
		if t, ok := m["todos"]; ok {
			if arr, ok := t.([]any); ok {
				return fmt.Sprintf("%d todos", len(arr))
			}
		}
	}
	// generic fallback: list a few key=value pairs.
	return genericArgs(m)
}

func genericArgs(m map[string]any) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v := m[k]
		switch x := v.(type) {
		case string:
			parts = append(parts, fmt.Sprintf("%s=%s", k, truncOne(x, 40)))
		case float64, bool, nil:
			parts = append(parts, fmt.Sprintf("%s=%v", k, x))
		case []any:
			parts = append(parts, fmt.Sprintf("%s=[%d]", k, len(x)))
		case map[string]any:
			parts = append(parts, fmt.Sprintf("%s={…}", k))
		default:
			parts = append(parts, k)
		}
		if len(parts) >= 4 {
			break
		}
	}
	return truncOne(strings.Join(parts, " "), 200)
}

// toolResultOneLine returns a single-line summary of a tool result plus a
// hint of whether there is more content the user could reveal with Ctrl+O.
// Used by the collapsed (default) toolItem render.
func toolResultOneLine(tool string, output json.RawMessage, isErr bool) (string, bool) {
	if len(output) == 0 {
		if isErr {
			return "error: empty output", false
		}
		return "no output", false
	}
	text := strings.TrimRight(extractText(output), "\n")
	lines := strings.Split(text, "\n")
	nLines := len(lines)
	if text == "" {
		nLines = 0
	}
	var first string
	for _, ln := range lines {
		if t := strings.TrimSpace(ln); t != "" {
			first = t
			break
		}
	}
	hasMore := nLines > 1

	if isErr {
		if first == "" {
			return "error", hasMore
		}
		return "error: " + truncOne(first, 200), hasMore
	}
	switch tool {
	case "Read":
		if nLines == 0 {
			return "empty", false
		}
		return fmt.Sprintf("read %d lines", nLines), nLines > 0
	case "Grep", "Glob":
		count := 0
		for _, ln := range lines {
			if strings.TrimSpace(ln) != "" {
				count++
			}
		}
		if count == 0 {
			return "no matches", false
		}
		if count == 1 {
			return "1 match", true
		}
		return fmt.Sprintf("%d matches", count), true
	case "Edit", "Write", "NotebookEdit":
		if first == "" {
			return "ok", false
		}
		return truncOne(first, 200), hasMore
	}
	if first == "" {
		return "ok", false
	}
	return truncOne(first, 200), hasMore
}

// toolResultSummary returns the first few lines + size info for a result.
// `output` is the JSON-encoded shim payload — usually a string but may be a
// list of {type:"text",text:...} parts.
func toolResultSummary(output json.RawMessage, isErr bool, maxLines int) string {
	if len(output) == 0 {
		if isErr {
			return "(error, empty output)"
		}
		return "(no output)"
	}
	text := extractText(output)
	text = strings.TrimRight(text, "\n")
	if text == "" {
		// Fall back to raw JSON, which preserves structured outputs.
		text = string(output)
	}
	lines := strings.Split(text, "\n")
	total := len(lines)
	if total > maxLines {
		lines = lines[:maxLines]
	}
	out := strings.Join(lines, "\n")
	if total > maxLines {
		out += fmt.Sprintf("\n… (+%d more lines)", total-maxLines)
	}
	return out
}

func extractText(raw json.RawMessage) string {
	// Most common case: the value is a JSON string.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Anthropic-style content blocks: [{type:"text",text:"..."}, ...]
	var blocks []map[string]any
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var b strings.Builder
		for _, blk := range blocks {
			if t, _ := blk["type"].(string); t == "text" {
				if txt, _ := blk["text"].(string); txt != "" {
					if b.Len() > 0 {
						b.WriteByte('\n')
					}
					b.WriteString(txt)
				}
			}
		}
		if b.Len() > 0 {
			return b.String()
		}
	}
	// Anything else — return the raw JSON.
	return string(raw)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func truncOne(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 1 || len([]rune(s)) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
