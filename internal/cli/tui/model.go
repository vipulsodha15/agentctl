package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/agentctl/agentctl/internal/proto"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/glamour"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Sender abstracts the side effects the TUI invokes: sending a user message,
// requesting an interrupt of the in-flight turn, and fetching the installed
// skill list for the slash-command palette. The implementations live in the
// cli package and use the existing socket RPC.
type Sender interface {
	Send(content string) error
	Interrupt() error
	ListSkills() ([]Skill, error)
}

// Skill is the minimal payload the palette needs.
type Skill struct {
	Name string
}

// Messages handed in via tea.Program.Send from the event-stream goroutine.
type eventMsg struct{ ev proto.Event }
type streamEndMsg struct {
	code string
	err  error
}
type sendErrMsg struct{ err error }
type interruptErrMsg struct{ err error }
type detachMsg struct{}
type skillsLoadedMsg struct {
	skills []Skill
	err    error
}

type sessionInfo struct {
	id        string
	name      string
	model     string
	status    string
	queue     int
	inFlight  bool
	lastIn    int64
	lastOut   int64
	lastCost  float64
	usageOK   bool
	mcpIssues int
}

type Model struct {
	sessionID string
	sender    Sender

	items     []item
	curAssist *assistantItem // streaming assistant turn, if any
	turnID    string

	info sessionInfo

	width  int
	height int

	vp        viewport.Model
	input     textarea.Model
	md        *glamour.TermRenderer
	mdWidth   int
	styles    styles
	ready     bool
	exitCode  int
	streamEnd bool
	endNote   string
	confirmQ  bool // pending Ctrl-C confirm-to-detach
	lastErr   string

	palette palette

	spinner  spinner.Model
	spinning bool // true while a tea.Tick chain is in flight

	expandAll bool // Ctrl+O toggle: show full tool output instead of one-line summaries
}

// New constructs the model. sessionID is the session being attached to; the
// header shows it until session.snapshot fills in the rest.
func New(sessionID string, sender Sender) *Model {
	st := newStyles()
	ta := textarea.New()
	ta.Placeholder = "Message…"
	ta.Prompt = "│ "
	ta.CharLimit = 0
	ta.SetWidth(40)
	ta.SetHeight(2)
	ta.ShowLineNumbers = false
	ta.Focus()
	ta.KeyMap.InsertNewline.SetKeys("shift+enter", "alt+enter", "ctrl+j")

	vp := viewport.New(40, 10)
	vp.MouseWheelEnabled = true

	sp := spinner.New(spinner.WithSpinner(spinner.Dot))
	sp.Style = lipgloss.NewStyle().Foreground(colAccent)

	return &Model{
		sessionID: sessionID,
		sender:    sender,
		info:      sessionInfo{id: sessionID, status: "—"},
		vp:        vp,
		input:     ta,
		styles:    st,
		spinner:   sp,
	}
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.loadSkillsCmd())
}

func (m *Model) loadSkillsCmd() tea.Cmd {
	sender := m.sender
	return func() tea.Msg {
		sk, err := sender.ListSkills()
		return skillsLoadedMsg{skills: sk, err: err}
	}
}

// Width adjustments — the markdown renderer is expensive to build, so we
// only rebuild on width change.
func (m *Model) ensureMD(width int) {
	if width < 20 {
		width = 20
	}
	if m.md != nil && m.mdWidth == width {
		return
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(width),
	)
	if err == nil {
		m.md = r
		m.mdWidth = width
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.relayout()
		m.rerenderAll()
		return m, nil

	case tea.KeyMsg:
		// Confirm-on-Ctrl+C: first press shows a prompt, second press exits.
		if m.confirmQ && msg.String() != "ctrl+c" {
			m.confirmQ = false
		}
		// Palette intercepts navigation/accept keys before they reach the
		// textarea. We keep Ctrl+C/Ctrl+D handling below so quit still works.
		if m.palette.visible {
			switch msg.String() {
			case "up":
				m.palette.selectPrev()
				return m, nil
			case "down":
				m.palette.selectNext()
				return m, nil
			case "tab", "enter":
				if m.acceptPaletteSelection() {
					return m, nil
				}
			case "esc":
				m.palette.close()
				m.relayout()
				m.rerenderAll()
				return m, nil
			}
		}
		switch msg.String() {
		case "ctrl+d":
			return m, tea.Quit
		case "ctrl+c":
			if m.confirmQ {
				return m, tea.Quit
			}
			m.confirmQ = true
			return m, nil
		case "esc":
			if m.info.inFlight {
				go func() { _ = m.sender.Interrupt() }()
			}
			return m, nil
		case "enter":
			text := strings.TrimRight(m.input.Value(), "\n")
			if strings.TrimSpace(text) == "" {
				return m, nil
			}
			m.input.Reset()
			m.refreshPalette()
			// Optimistically render the user message so the input feels snappy.
			m.appendItem(&userItem{content: text})
			go func(t string) {
				if err := m.sender.Send(t); err != nil {
					// Surfaces via tea.Program.Send done in start/attach.
					_ = err
				}
			}(text)
			return m, nil
		case "pgup":
			m.vp.HalfViewUp()
			return m, nil
		case "pgdown":
			m.vp.HalfViewDown()
			return m, nil
		case "home":
			m.vp.GotoTop()
			return m, nil
		case "end":
			m.vp.GotoBottom()
			return m, nil
		case "ctrl+l":
			m.rerenderAll()
			return m, nil
		case "ctrl+o":
			m.expandAll = !m.expandAll
			m.rerenderAll()
			return m, nil
		}

	case eventMsg:
		wasIn := m.info.inFlight
		m.applyEvent(msg.ev)
		var cmd tea.Cmd
		if wasIn != m.info.inFlight {
			m.relayout()
			m.rerenderAll()
		}
		if !wasIn && m.info.inFlight && !m.spinning {
			m.spinning = true
			cmd = m.spinner.Tick
		}
		return m, cmd

	case spinner.TickMsg:
		// Stop the tick chain when the turn ends — otherwise the spinner
		// would keep ticking forever in the background.
		if !m.info.inFlight {
			m.spinning = false
			return m, nil
		}
		var c tea.Cmd
		m.spinner, c = m.spinner.Update(msg)
		return m, c

	case skillsLoadedMsg:
		if msg.err == nil {
			m.palette.items = msg.skills
		}
		return m, nil

	case streamEndMsg:
		m.streamEnd = true
		if msg.err != nil {
			m.endNote = "stream error: " + msg.err.Error()
		} else if msg.code != "" {
			m.endNote = "stream end: " + msg.code
		} else {
			m.endNote = "disconnected"
		}
		return m, tea.Quit

	case sendErrMsg:
		if msg.err != nil {
			m.lastErr = "send: " + msg.err.Error()
			m.appendItem(&statusItem{level: "err", text: m.lastErr})
		}
		return m, nil

	case interruptErrMsg:
		if msg.err != nil {
			m.appendItem(&statusItem{level: "warn", text: "interrupt: " + msg.err.Error()})
		}
		return m, nil
	}

	var c tea.Cmd
	m.input, c = m.input.Update(msg)
	cmds = append(cmds, c)
	m.vp, c = m.vp.Update(msg)
	cmds = append(cmds, c)
	if _, ok := msg.(tea.KeyMsg); ok {
		m.refreshPalette()
	}
	return m, tea.Batch(cmds...)
}

// refreshPalette recomputes palette visibility/filter against the current
// textarea contents, relayouting if the rendered height changed.
func (m *Model) refreshPalette() {
	if m.palette.updateFromInput(m.input.Value()) {
		m.relayout()
		m.rerenderAll()
	}
}

// acceptPaletteSelection rewrites the textarea to "/<name> " with any
// existing trailing text preserved, then closes the palette. Returns false
// when there's nothing to accept (empty filter) so the caller can fall
// through to default Enter/Tab handling.
func (m *Model) acceptPaletteSelection() bool {
	sel, ok := m.palette.selected()
	if !ok {
		return false
	}
	text := m.input.Value()
	rest := ""
	if i := strings.IndexAny(text, " \t\n"); i >= 0 {
		rest = strings.TrimLeft(text[i:], " \t")
	}
	next := "/" + sel.Name + " " + rest
	m.input.SetValue(next)
	m.palette.close()
	m.relayout()
	m.rerenderAll()
	return true
}

// appendItem adds an item and rerenders the viewport. Auto-scrolls to bottom
// unless the user has scrolled up (we infer that by checking the offset).
func (m *Model) appendItem(it item) {
	m.items = append(m.items, it)
	m.rerenderAll()
}

func (m *Model) relayout() {
	if m.width < 20 {
		m.width = 20
	}
	if m.height < 10 {
		m.height = 10
	}
	// header(1) + viewport + status?(1) + palette? + input(2 rows + 2 border) + footer(1) = m.height
	inputH := 4
	headerH := 1
	footerH := 1
	statusH := 0
	if m.info.inFlight {
		statusH = 1
	}
	vpH := m.height - inputH - headerH - footerH - m.palette.renderHeight() - statusH
	if vpH < 3 {
		vpH = 3
	}
	m.vp.Width = m.width
	m.vp.Height = vpH
	m.input.SetWidth(m.width - 2) // borders
	m.ensureMD(m.width - 4)        // body width inside indent
	m.ready = true
}

func (m *Model) rerenderAll() {
	if !m.ready {
		return
	}
	atBottom := m.vp.AtBottom()
	body := m.renderItems()
	m.vp.SetContent(body)
	if atBottom {
		m.vp.GotoBottom()
	}
}

func (m *Model) renderItems() string {
	w := m.vp.Width
	if w < 4 {
		w = 4
	}
	if len(m.items) == 0 {
		return m.renderWelcome(w)
	}
	parts := make([]string, 0, len(m.items)*2)
	for i, it := range m.items {
		rendered := it.render(w-2, &m.styles, m.md, m.expandAll)
		if rendered == "" {
			continue
		}
		if i > 0 && len(parts) > 0 {
			parts = append(parts, "")
		}
		parts = append(parts, rendered)
	}
	return strings.Join(parts, "\n")
}

func (m *Model) renderWelcome(w int) string {
	title := m.styles.headerTitle.Render("agentctl")
	subtitle := m.styles.statusInfo.Render("Fullscreen TUI · live transcript + tool calls")
	id := m.info.id
	if m.info.name != "" {
		id = m.info.name + " · " + m.info.id
	}
	idLine := m.styles.footerMuted.Render(id)
	help := m.styles.statusInfo.Render(
		"Type a message below and press Enter to start. Type / for skills.\n" +
			"Esc interrupts · Ctrl+O expands tool output · PgUp/PgDn scrolls · Ctrl+D detaches.",
	)
	block := lipgloss.JoinVertical(lipgloss.Center, title, subtitle, "", idLine, "", help)
	box := lipgloss.NewStyle().Width(w).Align(lipgloss.Center).Render(block)

	// Vertically center the welcome inside the viewport.
	contentH := lipgloss.Height(box)
	pad := (m.vp.Height - contentH) / 2
	if pad < 1 {
		pad = 1
	}
	return strings.Repeat("\n", pad) + box
}

func (m *Model) View() string {
	if !m.ready {
		return "initializing TUI…"
	}
	header := m.renderHeader()
	footer := m.renderFooter()
	body := m.vp.View()

	inputStyle := m.styles.inputBox
	if m.input.Focused() {
		inputStyle = m.styles.inputBoxFocus
	}
	input := inputStyle.Width(m.width - 2).Render(m.input.View())

	parts := []string{header, body}
	if m.info.inFlight {
		parts = append(parts, m.renderTurnStatus())
	}
	if pal := m.palette.render(m.width-2, &m.styles); pal != "" {
		parts = append(parts, pal)
	}
	parts = append(parts, input, footer)
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// renderTurnStatus draws the animated "agent is responding" row that sits
// between the transcript and the input box while a turn is in flight.
func (m *Model) renderTurnStatus() string {
	inner := m.width - 2
	if inner < 4 {
		inner = 4
	}
	left := m.spinner.View() + " " + m.styles.statusInfo.Render("agent is responding…")
	right := m.styles.hint.Render("Esc to interrupt")
	return m.styles.footer.Render(spread(left, right, inner))
}

func (m *Model) renderHeader() string {
	title := m.info.name
	if title == "" {
		title = m.info.id
	}
	model := m.info.model
	if model == "" {
		model = "—"
	}
	// Inner width accounts for the 2 cols of horizontal padding on m.styles.header.
	inner := m.width - 2
	if inner < 4 {
		inner = 4
	}
	left := m.styles.headerTitle.Render(title)
	right := m.styles.headerMuted.Render(model)
	return m.styles.header.Render(spread(left, right, inner))
}

func (m *Model) renderFooter() string {
	statusGlyph := "•"
	statusStyle := m.styles.footerMuted
	switch m.info.status {
	case "running", "starting":
		statusGlyph = "●"
		statusStyle = m.styles.footerOK
	case "error", "terminated":
		statusGlyph = "✗"
		statusStyle = m.styles.footerErr
	case "stopping", "stopped":
		statusGlyph = "○"
		statusStyle = m.styles.footerWarn
	}
	statusTxt := statusStyle.Render(fmt.Sprintf("%s %s", statusGlyph, m.info.status))

	// Build the left side from highest- to lowest-priority segments so we can
	// trim trailing ones when the terminal is narrow.
	segs := []string{statusTxt}
	if m.info.queue > 0 {
		segs = append(segs, m.styles.footerWarn.Render(fmt.Sprintf("queue %d", m.info.queue)))
	}
	if m.info.usageOK {
		segs = append(segs, m.styles.footerMuted.Render(fmt.Sprintf("in %s · out %s",
			humanK(m.info.lastIn), humanK(m.info.lastOut))))
		if m.info.lastCost > 0 {
			segs = append(segs, m.styles.footerMuted.Render(fmt.Sprintf("$%.4f", m.info.lastCost)))
		}
	}
	hint := "Esc interrupt · Ctrl+O expand · Ctrl+D detach"
	if m.expandAll {
		hint = "Esc interrupt · Ctrl+O collapse · Ctrl+D detach"
	}
	if m.confirmQ {
		hint = "Press Ctrl+C again to quit"
	}
	right := m.styles.hint.Render(hint)

	inner := m.width - 2 // padding allowance
	if inner < 4 {
		inner = 4
	}
	left := joinTrimming(segs, "  ·  ", inner-lipgloss.Width(right)-3)
	// If even the status alone is too long, drop the hint.
	if lipgloss.Width(left)+lipgloss.Width(right)+1 > inner {
		return m.styles.footer.Render(truncStyled(left, inner))
	}
	return m.styles.footer.Render(spread(left, right, inner))
}

// spread places `left` at the start and `right` at the end of an inner-width
// line, separated by spaces. If they don't fit it truncates left to make room.
func spread(left, right string, inner int) string {
	lw := lipgloss.Width(left)
	rw := lipgloss.Width(right)
	if lw+rw+1 > inner {
		// Truncate left so right still fits.
		left = truncStyled(left, max(0, inner-rw-1))
		lw = lipgloss.Width(left)
	}
	gap := inner - lw - rw
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

// joinTrimming joins segs with sep, dropping trailing segments until the
// result fits within budget cells. ANSI escapes are preserved.
func joinTrimming(segs []string, sep string, budget int) string {
	if budget < 1 {
		budget = 1
	}
	for n := len(segs); n > 0; n-- {
		s := strings.Join(segs[:n], sep)
		if lipgloss.Width(s) <= budget {
			return s
		}
	}
	if len(segs) > 0 {
		return truncStyled(segs[0], budget)
	}
	return ""
}

// truncStyled cuts a styled string to width cells, preserving leading ANSI
// escapes. We use lipgloss's MaxWidth which is ANSI-aware.
func truncStyled(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	return lipgloss.NewStyle().MaxWidth(w).Render(s)
}

func humanK(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 1_000_000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%.2fm", float64(n)/1_000_000)
}

// applyEvent mutates the model based on an incoming event.
func (m *Model) applyEvent(ev proto.Event) {
	switch ev.Kind {
	case proto.EventSessionSnapshot:
		var d proto.SessionSnapshotData
		_ = json.Unmarshal(ev.Data, &d)
		m.info.id = d.Session.ID
		m.info.name = d.Session.Name
		m.info.model = d.Session.Model
		m.info.status = d.Session.Status
		m.info.queue = d.QueueDepth
		m.info.inFlight = d.InFlight != ""
		if d.Session.CostUSD != nil {
			m.info.lastCost = *d.Session.CostUSD
			m.info.usageOK = true
		}
		// Replay conversation history.
		m.items = parseSnapshot(d.Conversation)
		m.curAssist = nil
		m.rerenderAll()
	case proto.EventSessionStarting:
		m.info.status = "starting"
		m.rerenderAll()
	case proto.EventSessionRunning:
		m.info.status = "running"
		m.rerenderAll()
	case proto.EventSessionStopping:
		m.info.status = "stopping"
		m.rerenderAll()
	case proto.EventSessionStopped:
		m.info.status = "stopped"
		m.rerenderAll()
	case proto.EventSessionTerminated:
		m.info.status = "terminated"
		m.appendItem(&statusItem{level: "warn", text: "Session terminated."})
	case proto.EventSessionError:
		m.appendItem(&statusItem{level: "err", text: "Session error: " + string(ev.Data)})

	case proto.EventUserMessage:
		var d proto.UserMessageData
		_ = json.Unmarshal(ev.Data, &d)
		// If we already optimistically appended the user message (echo from our
		// own send), don't duplicate it.
		if !m.lastItemMatchesUser(d.Content) {
			m.appendItem(&userItem{content: d.Content})
		}

	case proto.EventTurnStart:
		var d proto.TurnStartData
		_ = json.Unmarshal(ev.Data, &d)
		m.turnID = d.TurnID
		m.info.inFlight = true
		// Start a fresh assistant item to accumulate deltas into.
		ai := &assistantItem{}
		m.curAssist = ai
		m.appendItem(ai)

	case proto.EventAssistantDelta:
		var d proto.AssistantDeltaData
		_ = json.Unmarshal(ev.Data, &d)
		if d.Delta == "" {
			return
		}
		if m.curAssist == nil {
			m.curAssist = &assistantItem{}
			m.appendItem(m.curAssist)
		}
		m.curAssist.content += d.Delta
		m.rerenderAll()

	case proto.EventAssistantMessage:
		var d proto.AssistantMessageData
		_ = json.Unmarshal(ev.Data, &d)
		if m.curAssist == nil {
			m.curAssist = &assistantItem{}
			m.appendItem(m.curAssist)
		}
		if m.curAssist.content == "" && d.Content != "" {
			m.curAssist.content = d.Content
		}
		m.curAssist.final = true
		m.rerenderAll()

	case proto.EventTurnEnd, proto.EventTurnCancelled:
		if m.curAssist != nil {
			m.curAssist.final = true
		}
		m.curAssist = nil
		m.info.inFlight = false
		if ev.Kind == proto.EventTurnCancelled {
			m.appendItem(&statusItem{level: "warn", text: "Turn cancelled."})
		} else {
			m.rerenderAll()
		}

	case proto.EventToolCall:
		var d toolCallWire
		_ = json.Unmarshal(ev.Data, &d)
		it := &toolItem{
			useID: d.ToolUseID,
			tool:  d.Name(),
			input: d.Input,
		}
		// If we're in an assistant turn, the tool call belongs after the text
		// we've streamed so far. Lock in the current assistant item so the
		// next delta starts a new assistant block below this tool.
		if m.curAssist != nil {
			m.curAssist.final = true
			m.curAssist = nil
		}
		m.appendItem(it)

	case proto.EventToolResult:
		var d toolResultWire
		_ = json.Unmarshal(ev.Data, &d)
		body := d.Body()
		// Pair by tool_use_id when available, otherwise the most recent
		// un-finished tool block.
		paired := false
		if d.ToolUseID != "" {
			for i := len(m.items) - 1; i >= 0; i-- {
				if ti, ok := m.items[i].(*toolItem); ok && ti.useID == d.ToolUseID {
					ti.output = body
					ti.isError = d.IsError
					ti.done = true
					paired = true
					break
				}
			}
		}
		if !paired {
			for i := len(m.items) - 1; i >= 0; i-- {
				if ti, ok := m.items[i].(*toolItem); ok && !ti.done {
					ti.output = body
					ti.isError = d.IsError
					ti.done = true
					paired = true
					break
				}
			}
		}
		if !paired {
			// Orphan — render as standalone block.
			m.appendItem(&toolItem{tool: d.Tool(), output: body, isError: d.IsError, done: true})
		} else {
			m.rerenderAll()
		}

	case proto.EventQueueDepth:
		var d proto.QueueDepthData
		_ = json.Unmarshal(ev.Data, &d)
		m.info.queue = d.Depth
		m.rerenderAll()

	case proto.EventUsage:
		var d proto.UsageData
		_ = json.Unmarshal(ev.Data, &d)
		m.info.lastIn = d.InputTokens
		m.info.lastOut = d.OutputTokens
		m.info.lastCost = d.CostUSD
		m.info.usageOK = true
		m.rerenderAll()

	case proto.EventMCPUnreachable:
		m.appendItem(&statusItem{level: "warn", text: "MCP unreachable: " + string(ev.Data)})
	}
}

func (m *Model) lastItemMatchesUser(content string) bool {
	if len(m.items) == 0 {
		return false
	}
	u, ok := m.items[len(m.items)-1].(*userItem)
	if !ok {
		return false
	}
	return strings.TrimSpace(u.content) == strings.TrimSpace(content)
}

// Wire types — the shim sends `name`/`tool_use_id` for tool events, which the
// canonical proto.ToolCallData / proto.ToolResultData don't expose. We decode
// the raw payload locally so we can pair calls with results.
type toolCallWire struct {
	TurnID    string          `json:"turn_id"`
	ToolUseID string          `json:"tool_use_id"`
	NameField string          `json:"name"`
	ToolField string          `json:"tool"`
	Input     json.RawMessage `json:"input,omitempty"`
}

func (t toolCallWire) Name() string {
	if t.NameField != "" {
		return t.NameField
	}
	return t.ToolField
}

type toolResultWire struct {
	TurnID    string          `json:"turn_id"`
	ToolUseID string          `json:"tool_use_id"`
	ToolField string          `json:"tool"`
	Content   json.RawMessage `json:"content,omitempty"`
	Output    json.RawMessage `json:"output,omitempty"`
	IsError   bool            `json:"is_error"`
}

func (t toolResultWire) Tool() string { return t.ToolField }

// pick whichever payload field is populated.
func (t toolResultWire) Body() json.RawMessage {
	if len(t.Content) > 0 {
		return t.Content
	}
	return t.Output
}
