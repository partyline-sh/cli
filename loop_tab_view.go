package main

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	ansix "github.com/charmbracelet/x/ansi"

	uitheme "partyline.sh/partyline/internal/theme"
)

// The Loops board's model and screens. It decides nothing: `a` on a card that needs you opens the
// approval popup, where the gate's checks and the confirmation apply.

type tabActions struct {
	rows    func(now time.Time) []tabRow
	detail  func(row tabRow) string
	gateRun func(tabRow) error
	now     func() time.Time
}

type tabModel struct {
	act         tabActions
	rows        []tabRow
	sel         int
	col         tabSection
	positions   [5]int
	after       *tabRow
	detail      bool
	vp          viewport.Model
	w, h        int
	contractors bool
	crew        []*contractor
	crewSel     int
	preview     *contractorView
	full        bool
	frame       int // the spinner's frame
}

type tabTick struct{}

func tabTickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return tabTick{} })
}

func newTabModel(act tabActions) tabModel {
	m := tabModel{act: act, vp: viewport.New(), w: 100, h: 30}
	m.rows = act.rows(act.now())
	m.selectCard()
	m.refreshCrew()
	return m
}

func (m tabModel) Init() tea.Cmd { return tea.Batch(tabTickCmd(), tabSpinCmd()) }

func (m tabModel) current() (tabRow, bool) {
	if m.sel < 0 || m.sel >= len(m.rows) {
		return tabRow{}, false
	}
	if m.rows[m.sel].Section != m.col {
		return tabRow{}, false
	}
	return m.rows[m.sel], true
}

// actionable is the selected run when key ("a" approve, "s" stop) applies to it: approve only
// what needs you, stop only what is still live.
func (m tabModel) actionable(key string) (tabRow, bool) {
	r, ok := m.current()
	if !ok || r.RunID == "" {
		return tabRow{}, false
	}
	return r, key == "a" && r.Section == tabNeedsYou || key == "s" && r.Section != tabDone && r.Section != tabPlanned
}

func (m tabModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.full && m.preview != nil {
		if key, ok := msg.(tea.KeyPressMsg); ok {
			switch key.String() {
			case "esc":
				m.full = false
				m.refreshCrew()
				return m, nil
			case "q", "ctrl+c":
				return m, tea.Quit
			}
		}
		if _, ok := msg.(tabTick); ok {
			next, _ := m.preview.Update(contractorTick{})
			v := next.(contractorView)
			m.preview = &v
			return m, tabTickCmd()
		}
		if size, ok := msg.(tea.WindowSizeMsg); ok {
			m.w, m.h = size.Width, size.Height
		}
		next, cmd := m.preview.Update(msg)
		v := next.(contractorView)
		m.preview = &v
		if _, ok := msg.(tabSpin); ok {
			m.frame++
			return m, tabSpinCmd()
		}
		return m, cmd
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.sizePreview()
		m.vp.SetWidth(m.w - 4)
		m.vp.SetHeight(max(m.h-6, 3))
	case tabTick:
		m.refreshCrew()
		// Keep the same row selected across a refresh, by its run (or planned step).
		cur, _ := m.current()
		m.rows = m.act.rows(m.act.now())
		for i, r := range m.rows {
			if tabSameRow(r, cur) {
				m.sel = i
			}
		}
		for n, i := range m.columnRows(m.col) {
			if i == m.sel {
				m.positions[m.col] = n
			}
		}
		m.selectCard()
		// Open details follow the run; SetContent keeps the scroll position.
		if r, ok := m.current(); ok && m.detail {
			m.vp.SetContent(m.act.detail(r))
		}
		return m, tabTickCmd()
	case tabSpin:
		m.frame++
		return m, tabSpinCmd()
	case tea.KeyPressMsg:
		if !m.detail && (msg.String() == "tab" || msg.String() == "shift+tab") {
			m.contractors = !m.contractors
			if m.contractors {
				m.refreshCrew()
			}
			return m, nil
		}
		if m.contractors {
			return m.updateCrew(msg)
		}
		if m.detail {
			if msg.String() == "ctrl+c" {
				return m, tea.Quit
			}
			switch msg.String() {
			case "esc", "q", "enter":
				m.detail = false
				return m, nil
			case "a", "s":
				// The open card can be approved or stopped as on the board, without going back first.
				if r, ok := m.actionable(msg.String()); ok {
					m.after = &r
					return m, tea.Quit
				}
			}
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			return m, cmd
		}
		switch msg.String() {
		case "esc", "q", "ctrl+c":
			return m, tea.Quit
		case "left", "h":
			m.col = (m.col + 4) % 5
			m.selectCard()
		case "right", "l":
			m.col = (m.col + 1) % 5
			m.selectCard()
		case "up", "k":
			m.positions[m.col] = max(m.positions[m.col]-1, 0)
			m.selectCard()
		case "down", "j":
			m.positions[m.col]++
			m.selectCard()
		case "enter":
			if r, ok := m.current(); ok && r.Watch != "" {
				m.selectContractor(r.Watch)
				return m, nil
			}
			if r, ok := m.current(); ok {
				m.vp.SetContent(m.act.detail(r))
				m.vp.GotoTop()
				m.detail = true
			}
		case "a", "s":
			if r, ok := m.actionable(msg.String()); ok {
				m.after = &r
				return m, tea.Quit
			}
		}
	}
	return m, nil
}

func (m tabModel) View() tea.View {
	if m.full && m.preview != nil {
		return m.preview.View()
	}
	if m.contractors {
		return m.crewView()
	}
	st := gateStyle{tmuxTheme()}
	w := max(m.w-4, 1)
	var b strings.Builder
	if !m.detail {
		b.WriteString(m.tabHeading(st) + "\n")
	}
	if m.detail {
		r, _ := m.current()
		keys := st.key("esc", "back") + "   " + st.key("↑↓", "scroll")
		if _, ok := m.actionable("a"); ok {
			keys += "   " + st.key("a", "approve")
		}
		if _, ok := m.actionable("s"); ok {
			keys += "   " + st.key("s", "stop")
		}
		b.WriteString(st.heading(termSafe(r.Title), w) + "\n" + m.vp.View() + "\n\n" + keys)
	} else {
		welcome := ""
		if len(m.rows) == 0 {
			welcome = st.heading("Welcome to Partyline", w) + "\n" + st.key("ctrl-\\ o", "open the launcher") + "\n\n"
			b.WriteString(welcome)
		}
		keys := st.key("←→", "columns") + "   " + st.key("↑↓", "cards") + "   " + st.key("⏎", "open") + "   " + st.key("a", "approve") + "   " + st.key("s", "stop") + "   " + st.key("esc/q", "close")
		room := max(m.h-5-lipgloss.Height(keys)-strings.Count(welcome, "\n"), 1)
		b.WriteString(m.board(st, w, room) + "\n" + keys)
	}
	v := tea.NewView(lipgloss.NewStyle().Padding(1, 2).Render(lipgloss.NewStyle().Width(w).Render(b.String())))
	v.AltScreen = true
	v.BackgroundColor = lipgloss.Color(st.th.Surface)
	return v
}

// tabSameRow: a and b are the same card across a refresh. A planned step has no run, so its batch
// and step tell it apart from another step with the same title.
func tabSameRow(a, b tabRow) bool {
	if a.RunID != b.RunID || a.Title != b.Title {
		return false
	}
	return a.RunID != "" || (a.Batch == b.Batch && a.Detail == b.Detail)
}

func (m tabModel) columnRows(sec tabSection) []int {
	var out []int
	for i, r := range m.rows {
		if r.Section == sec {
			out = append(out, i)
		}
	}
	return out
}

func (m *tabModel) selectCard() {
	in := m.columnRows(m.col)
	m.positions[m.col] = min(m.positions[m.col], max(len(in)-1, 0))
	m.sel = -1
	if len(in) > 0 {
		m.sel = in[m.positions[m.col]]
	}
}

func (m tabModel) visibleColumns() (int, int) {
	if m.w >= 110 {
		return 0, 5
	}
	return min(max(int(m.col)-1, 0), 2), 3
}

func (m tabModel) board(st gateStyle, w, h int) string {
	start, count := m.visibleColumns()
	cw := max((w-count+1)/count, 4)
	needs := len(m.columnRows(tabNeedsYou))
	header := st.text(fmt.Sprintf("%d needs you · %s", needs, watcherLine(m.act.now())))
	var columns []string
	for c := start; c < start+count; c++ {
		in := m.columnRows(tabSection(c))
		title := fmt.Sprintf("%s (%d)", strings.ToUpper(tabSectionNames[c]), len(in))
		colour := st.th.Muted
		if tabSection(c) == m.col {
			colour = st.th.Focus
		}
		heading := st.fg(colour).Bold(true).Render(title)
		var cards []string
		for _, i := range in {
			cards = append(cards, tabLine(st, m.rows[i], i == m.sel && tabSection(c) == m.col, cw, m.frame))
		}
		body := strings.Join(cards, "\n")
		lines := strings.Split(body, "\n")
		room := max(h-2, 1)
		offset := max((m.positions[c]+1)*5-room, 0)
		offset = min(offset, max(len(lines)-room, 0))
		lines = lines[offset:min(offset+room, len(lines))]
		column := lipgloss.NewStyle().Width(cw).Height(room + 1).Render(heading + "\n" + strings.Join(lines, "\n"))
		columns = append(columns, column)
	}
	board := header + "\n" + lipgloss.JoinHorizontal(lipgloss.Top, interleaveColumns(columns)...)
	if strips := tabBatchStrips(st, m.rows, w); len(strips) > 0 {
		board += "\n" + strings.Join(strips, "\n")
	}
	return board
}

func interleaveColumns(columns []string) []string {
	var out []string
	for i, c := range columns {
		if i > 0 {
			out = append(out, " ")
		}
		out = append(out, c)
	}
	return out
}

// tabLine draws a card with three content lines.
func tabLine(st gateStyle, r tabRow, selected bool, w int, frame int) string {
	glyph, colour := tabIndicatorGlyph(st.th, r, frame)
	id := r.RunID
	if id == "" {
		id = r.Batch
	}
	inner := max(w-4, 1)
	clip := func(s string) string { return ansix.Truncate(termSafe(s), inner, "…") }
	content := st.fg(colour).Render(clip(glyph+" "+id)) + "\n" + st.text(clip(r.Title)) + "\n" + tabStateLine(st, r, inner)
	border := st.th.Border
	style := lipgloss.NewStyle().Border(lipgloss.NormalBorder()).Width(w).Padding(0, 1)
	if selected {
		border = st.th.Focus
		style = style.Background(lipgloss.Color(st.th.Raised))
	}
	return style.BorderForeground(lipgloss.Color(border)).Render(content)
}

// tabGlyph is the state marker and its colour, from internal/theme.
func tabGlyph(th uitheme.Theme, r tabRow) (string, string) {
	var s uitheme.State
	switch {
	case r.Section == tabNeedsYou:
		s = uitheme.NeedsYou
	case r.Section == tabInReview, r.Section == tabInProgress:
		s = uitheme.Working
	case r.Section == tabPlanned:
		s = uitheme.Idle
	case r.State == loopPRReady:
		s = uitheme.Passed
	default:
		s = uitheme.Failed
	}
	return s.Glyph(), th.Colour(s)
}

// watcherLine says whether the watcher is running, from its heartbeat.
func watcherLine(now time.Time) string {
	s := loadWatcherState()
	if s.Heartbeat.IsZero() || now.Sub(s.Heartbeat) >= watcherStale {
		return "watcher not running"
	}
	return "watcher " + tabAge(now.Sub(s.Heartbeat))
}
