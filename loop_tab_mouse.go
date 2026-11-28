package main

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// The Loops popup with the mouse: a click selects a card (or a contractor), a click on the selected
// one opens it as ⏎ does, a click on "Board" or "Contractors" switches tab, and the wheel moves as
// ↑/↓ do (scrolling the open details). Clicks are mapped with the same layout numbers the views draw
// with, then handled by the key code, so the mouse can do nothing the keys cannot.

// tabBoardKeys is the board's key line; its height decides the board's room, so clicks share it.
func tabBoardKeys(st gateStyle) string {
	return st.key("←→↑↓", "move") + "   " + st.key("⏎", "open") + "   " + st.key("s", "stop") + "   " + st.key("d", "delete") + "   " + st.key(",", "settings") + "   " + st.key("esc/q", "close")
}

// The rows the views start at, counting the popup's one row of padding.
const (
	tabMouseHeadingY = 1 // Board · Contractors (n) · Tab switch
	tabMouseCardsY   = 4 // after the heading, the "n needs you" line and the column titles
	tabMouseCrewY    = 2 // the Contractors list, under the heading
	tabMouseLeftX    = 2
)

func tabKeyMsg(k string) tea.KeyPressMsg {
	switch k {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	}
	return tea.KeyPressMsg{Text: k, Code: rune(k[0])}
}

// mouse handles a click or a wheel step; ok is false for anything it does not use.
func (m tabModel) mouse(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			nm, cmd := m.Update(tabKeyMsg("up"))
			return nm, cmd, true
		case tea.MouseWheelDown:
			nm, cmd := m.Update(tabKeyMsg("down"))
			return nm, cmd, true
		}
	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft || m.detail {
			return m, nil, msg.Button == tea.MouseLeft
		}
		if msg.Y == tabMouseHeadingY {
			// "Board" then three spaces then "Contractors (n)".
			onCrew := msg.X >= tabMouseLeftX+len("Board")+3
			onBoard := msg.X >= tabMouseLeftX && msg.X < tabMouseLeftX+len("Board")
			if (onCrew && !m.contractors) || (onBoard && m.contractors) {
				nm, cmd := m.Update(tabKeyMsg("tab"))
				return nm, cmd, true
			}
			return m, nil, true
		}
		if m.contractors {
			i, ok := m.crewAt(msg.Y)
			if !ok {
				return m, nil, true
			}
			if i == m.crewSel {
				nm, cmd := m.Update(tabKeyMsg("enter"))
				return nm, cmd, true
			}
			m.crewSel, m.preview = i, nil
			m.refreshCrew()
			return m, nil, true
		}
		sec, n, ok := m.cardAt(msg.X, msg.Y)
		if !ok {
			return m, nil, true
		}
		if sec == m.col && m.positions[sec] == n {
			nm, cmd := m.Update(tabKeyMsg("enter"))
			return nm, cmd, true
		}
		m.col, m.positions[sec] = sec, n
		m.selectCard()
		return m, nil, true
	}
	return m, nil, false
}

// cardAt is the column and the card's place in it under (x, y), with the scrolling board() applies.
func (m tabModel) cardAt(x, y int) (tabSection, int, bool) {
	if len(m.rows) == 0 || y < tabMouseCardsY || x < tabMouseLeftX {
		return 0, 0, false
	}
	st := gateStyle{tmuxTheme()}
	w := max(m.w-4, 1)
	start, count := m.visibleColumns()
	cw := max((w-count+1)/count, 4)
	k := (x - tabMouseLeftX) / (cw + 1)
	if k >= count || (x-tabMouseLeftX)%(cw+1) == cw {
		return 0, 0, false // past the last column, or on the gap between two
	}
	sec := tabSection(start + k)
	in := m.columnRows(sec)
	h := max(m.h-5-lipgloss.Height(tabBoardKeys(st)), 1)
	room := max(h-2, 1)
	per := tabCardLines + 2
	lines := len(in) * per
	offset := max((m.positions[sec]+1)*per-room, 0)
	offset = min(offset, max(lines-room, 0))
	if y-tabMouseCardsY >= room {
		return 0, 0, false // below the board: the batch strips and the keys
	}
	n := (y - tabMouseCardsY + offset) / per
	if n >= len(in) {
		return 0, 0, false
	}
	return sec, n, true
}

// crewAt is the contractor drawn at row y of the Contractors list, laid out as crewView lays it out.
func (m tabModel) crewAt(y int) (int, bool) {
	var at []int // the crew index each list line belongs to, -1 for group headings
	group := ""
	selected := 0
	for i, c := range m.crew {
		g := "FINISHED"
		if c.State == "running" {
			g = "RUNNING"
		}
		if g != group {
			at = append(at, -1)
			group = g
		}
		if i == m.crewSel {
			selected = len(at)
		}
		at = append(at, i, i)
	}
	room := max(m.h-6, 1)
	offset := max(selected+2-room, 0)
	offset = min(offset, max(len(at)-room, 0))
	line := y - tabMouseCrewY + offset
	if y < tabMouseCrewY || y-tabMouseCrewY >= room || line < 0 || line >= len(at) || at[line] < 0 {
		return 0, false
	}
	return at[line], true
}
