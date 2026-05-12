package tui

import (
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const paletteMaxRows = 6

// palette is the slash-command autocomplete state. It's visible whenever the
// textarea starts with `/<word>` (no whitespace yet). Selection is local —
// callers read .selected() and rewrite the textarea on accept.
type palette struct {
	items    []Skill
	filtered []Skill
	idx      int
	visible  bool
}

// updateFromInput recomputes filter+visibility based on the textarea contents.
// Returns true when the rendered height changes, so the caller can relayout.
func (p *palette) updateFromInput(s string) bool {
	prevVisible := p.visible
	prevH := p.renderHeight()

	text := strings.TrimLeft(s, " \t")
	if !strings.HasPrefix(text, "/") || strings.ContainsAny(text[1:], " \t\n") {
		p.visible = false
		p.filtered = nil
		p.idx = 0
		return prevVisible != p.visible || prevH != p.renderHeight()
	}
	p.visible = true
	p.filtered = filterSkills(p.items, text[1:])
	if p.idx >= len(p.filtered) {
		p.idx = 0
	}
	return prevVisible != p.visible || prevH != p.renderHeight()
}

func filterSkills(items []Skill, prefix string) []Skill {
	pl := strings.ToLower(prefix)
	out := make([]Skill, 0, len(items))
	for _, s := range items {
		if pl == "" || strings.HasPrefix(strings.ToLower(s.Name), pl) {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (p *palette) selectNext() {
	if !p.visible || len(p.filtered) == 0 {
		return
	}
	p.idx = (p.idx + 1) % len(p.filtered)
}

func (p *palette) selectPrev() {
	if !p.visible || len(p.filtered) == 0 {
		return
	}
	p.idx = (p.idx - 1 + len(p.filtered)) % len(p.filtered)
}

func (p *palette) close() {
	p.visible = false
	p.filtered = nil
	p.idx = 0
}

func (p *palette) selected() (Skill, bool) {
	if !p.visible || len(p.filtered) == 0 || p.idx >= len(p.filtered) {
		return Skill{}, false
	}
	return p.filtered[p.idx], true
}

// renderHeight returns the number of terminal rows the palette will occupy.
// Used by relayout to keep the viewport from being overdrawn.
func (p *palette) renderHeight() int {
	if !p.visible {
		return 0
	}
	rows := len(p.filtered)
	if rows == 0 {
		rows = 1 // "no matches" hint
	}
	if rows > paletteMaxRows {
		rows = paletteMaxRows
	}
	return rows + 2 // border top + bottom
}

func (p *palette) render(width int, st *styles) string {
	if !p.visible {
		return ""
	}
	if len(p.filtered) == 0 {
		return st.paletteBox.Render(st.paletteHint.Render("no matching skills"))
	}

	start, end := visibleWindow(p.idx, len(p.filtered), paletteMaxRows)
	longest := 0
	for _, s := range p.filtered[start:end] {
		if w := lipgloss.Width(s.Name); w > longest {
			longest = w
		}
	}
	rowW := longest
	maxRowW := width - 4 // borders + padding
	if maxRowW > 0 && rowW > maxRowW {
		rowW = maxRowW
	}
	rows := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		name := p.filtered[i].Name
		style := st.paletteRow
		if i == p.idx {
			style = st.paletteRowSel
		}
		rows = append(rows, style.Width(rowW).Render(name))
	}
	return st.paletteBox.Render(lipgloss.JoinVertical(lipgloss.Left, rows...))
}

// visibleWindow keeps idx in view inside a window of max rows.
func visibleWindow(idx, n, max int) (int, int) {
	if n <= max {
		return 0, n
	}
	start := idx - max/2
	if start < 0 {
		start = 0
	}
	if start+max > n {
		start = n - max
	}
	return start, start + max
}
