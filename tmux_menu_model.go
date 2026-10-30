package main

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	ansix "github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
	"partyline.sh/partyline/internal/brand"
)

// The ctrl-\ menu as a Bubble Tea model: the same rows (tmuxMenuItems) and keys as before. Moving
// onto a session row previews it; an armed confirm needs its key again and any other key stands
// it down; esc, ctrl-c and ctrl-\ go back to where you started. What a pick does runs after the
// program has exited and the terminal is restored (menuModel.after), as the old reader did.

type menuActions struct {
	focus  func(paneID string)         // preview: queued in order (menuPreviewer)
	goBack func(paneID string)         // esc: focus the origin, after the menu has closed
	pick   func(it tmuxMenuItem) error // runs after the menu closes
	// refuse says why a pick is refused before it acts, or "" (the launcher cannot be closed).
	refuse func(it tmuxMenuItem) string
	// rename saves a session's name, as the launcher's r does; nil leaves r with no effect.
	rename func(it tmuxMenuItem, name string) error
}

type menuModel struct {
	act        menuActions
	items      []tmuxMenuItem
	tab        int // 0: Sessions, 1: Commands
	filter     string
	filtering  bool
	sessionSel int
	commandSel int
	sel        int
	armed      int // index of a confirm item picked once; -1 when none
	origin     string
	w, h       int
	after      func() error // what to run once the program has exited
	note       string       // a refusal, shown on the hint line
	renaming   bool         // r on a session: the launcher's rename box is open
	renameBuf  string
}

// menuStatuses arrives when the session scan finishes, after the menu has painted.
type menuStatuses map[string]string

func newMenuModel(items []tmuxMenuItem, origin string, act menuActions) menuModel {
	m := menuModel{act: act, items: items, armed: -1, origin: origin, w: 72, h: tmuxMenuHeight(items, 0) - 2} // tmuxMenuHeight less the border
	for i, it := range items {
		if it.paneID != "" && it.paneID == origin {
			m.sel = i
			break
		}
	}
	for m.sel < len(items) && items[m.sel].sep {
		m.sel++
	}
	m.sessionSel = m.sel
	m.commandSel = -1
	for i, it := range items {
		if it.paneID == "" && !it.sep {
			m.commandSel = i
			break
		}
	}
	if len(m.tabItems(0)) == 0 {
		m.tab, m.sel, m.sessionSel = 1, m.commandSel, -1
	}
	return m
}

// tabItems keeps indices into items so hotkeys and confirmations share one identity.
func (m menuModel) tabItems(tab int) []int {
	var rows []int
	for i, it := range m.items {
		if tab == 0 && m.filter != "" && !strings.Contains(strings.ToLower(it.label), strings.ToLower(m.filter)) && !strings.Contains(strings.ToLower(it.engine), strings.ToLower(m.filter)) {
			continue
		}
		if (it.paneID != "") == (tab == 0) {
			rows = append(rows, i)
		}
	}
	return rows
}

// refilter selects and previews the first match, preserving item indices for hotkeys.
func (m menuModel) refilter() menuModel {
	m.sel, m.sessionSel = -1, -1
	if rows := m.tabItems(0); len(rows) > 0 {
		m.sel, m.sessionSel = rows[0], rows[0]
		if m.act.focus != nil {
			m.act.focus(m.items[m.sel].paneID)
		}
	}
	return m
}

func (m menuModel) Init() tea.Cmd {
	return func() tea.Msg { return menuStatuses(tmuxSessionStatuses()) }
}

func (m menuModel) move(dir int) menuModel {
	rows := m.tabItems(m.tab)
	at := 0
	for j, i := range rows {
		if i == m.sel {
			at = j
			break
		}
	}
	for range rows {
		at = (at + dir + len(rows)) % len(rows)
		m.sel = rows[at]
		if !m.items[m.sel].sep {
			if m.tab == 0 {
				m.sessionSel = m.sel
				if m.act.focus != nil {
					m.act.focus(m.items[m.sel].paneID)
				}
			} else {
				m.commandSel = m.sel
			}
			return m
		}
	}
	return m
}

// back returns focus to the pane the menu opened on, then closes.
func (m menuModel) back() (tea.Model, tea.Cmd) {
	// Not through the preview queue: that is closed, its last preview landed, before this runs.
	origin, goBack := m.origin, m.act.goBack
	m.after = func() error {
		if origin != "" && goBack != nil {
			goBack(origin)
		}
		return nil
	}
	return m, tea.Quit
}

// choose picks row i: an armed confirm needs a second pick; anything else closes and acts.
func (m menuModel) choose(i int) (tea.Model, tea.Cmd) {
	if i < 0 || i >= len(m.items) || m.items[i].sep {
		return m, nil
	}
	it := m.items[i]
	if it.paneID != "" {
		m.sessionSel = i
	} else {
		m.commandSel = i
	}
	if it.confirm {
		m.tab = 1
	}
	if (it.paneID != "") == (m.tab == 0) {
		m.sel = i
	}
	if m.act.refuse != nil {
		if why := m.act.refuse(it); why != "" {
			m.armed, m.note = -1, why
			return m, nil
		}
	}
	if it.confirm && m.armed != i {
		m.armed = i
		return m, nil
	}
	pick := m.act.pick
	m.after = func() error { return pick(it) }
	return m, tea.Quit
}

func (m menuModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case menuStatuses:
		applyMenuStatuses(m.items, msg)
	case tea.KeyPressMsg:
		m.note = ""
		if m.renaming {
			return m.updateRename(msg)
		}
		if msg.String() == "esc" && (m.filtering || m.filter != "") {
			m.filter, m.filtering, m.armed = "", false, -1
			selected := m.sel
			m = m.refilter()
			if m.tab == 1 {
				m.sel = selected
			}
			return m, nil
		}
		if m.filtering {
			switch msg.String() {
			case "enter":
				m.filtering = false
				return m, nil
			case "backspace":
				r := []rune(m.filter)
				if len(r) > 0 {
					m.filter = string(r[:len(r)-1])
				}
				return m.refilter(), nil
			case "ctrl+c", "ctrl+\\":
				return m.back()
			default:
				if msg.Text != "" {
					m.filter += msg.Text
					return m.refilter(), nil
				}
				return m, nil
			}
		}
		switch k := msg.String(); k {
		case "/":
			if m.tab == 1 {
				m.commandSel = m.sel
			}
			m.tab, m.filtering, m.armed = 0, true, -1
			return m.refilter(), nil
		case "tab", "shift+tab":
			if m.tab == 0 {
				m.sessionSel = m.sel
			} else {
				m.commandSel = m.sel
			}
			m.tab = 1 - m.tab
			m.sel = m.sessionSel
			if m.tab == 1 {
				m.sel = m.commandSel
			}
			m.armed = -1
			return m, nil
		case "up", "left":
			// Moving stands an armed confirm down: x, an arrow, x must not close the next session.
			m.armed = -1
			return m.move(-1), nil
		case "down", "right":
			m.armed = -1
			return m.move(+1), nil
		case "esc", "ctrl+c", "ctrl+\\":
			return m.back()
		case "enter":
			return m.choose(m.sel)
		case "r":
			// On Sessions, r renames the highlighted session, as in the launcher (ctrl-\ o); on
			// Commands it stays the relay's hotkey.
			if m.tab == 0 && m.act.rename != nil && m.sel >= 0 && m.sel < len(m.items) && m.items[m.sel].paneID != "" {
				m.renaming, m.renameBuf, m.armed = true, m.items[m.sel].label, -1
				return m, nil
			}
			fallthrough
		default:
			for i, it := range m.items {
				if t := msg.Text; t != "" && ((it.key != "" && t == it.key) || (it.alt != "" && t == it.alt)) {
					return m.choose(i)
				}
			}
			m.armed = -1 // any other key stands down a pending confirm
		}
	}
	return m, nil
}

// updateRename edits the name in the rename box; ⏎ saves it (empty resets), esc cancels.
func (m menuModel) updateRename(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc", "ctrl+c":
		m.renaming, m.renameBuf = false, ""
	case "enter":
		name := strings.TrimSpace(m.renameBuf)
		m.renaming, m.renameBuf = false, ""
		if err := m.act.rename(m.items[m.sel], name); err != nil {
			m.note = "not renamed: " + err.Error()
			return m, nil
		}
		if name != "" {
			m.items[m.sel].label = name
		}
		m.note = "✓ renamed"
		if name == "" {
			m.note = "✓ name cleared"
		}
	case "backspace":
		if r := []rune(m.renameBuf); len(r) > 0 {
			m.renameBuf = string(r[:len(r)-1])
		}
	case "ctrl+u":
		m.renameBuf = ""
	default:
		if k.Text != "" {
			m.renameBuf += k.Text
		}
	}
	return m, nil
}

func (m menuModel) View() tea.View {
	if m.renaming {
		st := gateStyle{tmuxTheme()}
		v := tea.NewView(modalBox(st, max(m.w, 40), max(m.h, 8), " rename session ", renameModalLines(m.items[m.sel].label, m.renameBuf)))
		v.AltScreen = true
		v.BackgroundColor = lipgloss.Color(st.th.Surface)
		return v
	}
	all := m.items
	indices := m.tabItems(m.tab)
	m.items = nil
	selection, armed := -1, -1
	for j, i := range indices {
		m.items = append(m.items, all[i])
		if i == m.sel {
			selection = j
		}
		if i == m.armed {
			armed = j
		}
	}
	m.sel, m.armed = selection, armed
	st := gateStyle{tmuxTheme()}
	w := max(m.w-2, 30)
	var b strings.Builder
	// The layout tmuxMenuHeight sizes the popup for: the items, a blank line, the summary when there
	// is one, and the hint. A list that does not fit gives up one row per ↑/↓ more marker it shows.
	sessions, commands := st.muted("Sessions"), st.muted("Commands")
	if m.tab == 0 {
		sessions = st.fg(st.th.Focus).Bold(true).Render("Sessions")
	} else {
		commands = st.fg(st.th.Focus).Bold(true).Render("Commands")
	}
	tabs := " " + sessions + "  " + commands
	if m.tab == 0 && (m.filtering || m.filter != "") {
		tabs += "  / " + m.filter
	}
	b.WriteString(ansix.Truncate(tabs, w, "…") + "\n")
	reserve := 3
	if m.tab == 1 && m.sessionSel >= 0 && m.sessionSel < len(all) && all[m.sessionSel].paneID != "" {
		it := all[m.sessionSel]
		b.WriteString(" " + st.muted(runewidth.Truncate("for #"+it.num+" "+it.label, w-1, "…")) + "\n")
		reserve++
	}
	if len(m.items) == 0 {
		empty := " no sessions yet"
		if m.filter != "" {
			empty = " no session matches"
		}
		b.WriteString(st.muted(empty) + "\n")
	}

	if menuSummary(m.items) != "" {
		reserve++
	}
	body := m.h - reserve
	start, end := 0, len(m.items)
	if len(m.items) > body {
		for markers := 1; markers <= 2; markers++ {
			// At least one item row: menuWindow gives back the whole list when it has none to fill.
			start, end = menuWindow(m.items, m.sel, max(body-markers, 1)+3) // menuWindow keeps 3 rows back itself
			drawn := 0
			if start > 0 {
				drawn++
			}
			if end < len(m.items) {
				drawn++
			}
			if drawn <= markers {
				break
			}
		}
	}
	if start > 0 {
		b.WriteString(st.faint("  ↑ more") + "\n")
	}
	for i := start; i < end; i++ {
		b.WriteString(menuRow(st, m.items[i], i == m.sel, i == m.armed, w) + "\n")
	}
	if end < len(m.items) {
		b.WriteString(st.faint("  ↓ more") + "\n")
	}
	b.WriteString("\n")
	if s := menuSummary(m.items); s != "" {
		b.WriteString(" " + st.muted(s) + "\n")
	}
	hint := "↑↓ preview · ⏎ keep · r rename · tab commands · hotkey acts · esc back"
	if m.tab == 1 {
		hint = "↑↓ move · ⏎ run · tab sessions · hotkey acts · esc back"
	}
	if m.filtering {
		hint = "type to filter · ⏎ done · esc clear"
	}
	if m.note != "" {
		hint = m.note
	}
	b.WriteString(" " + st.faint(runewidth.Truncate(hint, w-1, "…")))
	v := tea.NewView(b.String())
	v.AltScreen = true
	v.BackgroundColor = lipgloss.Color(st.th.Surface)
	return v
}

// menuRow is one row: the hotkey or window number in the accent, the label, and a session's status
// on the right; the selected row on the raised background, keeping its number.
func menuRow(st gateStyle, it tmuxMenuItem, selected, armed bool, w int) string {
	if it.sep {
		return " " + st.muted(it.label)
	}
	num := it.num
	if it.key != "" {
		num = brand.ShiftKey(it.key) // ⇧N: the only sign the key needs shift
	}
	label := it.label
	if armed {
		// First, so a long label cannot clip the one thing the row has to say now.
		label = "sure? press " + brand.ShiftKey(it.key) + " again — " + it.label
	}
	textW := max(w-6, 10)
	status := ""
	if it.status != "" {
		textW -= menuStatusWidth + 2
		status = "  " + menuStatusText(st, it.status, selected)
	}
	body := runewidth.FillRight(runewidth.Truncate(label, textW, "…"), textW)
	if selected {
		line := " " + runewidth.FillRight(num, 2) + "  " + body + status
		return lipgloss.NewStyle().Background(lipgloss.Color(st.th.Raised)).Foreground(lipgloss.Color(st.th.Text)).Bold(true).Width(w).Render(line)
	}
	return " " + st.fg(st.th.Focus).Bold(true).Render(runewidth.FillRight(num, 2)) + "  " + st.text(body) + status
}

// menuStatusText is a session's status cell: needs you in the needs-you colour, working muted.
func menuStatusText(st gateStyle, status string, selected bool) string {
	var text string
	switch status {
	case "waiting":
		text = "‼ needs you"
	case "active":
		text = "/ working"
	default:
		return strings.Repeat(" ", menuStatusWidth)
	}
	text = runewidth.FillRight(text, menuStatusWidth)
	switch {
	case selected:
		return text
	case status == "waiting":
		return st.warn(text)
	}
	return st.muted(text)
}

// menuPreviewer applies focus commands on one goroutine, in the order they were asked for, so
// arrowing quickly ends on the last row instead of whichever command happened to finish last.
type menuPreviewer struct {
	queue chan string
	done  chan struct{}
}

func newMenuPreviewer(focus func(string)) *menuPreviewer {
	p := &menuPreviewer{queue: make(chan string, 64), done: make(chan struct{})}
	go func() {
		defer close(p.done)
		for id := range p.queue {
			focus(id)
		}
	}()
	return p
}

func (p *menuPreviewer) focus(id string) {
	select {
	case p.queue <- id:
	default: // a full queue drops the oldest so the newest still lands
		select {
		case <-p.queue:
		default:
		}
		p.queue <- id
	}
}

// close stops taking previews and waits for the queued ones to finish.
func (p *menuPreviewer) close() {
	close(p.queue)
	<-p.done
}
