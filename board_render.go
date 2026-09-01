package main

import (
	"fmt"
	"strings"

	"partyline.sh/partyline/internal/api"
)

// board_render.go — drawing the board. One function builds the entire screen as a string (the same
// contract as the launcher's frame()), so a repaint is one write and can never tear.

const (
	boardSel  = "\x1b[48;5;236m"  // focused row background
	boardDim  = "\x1b[38;5;240m"  // structure and asides
	boardMid  = "\x1b[38;5;245m"  // secondary text
	boardWarn = "\x1b[38;5;214m"  // needs a human
	boardBad  = "\x1b[38;5;203m"  // failed
	boardGood = "\x1b[38;5;78m"   // healthy / done
	boardCol  = "\x1b[1;38;5;39m" // focused column title
)

// tileHeight is fixed at three lines — title, state, and one line of live detail — so a column is a
// predictable number of tiles and the cursor does not jump by different amounts depending on what a
// card happens to carry.
const tileHeight = 3

// frame renders the whole board.
func (m *boardModel) frame() string {
	var sb strings.Builder
	sb.WriteString("\x1b[H") // home; every line clears to EOL as it goes

	// Never clamp UP. A terminal genuinely smaller than this gets a smaller frame, not a bigger one:
	// emitting more lines than the screen has scrolls it on every repaint, which reads as the whole
	// board juddering. Only the degenerate zero/negative case gets a floor.
	w, h := m.w, m.h
	if w < 1 {
		w = 80
	}
	if h < 1 {
		h = 24
	}

	// Assemble every line, then force the list to exactly h. Deriving a body height by arithmetic
	// and flooring it (bodyH < 3 → 3) overshot on a short terminal and emitted more lines than the
	// screen had, which scrolls the board on every repaint. Composing then truncating cannot.
	lines := m.headerLines(w)
	bodyH := h - len(lines) - 2 // status line + hint bar
	if bodyH > 0 {
		lines = append(lines, m.bodyLines(w, bodyH)...)
	}
	lines = append(lines, m.statusLine(w), m.hintLine(w))

	// Truncation keeps the TOP: the header and the cards matter more than the hint bar on a screen
	// too small for both.
	if len(lines) > h {
		lines = lines[:h]
	}
	for len(lines) < h {
		lines = append(lines, "")
	}

	for i, line := range lines {
		sb.WriteString(line + "\x1b[K")
		if i < len(lines)-1 {
			sb.WriteString("\r\n")
		}
	}
	sb.WriteString("\x1b[J")

	// The modal is drawn LAST, with absolute cursor positioning, so it lands on top of a complete
	// board rather than replacing it. Keeping the board visible behind a question is what makes the
	// answer make sense.
	if box := m.overlayBox(w, h); len(box) > 0 {
		top := max(1, (h-len(box))/2)
		left := max(1, (w-visWidth(box[0]))/2)
		for i, line := range box {
			sb.WriteString(fmt.Sprintf("\x1b[%d;%dH%s", top+i, left, line))
		}
	}
	return sb.String()
}

// headerLines is the wordmark row plus the column tabs. The tab strip is drawn on every width, not
// just narrow ones: when the board is scrolled it is the only thing that says which columns exist
// off-screen, and hiding it on wide terminals made the same board look like two different screens.
func (m *boardModel) headerLines(w int) []string {
	counts := ""
	urgent := countUrgent(m.data)
	if m.data != nil {
		// Counted over the LOADED columns: naming partyline's five here would print "0 backlog" over
		// an Odoo board that has no such column.
		var parts []string
		for _, col := range m.data.Columns {
			parts = append(parts, fmt.Sprintf("%d %s", len(m.data.Column(col.Key)), strings.ToLower(col.Title)))
		}
		counts = boardDim + strings.Join(parts, " · ") + reset
	}
	if urgent > 0 {
		counts += fmt.Sprintf("   %s%d need you%s", boardWarn, urgent, reset)
	}

	title := boardWordmark() + "  " + boardDim + m.sourceLabel() + reset
	gap := w - visWidth(title) - visWidth(counts)
	if gap < 2 {
		gap = 2
		counts = clipVis(counts, max(0, w-visWidth(title)-2))
	}
	return []string{
		title + strings.Repeat(" ", gap) + counts,
		m.tabStrip(w),
	}
}

// tabStrip names all five columns with their counts, marking the focused one and dimming those
// currently off-screen.
func (m *boardModel) tabStrip(w int) string {
	// Two label widths, and the narrow one is not a nicety: clipping the strip drops the RIGHTMOST
	// columns, which are Review and Accepted — the two a person opens the board to act on. Losing
	// them is worse than abbreviating all five.
	build := func(short bool) string {
		start, count := visibleColumns(w, m.col, len(m.columnKeys()))
		var parts []string
		for i, col := range m.columnKeys() {
			n := 0
			if m.data != nil {
				n = len(m.data.Column(col))
			}
			name := m.data.Title(col)
			if short {
				name = string([]rune(name)[:3])
			}
			label := fmt.Sprintf("%s %d", name, n)
			switch {
			case i == m.col:
				label = boardCol + label + reset
			case i >= start && i < start+count:
				label = boardMid + label + reset
			default:
				label = boardDim + label + reset
			}
			parts = append(parts, label)
		}
		return strings.Join(parts, boardDim+" · "+reset)
	}
	if full := build(false); visWidth(full) <= w {
		return full
	}
	return clipVis(build(true), w)
}

// bodyLines lays the visible columns side by side.
func (m *boardModel) bodyLines(w, h int) []string {
	if m.data == nil {
		return centerNote(w, h, "reading the board…")
	}
	keys := m.columnKeys()
	start, count := visibleColumns(w, m.col, len(keys))
	if count == 0 {
		return centerNote(w, h, "nothing here")
	}

	// Width per column, with the remainder spread over the leftmost columns rather than left as a
	// ragged gap on the right.
	gutters := count - 1
	avail := w - gutters
	base, extra := avail/count, avail%count

	cols := make([][]string, count)
	for i := 0; i < count; i++ {
		cw := base
		if i < extra {
			cw++
		}
		cols[i] = m.columnLines(keys[start+i], cw, h, start+i == m.col)
	}

	out := make([]string, h)
	for row := 0; row < h; row++ {
		var line strings.Builder
		for i := range cols {
			if i > 0 {
				line.WriteString(" ")
			}
			line.WriteString(cols[i][row])
		}
		out[row] = line.String()
	}
	return out
}

// columnLines renders one column to exactly h lines.
func (m *boardModel) columnLines(col api.BoardColumn, w, h int, focused bool) []string {
	out := make([]string, 0, h)

	head := fmt.Sprintf("%s (%d)", m.data.Title(col), len(m.data.Column(col)))
	if focused {
		head = boardCol + head + reset
	} else {
		head = boardMid + head + reset
	}
	out = append(out, padVis(head, w))
	out = append(out, boardDim+strings.Repeat("─", w)+reset)

	rows := m.rows(col)
	if len(rows) == 0 {
		// Wrapped, not clipped: the note's whole job is to say what to do next, and a column narrow
		// enough to cut "press n to add work" in half is exactly the one where nobody can guess it.
		for _, line := range wrapPlain(emptyColumnNote(col), max(6, w-2)) {
			out = append(out, padVis(boardDim+"  "+line+reset, w))
		}
		for len(out) < h {
			out = append(out, strings.Repeat(" ", w))
		}
		return out[:h]
	}

	bodyH := h - len(out)
	cursor := m.cursor[col]
	top := m.scrollFor(col, cursor, rows, bodyH)

	lines := make([]string, 0, bodyH)
	for i := top; i < len(rows) && len(lines) < bodyH; i++ {
		sel := focused && i == cursor
		if rows[i].header() {
			lines = append(lines, m.chainHeaderLine(rows[i], w, sel))
			continue
		}
		lines = append(lines, m.tileLines(*rows[i].Card, w, sel)...)
	}
	for len(lines) < bodyH {
		lines = append(lines, strings.Repeat(" ", w))
	}
	out = append(out, lines[:bodyH]...)
	return out[:h]
}

// scrollFor keeps the cursor on screen, measuring in LINES rather than rows because a chain header
// is one line and a tile is three. Scrolling by rows made a cursor near the bottom of a column
// disappear whenever the rows above it happened to be tiles.
func (m *boardModel) scrollFor(col api.BoardColumn, cursor int, rows []boardRow, bodyH int) int {
	lineOf := func(idx int) int {
		n := 0
		for i := 0; i < idx && i < len(rows); i++ {
			if rows[i].header() {
				n++
			} else {
				n += tileHeight
			}
		}
		return n
	}
	top := m.scroll[col]
	if top > cursor {
		top = cursor
	}
	// Walk the top down until the cursor's last line fits.
	for top < cursor && lineOf(cursor+1)-lineOf(top) > bodyH {
		top++
	}
	if top < 0 {
		top = 0
	}
	m.scroll[col] = top
	return top
}

// tileLines is one card: what it is, what state it is in, and the single most useful live detail.
func (m *boardModel) tileLines(c api.BoardCard, w int, sel bool) []string {
	state, urgent := cardState(c)
	stateClr := boardMid
	switch {
	case urgent && c.Status == "failed":
		stateClr = boardBad
	case urgent:
		stateClr = boardWarn
	case c.Status == "done" || c.Column == api.ColAccepted:
		stateClr = boardGood
	}

	marker := "  "
	if sel {
		marker = boardCol + "▌ " + reset
	}

	title := clipVis(cardTitle(c), w-2)
	if sel {
		title = "\x1b[1m" + title + "\x1b[22m"
	}

	meta := stateClr + state + reset
	if c.Machine != "" {
		meta += boardDim + " · " + c.Machine + reset
	}
	if c.Total > 0 {
		meta += fmt.Sprintf("%s · %d/%d%s", boardDim, c.Done, c.Total, reset)
	}
	if c.ReviewGrade != "" {
		meta += boardDim + " · grade " + c.ReviewGrade + reset
	}

	lines := []string{
		marker + padVis(title, w-2),
		"  " + padVis(clipVis(meta, w-2), w-2),
		"  " + padVis(clipVis(boardDim+tileDetail(c)+reset, w-2), w-2),
	}
	if sel {
		for i := range lines {
			lines[i] = boardSel + lines[i] + reset
		}
	}
	return lines
}

// tileDetail is the third line: the ONE thing worth saying about this card right now.
//
// Ordered by what changes the reader's next move. A blocked chain names who is holding it (that is
// a different person's problem); a failure names the reason; a live run shows what the agent last
// said, which is the closest thing to watching it work.
func tileDetail(c api.BoardCard) string {
	switch {
	case c.ChainBlocker != nil:
		return "waiting on: " + strings.TrimSpace(c.ChainBlocker.Task)
	case c.MachineLocked:
		return "this machine is too old to dispatch to — update it"
	case c.Conflict != nil && c.Conflict.Count > 0:
		if c.Conflict.Resolvable {
			return fmt.Sprintf("conflicts with %d open PR(s) — rebasable", c.Conflict.Count)
		}
		return fmt.Sprintf("conflicts with %d open PR(s)", c.Conflict.Count)
	case c.Status == "failed" && c.Detail != "":
		return c.Detail
	case c.NoPR != nil:
		return c.NoPR.Detail
	case c.PRURL != "":
		return c.PRURL
	case c.LastLine != "":
		return strings.TrimSpace(c.LastLine)
	case c.Unscheduled && c.ReadinessNote != "":
		return c.ReadinessNote
	case c.Detail != "":
		return c.Detail
	case c.ParentLabel != "":
		return c.ParentLabel
	}
	return "—"
}

func (m *boardModel) chainHeaderLine(r boardRow, w int, sel bool) string {
	glyph := "▾"
	if m.collapsed[r.ChainID] {
		glyph = "▸"
	}
	line := fmt.Sprintf("%s%s chain · %d steps%s", boardDim, glyph, r.Count, reset)
	if sel {
		return boardSel + boardCol + padVis(line, w) + reset
	}
	return padVis(line, w)
}

// emptyColumnNote teaches the next step instead of printing "empty" five times. A board that is
// empty in a particular column usually means something specific, and saying which is the difference
// between an empty state and a dead end.
func emptyColumnNote(col api.BoardColumn) string {
	switch col {
	case api.ColBacklog:
		return "nothing queued\npress n to add work"
	case api.ColBuilding:
		return "nothing building"
	case api.ColBlocked:
		return "nothing stuck"
	case api.ColReview:
		return "nothing to review"
	case api.ColAccepted:
		return "nothing accepted yet"
	}
	return "empty"
}

// statusLine carries the last thing that happened — an action's result, or a refresh error. Errors
// live here rather than in a modal: the board keeps working with stale data, and a modal over a
// working board would be a worse answer than a line saying the data is old.
func (m *boardModel) statusLine(w int) string {
	if m.toast != "" {
		clr := boardGood
		if m.toastBad {
			clr = boardBad
		}
		return clipVis(clr+m.toast+reset, w)
	}
	if m.err != nil {
		return clipVis(boardBad+"could not refresh: "+m.err.Error()+reset, w)
	}
	// A manual source leads with its age: the operator needs to know the board is a snapshot before
	// they read anything off it.
	if note := freshnessNote(m.data); note != "" {
		if c, ok := m.focused(); ok && c.Foreign {
			return clipVis(boardDim+note+"  ·  i imports this into partyline"+reset, w)
		}
		return clipVis(boardDim+note+reset, w)
	}
	if c, ok := m.focused(); ok {
		if a, has := primaryAction(*c); has {
			return clipVis(boardDim+"⏎ "+a.Label+" — "+a.Hint+reset, w)
		}
		return clipVis(boardDim+"nothing to do on this card — it is finished"+reset, w)
	}
	return ""
}

func centerNote(w, h int, note string) []string {
	out := make([]string, h)
	for i := range out {
		out[i] = strings.Repeat(" ", w)
	}
	if h > 0 {
		pad := max(0, (w-len(note))/2)
		out[h/2] = strings.Repeat(" ", pad) + boardDim + note + reset
	}
	return out
}
