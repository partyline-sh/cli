package ptymux

// Split view: exactly TWO panes side by side, divided vertically, under the SAME single
// full-width status bar. Strictly additive — every pre-existing full-width path runs
// unchanged whenever mx.split == nil.
//
// A pane holds EITHER a live session or an in-pane session manager (a second, independent
// Home instance) that is waiting to be filled: ctrl-\ | opens the split empty, with a manager
// in both panes, and picking in a pane fills THAT pane only. Once both are filled the pair is
// BOUND (pairSlot) and owns exactly ONE ribbon slot for as long as it lives — see splittab.go.
//
// Bound-ness is the load-bearing idea: the split belongs to the tab slot, not to the Mux. So
// navigating away only PARKS the split (mx.split = nil, binding intact) and coming back re-enters
// it with the remembered focus + zoom. Nothing on the switch/spawn path can unbind a pair; only
// ctrl-\ x and a pane's child exiting do.
//
// Layout (1-based rows/cols) for a cols×rows terminal:
//
//	row 1          pane titles — the focused one is brand pink (the focus indicator)
//	rows 2…rows-1  pane bodies: left at col 1, a dim │ divider, right at leftW+2
//	row rows       the mux status bar: ONE row, full width, never painted by a pane
//
// Unlike single-pane mode we never let a child's raw bytes reach the terminal (its escapes
// address the whole screen), so both gates stay inactive and every visible row is written at
// an absolute CUP position from the child's own emulator, which is sized to its pane. Two
// constraints follow and are load-bearing:
//   - a child's DECSTBM scroll region must NOT be re-emitted here (it would make our absolute
//     addressing land inside that margin) — we reset the region instead, and the focused
//     child's real cursor is re-asserted LAST, exactly as single-pane paintBody does;
//   - nothing may address row `rows` (the bar) or beyond, and the body must start at row 2 —
//     the split analogue of the #238 "snapshot lands at row 1" rule.

import (
	"fmt"
	"strings"
	"sync/atomic"
)

// splitTitleRow is the pane-title row; bodies start on the row below it.
const splitTitleRow = 1

// splitDivider is the one-column vertical rule between the panes (dim); splitDimFg is the same
// grey, used to recess a pane that is waiting its turn.
const (
	splitDivider = "\x1b[38;5;240m│\x1b[0m"
	splitDimFg   = "\x1b[38;5;240m"
)

// splitSetupHint is the one-row status-bar line shown while either pane is still unfilled: what
// the outcome is, and the way out. It replaces an instruction modal on purpose — a modal has to
// be dismissed every single time, and is absent exactly when a returning user has forgotten the
// flow, whereas the numbered pane titles + the dimmed waiting pane are always on screen.
const splitSetupHint = "pick two sessions — they become one tab · esc cancels"

// pane is one half of the split. Exactly one of ch / home is non-nil: home is the in-pane
// session manager shown while the pane is still empty, ch is the session once it's filled.
type pane struct {
	ch   *child
	home PaneHome
}

func (p *pane) empty() bool { return p != nil && p.ch == nil }

// title is the pane's header text. An UNFILLED pane's title is a numbered instruction rather
// than a label — the titles are the whole instruction set for the guided setup (see
// splitSetupHint). side is 0 for the left pane, 1 for the right.
func (p *pane) title(side int) string {
	if p == nil {
		return ""
	}
	if p.ch != nil {
		return p.ch.label
	}
	if side == 0 {
		return "① pick the left session"
	}
	return "② then the right"
}

// pairSlot is a BOUND pair of sessions occupying ONE ribbon slot. It is the DURABLE half of the
// split: a split is a property of the tab slot, not of the Mux, so leaving a split only PARKS it
// (mx.split = nil) while this binding stays — both sessions keep running, the ribbon keeps
// showing one segment, and returning re-enters the same two panes with the same focus and zoom.
// Only ctrl-\ x (closePane) and a pane's child exiting ever unbind one.
type pairSlot struct {
	// slots are atomic because filling a pane happens on the input goroutine while the repaint
	// ticker and the SIGWINCH resize goroutine are reading them.
	slots [2]atomic.Pointer[pane]
	// focus (0 = left, 1 = right — ONLY this pane receives typed input) and zoom (the focused
	// pane owns the full width) are written by the input goroutine and read by the repaint
	// ticker, so they're atomic rather than mu-guarded. Both are REMEMBERED across parking.
	focus atomic.Int32
	zoom  atomic.Bool
	// origin is the tab the split was opened from, so esc out of a still-unfilled pane can put
	// the user back exactly where they were (nil = it opened from the launcher). Immutable.
	origin *child
}

// newPairSlot binds a pair with its two panes, initial focus and the tab it opened from.
func newPairSlot(l, r *pane, focus int, origin *child) *pairSlot {
	pr := &pairSlot{origin: origin}
	pr.slots[0].Store(l)
	pr.slots[1].Store(r)
	pr.focus.Store(int32(focus))
	return pr
}

func (pr *pairSlot) pane(i int) *pane { return pr.slots[i].Load() }

func (pr *pairSlot) focusIdx() int {
	if pr.focus.Load() == 1 {
		return 1
	}
	return 0
}

func (pr *pairSlot) focusPane() *pane { return pr.pane(pr.focusIdx()) }

// focusChild is the focused pane's session, or nil while that pane still shows its manager.
func (pr *pairSlot) focusChild() *child { return pr.focusPane().ch }

// has reports whether ch occupies either pane.
func (pr *pairSlot) has(ch *child) bool {
	return ch != nil && (pr.pane(0).ch == ch || pr.pane(1).ch == ch)
}

// filled reports whether both panes hold a live session (i.e. the split is fully set up). Only a
// filled pair is a real tab: a half-filled one is still being picked and owns no ribbon slot.
func (pr *pairSlot) filled() bool { return pr.pane(0).ch != nil && pr.pane(1).ch != nil }

// other returns the pane opposite side i's session (nil when that side is still unfilled).
func (pr *pairSlot) other(i int) *child { return pr.pane(1 - i).ch }

// splitState is the pair CURRENTLY ON SCREEN — the painting session for pr, created on entry and
// discarded on parking. Everything that must survive parking lives on pr, never here.
type splitState struct {
	pr        *pairSlot
	stop      chan struct{}
	lastPaint atomic.Int64 // unixnano of the last frame — repaint when a child's output is newer
	// dead is set by parkSplit BEFORE the caller's full-width repaint, and re-checked inside the
	// output lock just before a frame is written. Without it a split frame built by the repaint
	// ticker could land AFTER the teardown repaint and leave two panes on a single-pane screen.
	dead atomic.Bool
}

func newSplitState(pr *pairSlot) *splitState {
	return &splitState{pr: pr, stop: make(chan struct{})}
}

// The on-screen split forwards every question about its panes to the binding it is painting.
func (st *splitState) pane(i int) *pane   { return st.pr.pane(i) }
func (st *splitState) focusIdx() int      { return st.pr.focusIdx() }
func (st *splitState) focusPane() *pane   { return st.pr.focusPane() }
func (st *splitState) focusChild() *child { return st.pr.focusChild() }
func (st *splitState) has(ch *child) bool { return st.pr.has(ch) }
func (st *splitState) filled() bool       { return st.pr.filled() }
func (st *splitState) zoomed() bool       { return st.pr.zoom.Load() }

// splitGeom resolves the pane widths and body height for a cols×rows terminal. ok is false
// when the terminal is too small to hold titles + a body row + the status bar.
func splitGeom(cols, rows int) (leftW, rightW, bodyRows int, ok bool) {
	if cols < 8 || rows < 4 {
		return 0, 0, 0, false
	}
	leftW = (cols - 1) / 2 // -1 for the divider column
	return leftW, cols - 1 - leftW, rows - 2, true
}

// paneSize is the size child ch must be told it has because it is one of this pair's panes. ok
// is false for any other child (it keeps the normal full body size). A paired child is a pane
// even while its slot is PARKED — keeping its emulator pane-width is what lets a re-entry replay
// it exactly, with no resize round-trip through the child.
func (pr *pairSlot) paneSize(ch *child, cols, rows int) (c, r int, ok bool) {
	leftW, rightW, bodyRows, ok := splitGeom(cols, rows)
	if !ok {
		return 0, 0, false
	}
	if ch == nil {
		return 0, 0, false
	}
	if pr.zoom.Load() && ch == pr.focusChild() {
		return cols, bodyRows, true
	}
	switch ch {
	case pr.pane(0).ch:
		return leftW, bodyRows, true
	case pr.pane(1).ch:
		return rightW, bodyRows, true
	}
	return 0, 0, false
}

// paneHomeMinCols is the narrowest pane an in-pane session manager renders legibly. Below it
// ctrl-\ | declines to open an empty split rather than paint a cramped, unusable list.
const paneHomeMinCols = 30

// paneView is everything splitFrame needs about one pane — no session, no locks, so the
// geometry is unit-testable.
type paneView struct {
	title          string
	lines          []string // body rows, rendered at the pane's own width
	focused        bool
	curCol, curRow int    // 0-based cursor inside the body
	modes          []byte // this child's DEC private modes, re-asserted for the focused pane
}

// splitFrame builds the whole split repaint. Pure. rightW<=0 means zoomed (one pane, no divider).
func splitFrame(l, r paneView, leftW, rightW, bodyRows, rows int) []byte {
	var b strings.Builder
	// Absolute addressing is only correct with no scroll region and origin mode off — a child
	// may have pinned a DECSTBM margin, which must not follow it into the host terminal.
	b.WriteString("\x1b[?25l\x1b[r\x1b[?6l")
	b.WriteString(fmt.Sprintf("\x1b[%d;1H", splitTitleRow))
	b.WriteString(paneTitle(l.title, leftW, l.focused))
	if rightW > 0 {
		b.WriteString(splitDivider)
		b.WriteString(paneTitle(r.title, rightW, r.focused))
	}
	for i := 0; i < bodyRows; i++ {
		row := splitTitleRow + 1 + i
		fmt.Fprintf(&b, "\x1b[%d;1H%s\x1b[0m", row, clipPadANSI(lineAt(l.lines, i), leftW))
		if rightW > 0 {
			fmt.Fprintf(&b, "\x1b[%d;%dH%s", row, leftW+1, splitDivider)
			fmt.Fprintf(&b, "\x1b[%d;%dH%s\x1b[0m", row, leftW+2, clipPadANSI(lineAt(r.lines, i), rightW))
		}
	}
	// The focused pane owns the real cursor: show it, re-assert the child's own modes (which
	// re-hide it if that's what the child asked for), then place it LAST so nothing we paint
	// afterwards can displace it — the same ordering rule as single-pane paintBody.
	f, off, w := l, 1, leftW
	if rightW > 0 && r.focused {
		f, off, w = r, leftW+2, rightW
	}
	b.WriteString("\x1b[?25h")
	b.Write(f.modes)
	fmt.Fprintf(&b, "\x1b[%d;%dH", splitTitleRow+1+clamp(f.curRow, 0, bodyRows-1), off+clamp(f.curCol, 0, w-1))
	return []byte(b.String())
}
