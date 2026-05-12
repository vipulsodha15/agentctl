package tui

import "github.com/charmbracelet/lipgloss"

var (
	colMuted   = lipgloss.AdaptiveColor{Light: "#6c757d", Dark: "#8a8f98"}
	colAccent  = lipgloss.AdaptiveColor{Light: "#5e6ad2", Dark: "#7f87e0"}
	colUser    = lipgloss.AdaptiveColor{Light: "#0a7d40", Dark: "#5fd787"}
	colAssist  = lipgloss.AdaptiveColor{Light: "#1f6feb", Dark: "#79c0ff"}
	colTool    = lipgloss.AdaptiveColor{Light: "#8957e5", Dark: "#d2a8ff"}
	colWarn    = lipgloss.AdaptiveColor{Light: "#bf8700", Dark: "#e3b341"}
	colErr     = lipgloss.AdaptiveColor{Light: "#cf222e", Dark: "#ff7b72"}
	colOK      = lipgloss.AdaptiveColor{Light: "#1a7f37", Dark: "#56d364"}
	colBg      = lipgloss.AdaptiveColor{Light: "#f6f8fa", Dark: "#161b22"}
	colFgFaint = lipgloss.AdaptiveColor{Light: "#57606a", Dark: "#9ba2ad"}
)

type styles struct {
	app           lipgloss.Style
	header        lipgloss.Style
	headerTitle   lipgloss.Style
	headerMuted   lipgloss.Style
	footer        lipgloss.Style
	footerMuted   lipgloss.Style
	footerOK      lipgloss.Style
	footerWarn    lipgloss.Style
	footerErr     lipgloss.Style
	userLabel     lipgloss.Style
	userQuote     lipgloss.Style
	userBody      lipgloss.Style
	assistLabel   lipgloss.Style
	assistBody    lipgloss.Style
	toolHead      lipgloss.Style
	toolGlyph     lipgloss.Style
	toolName      lipgloss.Style
	toolArg       lipgloss.Style
	toolBody      lipgloss.Style
	toolBodyErr   lipgloss.Style
	statusInfo    lipgloss.Style
	statusErr     lipgloss.Style
	statusWarn    lipgloss.Style
	inputBox      lipgloss.Style
	inputBoxFocus lipgloss.Style
	hint          lipgloss.Style
	diffAdd       lipgloss.Style
	diffDel       lipgloss.Style
	diffHunk      lipgloss.Style
	paletteBox    lipgloss.Style
	paletteRow    lipgloss.Style
	paletteRowSel lipgloss.Style
	paletteHint   lipgloss.Style
}

func newStyles() styles {
	return styles{
		app:         lipgloss.NewStyle(),
		header:      lipgloss.NewStyle().Foreground(colMuted).Padding(0, 1),
		headerTitle: lipgloss.NewStyle().Foreground(colAccent).Bold(true),
		headerMuted: lipgloss.NewStyle().Foreground(colMuted),
		footer:      lipgloss.NewStyle().Foreground(colMuted).Padding(0, 1),
		footerMuted: lipgloss.NewStyle().Foreground(colMuted),
		footerOK:    lipgloss.NewStyle().Foreground(colOK),
		footerWarn:  lipgloss.NewStyle().Foreground(colWarn),
		footerErr:   lipgloss.NewStyle().Foreground(colErr),
		userLabel:   lipgloss.NewStyle().Foreground(colUser).Bold(true),
		userQuote:   lipgloss.NewStyle().Foreground(colMuted),
		userBody:    lipgloss.NewStyle().Foreground(colFgFaint),
		assistLabel: lipgloss.NewStyle().Foreground(colAssist).Bold(true),
		assistBody:  lipgloss.NewStyle(),
		toolHead:    lipgloss.NewStyle().Foreground(colTool),
		toolGlyph:   lipgloss.NewStyle().Foreground(colTool),
		toolName:    lipgloss.NewStyle().Foreground(colTool).Bold(true),
		toolArg:     lipgloss.NewStyle().Foreground(colFgFaint),
		toolBody:    lipgloss.NewStyle().Foreground(colMuted),
		toolBodyErr: lipgloss.NewStyle().Foreground(colErr),
		statusInfo:  lipgloss.NewStyle().Foreground(colMuted).Italic(true),
		statusErr:   lipgloss.NewStyle().Foreground(colErr).Italic(true),
		statusWarn:  lipgloss.NewStyle().Foreground(colWarn).Italic(true),
		inputBox: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colMuted).
			Padding(0, 1),
		inputBoxFocus: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colAccent).
			Padding(0, 1),
		hint:     lipgloss.NewStyle().Foreground(colMuted),
		diffAdd:  lipgloss.NewStyle().Foreground(colOK),
		diffDel:  lipgloss.NewStyle().Foreground(colErr),
		diffHunk: lipgloss.NewStyle().Foreground(colAccent),
		paletteBox: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colMuted).
			Padding(0, 1),
		paletteRow:    lipgloss.NewStyle(),
		paletteRowSel: lipgloss.NewStyle().Foreground(colAccent).Bold(true),
		paletteHint:   lipgloss.NewStyle().Foreground(colMuted).Italic(true),
	}
}
