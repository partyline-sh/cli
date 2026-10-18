package ptymux

import (
	"testing"
	"time"
)

// x-in-the-selector (#156): close the HIGHLIGHTED tab without switching to it. The rule under
// test is barCloseTarget — which child a ribbon slot's close ends — because everything after it
// (removal, index fixup, pair dissolve, repaint) is the existing watchExit path a naturally
// exiting child already exercises.
func TestBarCloseTarget(t *testing.T) {
	a, b, c := &child{label: "A"}, &child{label: "B"}, &child{label: "C"}
	kids := []*child{a, b, c}

	plain := buildSlots(kids, nil)
	if got := barCloseTarget(plain, 1); got != 1 {
		t.Errorf("plain slot 1 → child %d, want 1 (B)", got)
	}
	// The virtual launcher slot (== len(slots)) and out-of-range are NOT closable — x there is a
	// no-op and the selector stays up, never a stray End() on some session.
	if got := barCloseTarget(plain, len(plain)); got != -1 {
		t.Errorf("launcher slot → %d, want -1", got)
	}
	if got := barCloseTarget(plain, -1); got != -1 {
		t.Errorf("negative sel → %d, want -1", got)
	}

	// A split slot: closing from the ribbon ends the LEFT pane only — the pair dissolves through
	// watchExit and the survivor becomes a plain tab. One keypress must never kill two sessions.
	pairOf := func(l, r *child) *pairSlot {
		pr := &pairSlot{}
		pr.slots[0].Store(&pane{ch: l})
		pr.slots[1].Store(&pane{ch: r})
		return pr
	}
	slots := buildSlots(kids, []*pairSlot{pairOf(a, b)})
	if len(slots) != 2 { // [A|B] folded into one slot, then C
		t.Fatalf("expected 2 slots with a pair, got %d", len(slots))
	}
	if got := barCloseTarget(slots, 0); got != 0 {
		t.Errorf("split slot → child %d, want 0 (left pane A)", got)
	}
	if got := barCloseTarget(slots, 1); got != 2 {
		t.Errorf("slot after the pair → child %d, want 2 (C)", got)
	}
}

// The busy rule gating the close-confirm (E9 slice 2): only a session with PTY output in the
// last ~1.5s confirms; idle and gate-less (test/fresh) children close instantly.
func TestChildBusy(t *testing.T) {
	if childBusy(nil) {
		t.Error("nil child must not read busy")
	}
	if childBusy(&child{}) {
		t.Error("gate-less child must not read busy — pruning stays fast")
	}
	g := &gate{}
	g.lastOut.Store(time.Now().UnixNano())
	if !childBusy(&child{gate: g}) {
		t.Error("output moments ago must read busy — that's the mis-chord the confirm exists for")
	}
	g2 := &gate{}
	g2.lastOut.Store(time.Now().Add(-10 * time.Second).UnixNano())
	if childBusy(&child{gate: g2}) {
		t.Error("10s-idle child must close without a prompt")
	}
}
