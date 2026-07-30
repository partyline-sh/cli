package main

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
	"partyline.sh/partyline/internal/brand"
)

// ONE FRAME, ONE PAINT.
//
// The bug this file exists to kill: cgBox painted an absolutely-positioned centered box and then
// parked the cursor "for the prompt", after which the caller printed the numbered list with plain
// fmt.Printf. Absolute positioning only holds for the FIRST such line — its trailing \n returns to
// column 0 — so item 1 landed under the box and items 2..n marched down the far left margin, with
// the prompt orphaned at the bottom. Three unrelated UIs on one screen.
//
// The fix is structural, not cosmetic: everything a modal shows (title, body, numbered list, the
// input row, the hint bar) is assembled as interior LINES, and the whole frame is emitted as a
// single positioned write. Nothing prints sequentially after the frame. Redraw = repaint.
//
// Geometry and the warm border colour match the mux's drawCenteredBox (internal/ptymux), which we
// can't call from here; width and clipping come from internal/brand so the borders can't disagree
// by a column. Paint is a PURE function of (lines, cursor, cols, rows) so it is testable headless.
//
// NOT FOR PANES. This paints absolute cursor positions, which is correct for a full-screen modal
// and WRONG inside a composed pane (see ptymux.TestPaneLinesArePositionIndependent). Never route
// pane content through here.

const cgBorder = "\x1b[38;5;215m" // same warm border colour as the mux's drawCenteredBox

// Cursor modes for cgPaintLines' cursorLine argument.
const (
	cgCursorHide = -1 // hide it — nothing will be typed
	cgCursorPark = -2 // park it two rows BELOW the frame (cgBox's legacy cooked-mode behaviour)
)

// cgModal is one composited modal. Body/Items/Hints are pre-styled; the renderer owns the numbering,
// the scroll window, the input row and the footer, so every converted screen lays out identically.
//
// Mode is the brand.HintBar pill and is deliberately EMPTY on these modals: a one-shot modal has no
// mode to indicate, and the title row already names it (an "ASK PEER" pill inside a box titled "Ask
// a peer" is pure redundancy). Kept as a field because the persistent ctrl-\ screens still want one.
type cgModal struct {
	Title  string
	Body   []string // shown above the list
	Items  []string // numbered by the renderer, 1-based
	Prompt string   // the input row, already styled; "" = no input row (and no cursor)
	Hints  []brand.Hint
	Mode   string
	Off    int // index of the first visible item (scroll offset)
}

// cgTermSize is the terminal size for a modal, with the same 80×24 fallback cgBox uses.
func cgTermSize() (cols, rows int) {
	cols, rows = 80, 24
	if c, r, err := term.GetSize(int(os.Stdout.Fd())); err == nil && c > 0 && r > 0 {
		cols, rows = c, r
	}
	return cols, rows
}

// cgItemRow numbers one list row exactly as the old sequential Pick did, so converting a screen
// changes WHERE the list is drawn without changing how a row reads.
func cgItemRow(n int, s string) string {
	return fmt.Sprintf("    %s  %s", sgr(cgKey, fmt.Sprintf("%2d", n)), s)
}

// lines assembles the modal's interior for a terminal of `rows` rows. It returns the lines, the
// index of the line the cursor belongs on (or cgCursorHide), the visible column offset within that
// line, and how many items were actually shown — the caller needs that last one to know whether a
// bare digit can reach an item and to clamp scrolling.
//
// EVERYTHING is fitted into the rows the terminal actually has. When the list doesn't fit, its
// window is truncated and a `↓ n more` row takes the last slot; when the BODY doesn't fit either (a
// peer's question wraps to a dozen lines), it is ellipsised too. A frame taller than the screen
// scrolls the terminal, which tears the box apart — so the frame gives up content, never its shape.
func (m cgModal) lines(rows int) (out []string, cursorLine, cursorVis, shown int) {
	var tail []string
	promptAt := -1
	if m.Prompt != "" {
		tail = append(tail, "")
		promptAt = len(tail)
		tail = append(tail, m.Prompt)
	}
	if len(m.Hints) > 0 {
		tail = append(tail, "", "  "+brand.HintBar(m.Mode, m.Hints, 0))
	}

	// -2 for the border rows, -2 so the modal never sits flush against the screen edges, -2 for the
	// title and the blank under it.
	avail := rows - 6 - len(tail)
	if avail < 2 {
		avail = 2 // a tiny terminal still gets a row and an affordance rather than nothing
	}
	sep := 0
	if len(m.Body) > 0 && len(m.Items) > 0 {
		sep = 1
	}
	room := avail - sep

	off := min(max(m.Off, 0), len(m.Items))
	// The body keeps as much as it wants provided the list still gets a row and its affordance: the
	// body is context you read once, the list is what the screen is FOR.
	bodyRoom := room - min(len(m.Items)-off, 2)
	if bodyRoom < 1 {
		bodyRoom = 1
	}
	body := m.Body
	if len(body) > bodyRoom {
		body = append(append([]string(nil), body[:bodyRoom-1]...), "  "+dim("…"))
	}

	window := m.Items[off:]
	more := 0
	if listRoom := room - len(body); len(window) > listRoom {
		shown, more = max(listRoom-1, 0), len(window)-max(listRoom-1, 0)
		window = window[:shown]
	} else {
		shown = len(window)
	}

	out = append(out, cgBold+"☎  "+m.Title+cgOff, "")
	out = append(out, body...)
	if sep == 1 {
		out = append(out, "")
	}
	for i, it := range window {
		out = append(out, cgItemRow(off+i+1, it))
	}
	if more > 0 {
		out = append(out, fmt.Sprintf("    %s", dim(fmt.Sprintf("↓ %d more", more))))
	}
	cursorLine, cursorVis = cgCursorHide, 0
	if promptAt >= 0 {
		cursorLine = len(out) + promptAt
		cursorVis = brand.VisWidth(m.Prompt)
	}
	out = append(out, tail...)
	return out, cursorLine, cursorVis, shown
}

// cgPaintLines renders interior lines as a centered rounded box and returns the ONE write that
// paints it. Pure: no I/O, no terminal queries — the tests paint into a string and inspect it.
//
// cursorLine indexes `lines` (cgCursorHide hides the cursor, cgCursorPark puts it below the frame
// for the legacy cooked-mode callers); cursorVis is the visible column offset within that line. The
// cursor is clamped into the interior, so no cursor mode can put it outside the border.
func cgPaintLines(lines []string, cursorLine, cursorVis, cols, rows int) string {
	maxW := cols - 4
	if maxW < 8 {
		maxW = 8
	}
	all := make([]string, len(lines))
	for i, l := range lines {
		all[i] = brand.ClipEllipsis(l, maxW)
	}
	w := 0
	for _, l := range all {
		if v := brand.VisWidth(l); v > w {
			w = v
		}
	}
	top := (rows - (len(all) + 2)) / 2
	if top < 1 {
		top = 1
	}
	left := (cols - (w + 4)) / 2
	if left < 1 {
		left = 1
	}

	var b strings.Builder
	b.WriteString("\x1b[2J")
	if cursorLine == cgCursorHide {
		b.WriteString("\x1b[?25l")
	} else {
		b.WriteString("\x1b[?25h")
	}
	fmt.Fprintf(&b, "\x1b[%d;%dH%s╭%s╮\x1b[0m", top, left, cgBorder, strings.Repeat("─", w+2))
	for i, l := range all {
		pad := w - brand.VisWidth(l)
		if pad < 0 {
			pad = 0
		}
		fmt.Fprintf(&b, "\x1b[%d;%dH%s│\x1b[0m %s%s %s│\x1b[0m", top+1+i, left, cgBorder, l, strings.Repeat(" ", pad), cgBorder)
	}
	fmt.Fprintf(&b, "\x1b[%d;%dH%s╰%s╯\x1b[0m", top+1+len(all), left, cgBorder, strings.Repeat("─", w+2))

	switch {
	case cursorLine == cgCursorHide:
	case cursorLine == cgCursorPark:
		row := top + len(all) + 3
		if row > rows {
			row = rows
		}
		fmt.Fprintf(&b, "\x1b[%d;%dH", row, left)
	default:
		if cursorLine < 0 || cursorLine >= len(all) {
			cursorLine = len(all) - 1
		}
		if cursorVis > w {
			cursorVis = w
		}
		fmt.Fprintf(&b, "\x1b[%d;%dH", top+1+cursorLine, left+2+cursorVis)
	}
	return b.String()
}

// cgPaint is cgModal's paint: assemble for the live terminal, emit one write, report how many items
// the frame could show.
func (m cgModal) paint() (shown int) {
	cols, rows := cgTermSize()
	lines, cl, cv, shown := m.lines(rows)
	os.Stdout.WriteString(cgPaintLines(lines, cl, cv, cols, rows))
	return shown
}
