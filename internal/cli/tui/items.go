package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

// item is one renderable block in the transcript. The viewport feeds on the
// concatenated render of all items; each item knows its own height so we can
// scroll efficiently. We re-render on width changes only.
//
// expanded is the global "show full tool output" flag toggled by Ctrl+O. Only
// toolItem looks at it; the rest accept it for interface uniformity.
type item interface {
	render(width int, s *styles, md *glamour.TermRenderer, expanded bool) string
}

type userItem struct {
	content string
}

func (u *userItem) render(width int, s *styles, _ *glamour.TermRenderer, _ bool) string {
	body := wrapText(strings.TrimSpace(u.content), width-2)
	lines := strings.Split(body, "\n")
	prefix := s.userQuote.Render("> ")
	for i, ln := range lines {
		lines[i] = prefix + s.userBody.Render(ln)
	}
	return strings.Join(lines, "\n")
}

type assistantItem struct {
	content string
	final   bool // true once turn.end/assistant.message received
}

func (a *assistantItem) render(_ int, s *styles, md *glamour.TermRenderer, _ bool) string {
	body := strings.TrimRight(a.content, "\n")
	if body == "" {
		if a.final {
			return s.statusInfo.Render("(no response)")
		}
		// Empty + still streaming: the dedicated status row at the bottom
		// already shows the spinner, so render nothing here.
		return ""
	}
	if md != nil {
		if out, err := md.Render(body); err == nil {
			return strings.TrimSpace(out)
		}
	}
	return body
}

type toolItem struct {
	useID   string
	tool    string
	input   json.RawMessage
	output  json.RawMessage
	done    bool
	isError bool
}

func (t *toolItem) render(width int, s *styles, _ *glamour.TermRenderer, expanded bool) string {
	glyph := s.toolGlyph.Render("●")
	if t.done {
		if t.isError {
			glyph = s.footerErr.Render("●")
		} else {
			glyph = s.footerOK.Render("●")
		}
	}
	head := glyph + " " + s.toolName.Render(t.tool)
	if arg := toolSummary(t.tool, t.input); arg != "" {
		// Reserve room: glyph(2) + name + parens(2) + ellipsis tolerance.
		budget := width - len(t.tool) - 6
		if budget < 16 {
			budget = 16
		}
		head += s.toolArg.Render("(" + truncOne(arg, budget) + ")")
	}
	if !t.done {
		return head + "\n" + s.toolBody.Render("  └ running…")
	}
	if expanded {
		text := strings.TrimRight(extractText(t.output), "\n")
		if text == "" {
			text = "(no output)"
		}
		innerWidth := width - 4
		if innerWidth < 10 {
			innerWidth = 10
		}
		wrapped := wrapText(text, innerWidth)
		if t.isError {
			wrapped = s.toolBodyErr.Render(wrapped)
		} else {
			wrapped = s.toolBody.Render(wrapped)
		}
		return head + "\n" + indent(wrapped, "  │ ")
	}
	summary, hasMore := toolResultOneLine(t.tool, t.output, t.isError)
	var rendered string
	if t.isError {
		rendered = s.toolBodyErr.Render(summary)
	} else {
		rendered = s.toolBody.Render(summary)
	}
	if hasMore {
		rendered += " " + s.hint.Render("(ctrl+o to expand)")
	}
	return head + "\n  └ " + rendered
}

type statusItem struct {
	level string // info, warn, err
	text  string
}

func (st *statusItem) render(width int, s *styles, _ *glamour.TermRenderer, _ bool) string {
	style := s.statusInfo
	prefix := "•"
	switch st.level {
	case "warn":
		style = s.statusWarn
		prefix = "!"
	case "err":
		style = s.statusErr
		prefix = "✗"
	}
	body := wrapText(st.text, width-3)
	body = indent(body, "  ")
	return style.Render(fmt.Sprintf("%s %s", prefix, strings.TrimLeft(body, " ")))
}

// wrapText wraps s to width using lipgloss's built-in renderer. Words longer
// than width are broken so we never overflow horizontally.
func wrapText(s string, width int) string {
	if width < 4 {
		width = 4
	}
	return lipgloss.NewStyle().Width(width).Render(s)
}

func indent(s, prefix string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = prefix + ln
	}
	return strings.Join(lines, "\n")
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
