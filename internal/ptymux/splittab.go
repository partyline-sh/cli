package ptymux

// A split IS one tab, not two tabs that are secretly related. That is the whole reason this file
// exists: the ribbon, the digit jump and the ←/→ selector all address RIBBON SLOTS rather than
// child indices, so a pair occupies a single entry and cycling can never land "inside" a half.
//
// Crucially the slots are derived from the durable BINDINGS (mx.pairs), not from "is a split being
// painted right now". A parked pair is still one slot with one merged label, so opening a new
// session or hopping to another tab can never make a split fall apart into two tabs.

import (
	"strings"
	"unicode/utf8"
)

// tabSlot is one ribbon entry. children[main] is what a jump targets; pair >= 0 marks a PAIR
// slot, whose other pane is children[pair] and whose binding is pr.
type tabSlot struct {
	main, pair int
	pr         *pairSlot
}

func (s tabSlot) split() bool { return s.pair >= 0 }

// buildSlots collapses children into ribbon slots: every BOUND pair becomes one slot, positioned
// where its LEFT pane sits in tab order; every unpaired child gets its own. A half-filled pair is
// still being picked, so it is not merged yet (its filled side shows as an ordinary tab). Pure, so
// the whole ribbon contract is testable without a PTY.
func buildSlots(kids []*child, pairs []*pairSlot) []tabSlot {
	side := make(map[*child]*pairSlot, 2*len(pairs))
	for _, pr := range pairs {
		l, r := pr.pane(0).ch, pr.pane(1).ch
		if l == nil || r == nil || l == r {
			continue
		}
		side[l], side[r] = pr, pr
	}
	idx := make(map[*child]int, len(kids))
	for i, c := range kids {
		idx[c] = i
	}
	slots := make([]tabSlot, 0, len(kids))
	for i, c := range kids {
		pr := side[c]
		switch {
		case pr == nil:
			slots = append(slots, tabSlot{main: i, pair: -1})
		case pr.pane(0).ch == c: // the slot lives at the LEFT pane's position
			r, ok := idx[pr.pane(1).ch]
			if !ok { // the right pane isn't in the child list (shouldn't happen) — degrade to a tab
				slots = append(slots, tabSlot{main: i, pair: -1})
				continue
			}
			slots = append(slots, tabSlot{main: i, pair: r, pr: pr})
		default: // the right pane — folded into its pair's slot
		}
	}
	return slots
}

// slotOfChild finds the slot holding child index c (-1 if none) — used to map mx.active onto the
// ribbon highlight, and to seed the ←/→ selector.
func slotOfChild(slots []tabSlot, c int) int {
	for i, s := range slots {
		if s.main == c || s.pair == c {
			return i
		}
	}
	return -1
}

// tabSlotsLocked is buildSlots for the mux's current state. Caller holds mx.mu.
func (mx *Mux) tabSlotsLocked() []tabSlot { return buildSlots(mx.children, mx.pairs) }

// paneSizeOf is the pane geometry for ch across every binding — the pair need not be on screen.
func paneSizeOf(pairs []*pairSlot, ch *child, cols, rows int) (int, int, bool) {
	for _, pr := range pairs {
		if c, r, ok := pr.paneSize(ch, cols, rows); ok {
			return c, r, ok
		}
	}
	return 0, 0, false
}

// slotLabelLocked is a ribbon entry's text: a plain tab's own label, or the pair's merged one.
// Driven by the binding, so a PARKED pair still reads "payments/checkout".
func (mx *Mux) slotLabelLocked(s tabSlot, cols int) string {
	if !s.split() {
		return mx.children[s.main].label
	}
	return splitTabLabel(mx.children[s.main].label, mx.children[s.pair].label, splitLabelBudget(cols))
}

// unbind dissolves a pair: its panes become ordinary tabs again. Reached ONLY from closePane
// (ctrl-\ x), a cancelled setup, and a pane's child exiting. Caller holds mx.mu.
func (mx *Mux) unbindLocked(pr *pairSlot) {
	out := mx.pairs[:0]
	for _, p := range mx.pairs {
		if p != pr {
			out = append(out, p)
		}
	}
	mx.pairs = out
}

// pairOfLocked finds the bound pair holding ch (nil when it is an ordinary tab). Caller holds mx.mu.
func (mx *Mux) pairOfLocked(ch *child) *pairSlot {
	for _, pr := range mx.pairs {
		if pr.has(ch) {
			return pr
		}
	}
	return nil
}

// dropPaired handles a PANE's child exiting: the pair can't reference a dead session, so it is
// unbound and the survivor becomes an ordinary tab. Returns true when a pair really was dissolved.
func (mx *Mux) dropPaired(ch *child) bool {
	mx.mu.Lock()
	pr := mx.pairOfLocked(ch)
	if pr != nil {
		mx.unbindLocked(pr)
	}
	onScreen := pr != nil && mx.split != nil && mx.split.pr == pr
	mx.mu.Unlock()
	if onScreen {
		mx.parkSplit()
	}
	if pr != nil {
		mx.resizeAll() // the survivor goes back to the full body size
	}
	return pr != nil
}

// reentryTargetLocked reports the bound pair a switch to CHILD i must re-enter instead of painting
// that child full width (nil = paint it full width, exactly as before splits existed). A pair
// still being picked is not a tab to return to. Caller holds mx.mu.
func (mx *Mux) reentryTargetLocked(i int) *pairSlot {
	if i < 0 || i >= len(mx.children) {
		return nil
	}
	pr := mx.pairOfLocked(mx.children[i])
	if pr == nil || !pr.filled() {
		return nil
	}
	return pr
}

// gotoSlot acts on ribbon slot i. A pair slot RE-ENTERS its split (restoring the remembered focus
// side + zoom) unless it is already on screen; every other slot is the ordinary full-width switch,
// which parks — never unbinds — whatever split was up.
func (mx *Mux) gotoSlot(i int) {
	mx.mu.Lock()
	slots := mx.tabSlotsLocked()
	ok := i >= 0 && i < len(slots)
	var s tabSlot
	if ok {
		s = slots[i]
	}
	onScreen := s.pr != nil && mx.split != nil && mx.split.pr == s.pr
	cols, rows := mx.cols, mx.rows
	mx.mu.Unlock()
	switch {
	case !ok:
		return
	case s.pr == nil:
		mx.switchTo(s.main)
	case onScreen: // already looking at it — just repaint
		mx.paintSplit()
		mx.drawBar()
	default:
		if _, _, _, geomOK := splitGeom(cols, rows); !geomOK {
			mx.switchTo(s.main) // too small for two panes; the binding survives for a bigger window
			return
		}
		mx.enterPair(s.pr)
	}
}

// ---- the merged label ----

// splitLabelBudget is how many columns a merged label may take before it is truncated. A split
// label is two project names, so it would otherwise push every other tab off the ribbon; a
// third of the row (never silly-small, never sprawling) makes it shrink like any other tab.
func splitLabelBudget(cols int) int {
	b := cols / 3
	if b < 9 {
		b = 9
	}
	if b > 40 {
		b = 40
	}
	return b
}

// splitPaneName reduces a tab label to its PROJECT component: muxLabelFor builds "engine ·
// project", so the engine prefix is dropped; a renamed session (no separator) is used whole.
func splitPaneName(label string) string {
	if i := strings.LastIndex(label, " · "); i >= 0 {
		label = label[i+len(" · "):]
	}
	label = strings.TrimSpace(label)
	if label == "" {
		return "shell" // a plain shell has no project — something short beats an empty half
	}
	return label
}

// splitTabLabel is the split's single ribbon label: the two project names joined by "/", each
// half truncated INDEPENDENTLY (so it can never render as "payments/" with nothing after it)
// and a short half's slack handed to the other one.
func splitTabLabel(left, right string, budget int) string {
	l, r := splitPaneName(left), splitPaneName(right)
	if budget < 3 {
		budget = 3 // one glyph each side of the "/"
	}
	lw := (budget - 1) / 2
	rw := budget - 1 - lw
	if n := utf8.RuneCountInString(l); n < lw {
		rw += lw - n
		lw = n
	}
	if n := utf8.RuneCountInString(r); n < rw {
		lw += rw - n
		rw = n
	}
	return truncName(l, lw) + "/" + truncName(r, rw)
}

// truncName clips s to at most w runes, marking the cut with an ellipsis when there is room.
// Never returns "" for w >= 1.
func truncName(s string, w int) string {
	if w < 1 {
		w = 1
	}
	rs := []rune(s)
	if len(rs) <= w {
		return s
	}
	if w <= 3 {
		return string(rs[:w]) // too narrow for an ellipsis to be worth a whole column
	}
	return string(rs[:w-1]) + "…"
}
