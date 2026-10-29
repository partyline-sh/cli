package main

import (
	"fmt"
	"path/filepath"

	"charm.land/lipgloss/v2"

	uitheme "partyline.sh/partyline/internal/theme"
)

// The approval popup's list screen rows, and the scroll that keeps the selected one in view.

// gateListTop is how many lines sit above the list's first row: the heading, its rule and a blank.
const gateListTop = 3

// listRow is the list's row for m.runs[i], highlighted when it is the selection.
func (m gateModel) listRow(st gateStyle, i, w int) string {
	r := m.runs[i]
	glyph := uitheme.NeedsYou.Glyph()
	if !loopWaiting(r) {
		glyph = uitheme.Working.Glyph()
	}
	line := fmt.Sprintf("%s %-18s %s  %s  %s", glyph, gateKind(r), termSafe(r.ID), termSafe(filepath.Base(r.Repo)), termSafe(gateTitle(r)))
	if r.Remote != nil {
		line = fmt.Sprintf("%s %-18s %s  %s", glyph, gateKind(r), termSafe(r.Remote.Phone.ID), termSafe(gateTitle(r)))
	}
	if i == m.sel {
		return lipgloss.NewStyle().Background(lipgloss.Color(st.th.Raised)).Width(w).Render(st.text(line))
	}
	return st.muted(line)
}

// listScroll is the scroll that keeps the selected row on screen, moving as little as it can. A
// long row wraps, so each row's height is measured as frame wraps it.
func (m gateModel) listScroll() int {
	st, lines, _, room := m.frame()
	w, wrap := max(m.w-4, 40), lipgloss.NewStyle().Width(max(m.w, 40)-4)
	top := gateListTop
	for i := 0; i < m.sel && i < len(m.runs); i++ {
		top += lipgloss.Height(wrap.Render(m.listRow(st, i, w)))
	}
	bottom := top
	if m.sel < len(m.runs) {
		bottom += lipgloss.Height(wrap.Render(m.listRow(st, m.sel, w))) - 1
	}
	scroll := m.scroll
	if m.sel == 0 {
		scroll = 0 // the first row brings the heading back with it
	} else if top < scroll {
		scroll = top
	}
	if bottom >= scroll+room {
		scroll = bottom - room + 1
	}
	return min(max(scroll, 0), max(len(lines)-room, 0))
}

// selectionScroll is the scroll after the selection moves: on the list, enough to show the new
// row; on a run's own screen, the top of the other run.
func (m gateModel) selectionScroll() int {
	if m.list {
		return m.listScroll()
	}
	return 0
}
