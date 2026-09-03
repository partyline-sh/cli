package ptymux

import (
	"strings"
	"testing"
)

// muxWithKeys builds a mux whose children exist only as keyed tabs. The guided split's state
// machine never touches a child's PTY/session, so this is enough to drive it headlessly.
func muxWithKeys(keys ...string) *Mux {
	mx := &Mux{wakeR: -1, wakeW: -1, mode: modeLive}
	for _, k := range keys {
		mx.children = append(mx.children, &child{key: k, label: k})
	}
	return mx
}

// ribbon describes the mux's ribbon the way the user sees it: one entry per slot, each rendered
// as either "label" or "left+right" for the split. Plus the slot the highlight is on.
func (mx *Mux) ribbon() ([]string, int) {
	mx.mu.Lock()
	defer mx.mu.Unlock()
	slots := mx.tabSlotsLocked()
	out := make([]string, len(slots))
	for i, s := range slots {
		out[i] = mx.children[s.main].label
		if s.split() {
			out[i] += "+" + mx.children[s.pair].label
		}
	}
	return out, slotOfChild(slots, mx.active)
}

// openSplit BINDS an empty guided pair and puts it on screen, without any terminal I/O (only
// beginSplit paints), so the pure state machine — binding, focus, filling, parking, cancel — can
// be walked in a test. Mirrors startEmptySplit's state changes exactly.
func (mx *Mux) openSplit(origin *child) *splitState {
	pr := newPairSlot(&pane{}, &pane{}, 0, origin)
	st := newSplitState(pr)
	mx.mu.Lock()
	mx.pairs = append(mx.pairs, pr)
	mx.split = st
	mx.mu.Unlock()
	return st
}

// fill puts child i into pane p and applies the guided focus rule, mirroring fillPane's core
// without the spawn/resize/paint it wraps.
func (mx *Mux) fill(st *splitState, p, i int) {
	mx.mu.Lock()
	st.pr.slots[p].Store(&pane{ch: mx.children[i]})
	st.pr.focus.Store(int32(nextFocus(st.pr, p)))
	mx.active = i
	mx.mu.Unlock()
}

// leave models a navigation away from the split — a tab switch or a spawn. It is deliberately
// EXACTLY parkSplit plus repointing mx.active, because the bug this file guards was a navigation
// path that unbound the pair; anything more elaborate here would hide a regression.
func (mx *Mux) leave(active int) {
	mx.parkSplit()
	mx.mu.Lock()
	mx.active = active
	mx.mu.Unlock()
}

// reenter models gotoSlot → enterPair's state change: the binding goes back on screen, bringing
// its remembered focus with it, and mx.active follows the focused pane.
func (mx *Mux) reenter(pr *pairSlot) *splitState {
	st := newSplitState(pr)
	mx.mu.Lock()
	mx.split = st
	if ch := pr.focusChild(); ch != nil {
		mx.active = mx.indexOfLocked(ch)
	}
	mx.mu.Unlock()
	return st
}

// bound reports whether pr is still in the mux's binding list.
func (mx *Mux) bound(pr *pairSlot) bool {
	mx.mu.Lock()
	defer mx.mu.Unlock()
	for _, p := range mx.pairs {
		if p == pr {
			return true
		}
	}
	return false
}

// spawnTab appends a child the way spawn() does, without a PTY.
func (mx *Mux) spawnTab(label string) int {
	mx.mu.Lock()
	defer mx.mu.Unlock()
	mx.children = append(mx.children, &child{key: label, label: label})
	return len(mx.children) - 1
}

// TestGuidedSplitStateMachine walks the whole user-visible contract of the guided split: an empty
// split opens with the LEFT pane focused, a pick auto-advances focus to the pane still empty (in
// either direction), a filled pair is ONE ribbon tab, and esc out of an unfilled pane leaves no
// split behind.
func TestGuidedSplitStateMachine(t *testing.T) {
	t.Run("empty split focuses left", func(t *testing.T) {
		mx := muxWithKeys("A", "B", "C")
		st := mx.openSplit(mx.children[2])
		if st.focusIdx() != 0 {
			t.Errorf("focus = %d, want 0 (left)", st.focusIdx())
		}
		if !st.pane(0).empty() || !st.pane(1).empty() {
			t.Error("a fresh split must have BOTH panes unfilled")
		}
		if st.filled() {
			t.Error("an empty split reports itself filled")
		}
		// Nothing has been created yet, so the ribbon is untouched.
		if tabs, _ := mx.ribbon(); len(tabs) != 3 {
			t.Errorf("ribbon = %v, want the 3 pre-existing tabs", tabs)
		}
	})

	t.Run("fill left advances focus right", func(t *testing.T) {
		mx := muxWithKeys("A", "B", "C")
		st := mx.openSplit(mx.children[2])
		mx.fill(st, 0, 0) // pick A on the left
		if st.focusIdx() != 1 {
			t.Errorf("focus = %d, want 1 — a pick must auto-advance to the empty pane", st.focusIdx())
		}
		if st.filled() {
			t.Error("one pane filled must not read as a live split")
		}
	})

	t.Run("fill right completes the split as one tab", func(t *testing.T) {
		mx := muxWithKeys("A", "B", "C")
		st := mx.openSplit(mx.children[2])
		mx.fill(st, 0, 0)
		mx.fill(st, 1, 1) // pick B on the right
		if !st.filled() {
			t.Fatal("both panes filled but the split does not report itself filled")
		}
		if st.focusIdx() != 1 {
			t.Errorf("focus = %d, want 1 — the second pick must NOT advance anywhere", st.focusIdx())
		}
		tabs, hl := mx.ribbon()
		want := []string{"A+B", "C"}
		if strings.Join(tabs, "|") != strings.Join(want, "|") {
			t.Errorf("ribbon = %v, want %v — a split is ONE tab", tabs, want)
		}
		if hl != 0 {
			t.Errorf("highlight on slot %d, want 0 (the split)", hl)
		}
	})

	t.Run("fill right first advances focus left", func(t *testing.T) {
		mx := muxWithKeys("A", "B", "C")
		st := mx.openSplit(mx.children[2])
		mx.fill(st, 1, 1) // ctrl-\ tab over, then pick B on the RIGHT first
		if st.focusIdx() != 0 {
			t.Errorf("focus = %d, want 0 — auto-advance is symmetric", st.focusIdx())
		}
		mx.fill(st, 0, 0)
		if st.focusIdx() != 0 {
			t.Errorf("focus = %d, want 0 — the completing pick stays put", st.focusIdx())
		}
		if tabs, _ := mx.ribbon(); tabs[0] != "A+B" {
			t.Errorf("ribbon = %v, want the split merged at A's position", tabs)
		}
	})

	t.Run("close pane leaves one full-width tab holding the other session", func(t *testing.T) {
		mx := muxWithKeys("A", "B", "C")
		st := mx.openSplit(mx.children[2])
		mx.fill(st, 0, 0)
		mx.fill(st, 1, 1)
		// ctrl-\ x closes the FOCUSED pane (right, holding B) — the survivor is A, and B is
		// still a live child (closing a pane never kills the session).
		other := st.pane(1 - st.focusIdx()).ch
		if other != mx.children[0] {
			t.Fatalf("survivor = %v, want child A", other)
		}
		pr := mx.parkSplit()
		mx.mu.Lock()
		mx.unbindLocked(pr) // ctrl-\ x is the only user-facing unbind
		mx.active = mx.indexOfLocked(other)
		mx.mu.Unlock()
		tabs, hl := mx.ribbon()
		want := []string{"A", "B", "C"}
		if strings.Join(tabs, "|") != strings.Join(want, "|") {
			t.Errorf("ribbon = %v, want %v — both sessions are plain tabs again", tabs, want)
		}
		if hl != 0 {
			t.Errorf("highlight on slot %d, want 0 (the surviving session, full width)", hl)
		}
	})

	t.Run("esc in an unfilled pane leaves no phantom tab", func(t *testing.T) {
		mx := muxWithKeys("A", "B", "C")
		st := mx.openSplit(mx.children[2]) // opened from tab C
		if st.pr.origin != mx.children[2] {
			t.Fatal("the split did not record the tab it was opened from")
		}
		pr := mx.parkSplit()
		mx.mu.Lock()
		mx.unbindLocked(pr) // a cancelled setup never completed, so its binding goes too
		back := mx.indexOfLocked(pr.origin)
		mx.active = back
		mx.mu.Unlock()
		if back != 2 {
			t.Errorf("returned to tab %d, want 2 (where the user came from)", back)
		}
		tabs, hl := mx.ribbon()
		want := []string{"A", "B", "C"}
		if strings.Join(tabs, "|") != strings.Join(want, "|") {
			t.Errorf("ribbon = %v, want %v — a cancelled split adds nothing", tabs, want)
		}
		if hl != 2 {
			t.Errorf("highlight on slot %d, want 2", hl)
		}
	})
}

// TestBuildSlotsCollapsesPairs pins the collapse itself: a bound pair is one slot at its LEFT
// pane's position, a half-filled pair is not merged yet, and both halves resolve to the SAME slot
// so ←/→ and 1-9 can never address one. Driven by the BINDING, so it holds while parked.
func TestBuildSlotsCollapsesPairs(t *testing.T) {
	kids := []*child{{label: "A"}, {label: "B"}, {label: "C"}}
	pairOf := func(l, r *child) *pairSlot {
		return newPairSlot(&pane{ch: l}, &pane{ch: r}, 0, nil)
	}
	for _, tc := range []struct {
		name  string
		pairs []*pairSlot
		want  []tabSlot
	}{
		{"no pairs", nil, []tabSlot{{0, -1, nil}, {1, -1, nil}, {2, -1, nil}}},
		{"half filled is not merged", []*pairSlot{pairOf(kids[1], nil)}, []tabSlot{{0, -1, nil}, {1, -1, nil}, {2, -1, nil}}},
		{"merged at left's position", []*pairSlot{pairOf(kids[0], kids[1])}, []tabSlot{{0, 1, nil}, {2, -1, nil}}},
		{"right pane earlier in tab order", []*pairSlot{pairOf(kids[2], kids[0])}, []tabSlot{{1, -1, nil}, {2, 0, nil}}},
		{"same child twice is not a pair", []*pairSlot{pairOf(kids[1], kids[1])}, []tabSlot{{0, -1, nil}, {1, -1, nil}, {2, -1, nil}}},
	} {
		got := buildSlots(kids, tc.pairs)
		if len(got) != len(tc.want) {
			t.Fatalf("%s: got %d slots, want %d", tc.name, len(got), len(tc.want))
		}
		for i := range got {
			if got[i].main != tc.want[i].main || got[i].pair != tc.want[i].pair {
				t.Errorf("%s: slot %d = {%d,%d}, want {%d,%d}", tc.name, i,
					got[i].main, got[i].pair, tc.want[i].main, tc.want[i].pair)
			}
		}
	}
	slots := buildSlots(kids, []*pairSlot{pairOf(kids[0], kids[1])})
	if a, b := slotOfChild(slots, 0), slotOfChild(slots, 1); a != b || a != 0 {
		t.Errorf("pair halves map to slots %d and %d, want both 0", a, b)
	}
	if got := slotOfChild(slots, 2); got != 1 {
		t.Errorf("the tab after the pair is slot %d, want 1", got)
	}
	if slots[0].pr == nil {
		t.Error("a pair slot must carry its binding so gotoSlot can re-enter it")
	}
}

// TestPairSurvivesParking is requirement #1: leaving a split parks it and returning re-enters the
// SAME pair with the remembered focus side and zoom. Nothing about the pair is derived from "is a
// split painting right now".
func TestPairSurvivesParking(t *testing.T) {
	mx := muxWithKeys("A", "B", "C")
	st := mx.openSplit(nil)
	mx.fill(st, 0, 0)
	mx.fill(st, 1, 1)
	pr := st.pr
	// Remember a non-default focus + zoom, the way ctrl-\ tab / ctrl-\ z would.
	pr.focus.Store(0)
	pr.zoom.Store(true)

	mx.leave(2) // switch to slot C
	if mx.split != nil {
		t.Fatal("leaving the split left it on screen")
	}
	if !mx.bound(pr) {
		t.Fatal("navigating away UNBOUND the pair — a split must outlive navigation")
	}
	tabs, hl := mx.ribbon()
	if strings.Join(tabs, "|") != "A+B|C" {
		t.Errorf("parked ribbon = %v, want the pair still merged as one slot", tabs)
	}
	if hl != 1 {
		t.Errorf("highlight on slot %d, want 1 (tab C)", hl)
	}
	// Both panes are still panes while parked — their emulators must stay pane-width so a
	// re-entry can replay them without a resize round-trip through the child.
	lw, _, bodyRows, _ := splitGeom(80, 24)
	if c, r, ok := paneSizeOf([]*pairSlot{pr}, mx.children[0], 80, 24); !ok || r != bodyRows {
		t.Errorf("parked left pane size = (%d,%d,%v), want a pane-height %d", c, r, ok, bodyRows)
	}
	pr.zoom.Store(false)
	if c, _, ok := paneSizeOf([]*pairSlot{pr}, mx.children[0], 80, 24); !ok || c != lw {
		t.Errorf("parked left pane width = %d, want %d", c, lw)
	}
	pr.zoom.Store(true)

	st2 := mx.reenter(pr)
	if st2.pr != pr {
		t.Error("re-entry built a split over a different pair")
	}
	if st2.focusIdx() != 0 {
		t.Errorf("re-entered focus = %d, want the remembered 0 (left)", st2.focusIdx())
	}
	if !st2.zoomed() {
		t.Error("re-entry forgot the remembered zoom state")
	}
	if mx.active != 0 {
		t.Errorf("mx.active = %d, want 0 — it must follow the focused pane", mx.active)
	}
	// Focus the right pane, park, return: the OTHER side must come back.
	pr.focus.Store(1)
	mx.leave(2)
	if st3 := mx.reenter(pr); st3.focusIdx() != 1 || mx.active != 1 {
		t.Errorf("focus %d / active %d after a right-focused park, want 1 / 1", st3.focusIdx(), mx.active)
	}
}

// TestSpawnWhileSplitKeepsPairBound is the user's exact regression: "when I open new single screen
// llm sessions the original 2 items in the split screen turn into 2 full screen tabs". Opening a
// session parks the split and appends a slot; it must NOT unbind the pair or split it into two
// ribbon entries.
func TestSpawnWhileSplitKeepsPairBound(t *testing.T) {
	mx := muxWithKeys("A", "B")
	st := mx.openSplit(nil)
	mx.fill(st, 0, 0)
	mx.fill(st, 1, 1)
	pr := st.pr
	if tabs, _ := mx.ribbon(); strings.Join(tabs, "|") != "A+B" {
		t.Fatalf("ribbon = %v, want a single merged slot before the spawn", tabs)
	}
	// Open three new sessions in a row, each parking the split (spawnOrSwitch → switchTo).
	for _, name := range []string{"D", "E", "F"} {
		i := mx.spawnTab(name)
		mx.leave(i)
		if !mx.bound(pr) {
			t.Fatalf("opening %q unbound the pair", name)
		}
		tabs, _ := mx.ribbon()
		if tabs[0] != "A+B" {
			t.Fatalf("after opening %q the ribbon is %v — the split fell apart into separate tabs", name, tabs)
		}
	}
	tabs, hl := mx.ribbon()
	if strings.Join(tabs, "|") != "A+B|D|E|F" {
		t.Errorf("ribbon = %v, want the pair plus one slot per new session", tabs)
	}
	if hl != 3 {
		t.Errorf("highlight on slot %d, want 3 (the newest session)", hl)
	}
	// And the pair is still exactly ONE stop in the ←/→ cycle.
	if n := len(tabs); n != 4 {
		t.Errorf("%d ribbon slots for 5 children, want 4 (the pair counts once)", n)
	}
}

// TestParkedPairRendersMergedLabel is requirement #3: the label comes from the binding, so a
// PARKED pair still reads "project/project" rather than reverting to two engine-prefixed tabs.
func TestParkedPairRendersMergedLabel(t *testing.T) {
	mx := &Mux{wakeR: -1, wakeW: -1, mode: modeLive, cols: 120}
	for _, l := range []string{"claude · payments", "codex · checkout", "claude · unrelated"} {
		mx.children = append(mx.children, &child{key: l, label: l})
	}
	st := mx.openSplit(nil)
	mx.fill(st, 0, 0)
	mx.fill(st, 1, 1)
	pr := st.pr
	mx.leave(2) // park it

	mx.mu.Lock()
	slots := mx.tabSlotsLocked()
	labels := make([]string, len(slots))
	for i, sl := range slots {
		labels[i] = mx.slotLabelLocked(sl, mx.cols)
	}
	mx.mu.Unlock()
	if len(labels) != 2 {
		t.Fatalf("parked ribbon has %d segments (%v), want 2", len(labels), labels)
	}
	if labels[0] != "payments/checkout" {
		t.Errorf("parked pair label = %q, want %q", labels[0], "payments/checkout")
	}
	if labels[1] != "claude · unrelated" {
		t.Errorf("unpaired tab label = %q, want its own label untouched", labels[1])
	}
	if !mx.bound(pr) {
		t.Error("the pair unbound itself while parked")
	}
}

// TestOnlyClosePaneUnbinds is requirement #4: no amount of navigation dissolves a pair; ctrl-\ x
// does, and then the slot is a single child holding the OTHER session (which keeps running).
func TestOnlyClosePaneUnbinds(t *testing.T) {
	mx := muxWithKeys("A", "B", "C")
	st := mx.openSplit(nil)
	mx.fill(st, 0, 0)
	mx.fill(st, 1, 1)
	pr := st.pr
	for i := 0; i < 25; i++ { // hop away and back, over and over
		mx.leave(2)
		st = mx.reenter(pr)
		if !mx.bound(pr) {
			t.Fatalf("the pair unbound after %d round trips", i+1)
		}
		if tabs, _ := mx.ribbon(); tabs[0] != "A+B" {
			t.Fatalf("round trip %d: ribbon = %v", i+1, tabs)
		}
	}
	// ctrl-\ x on the focused (right) pane: A survives in the slot, B stays a live child.
	pr.focus.Store(1)
	other := pr.other(pr.focusIdx())
	if other != mx.children[0] {
		t.Fatalf("survivor = %v, want child A", other)
	}
	unbound := mx.parkSplit()
	mx.mu.Lock()
	mx.unbindLocked(unbound)
	mx.active = mx.indexOfLocked(other)
	mx.mu.Unlock()
	if mx.bound(pr) {
		t.Error("ctrl-\\ x did not unbind the pair")
	}
	tabs, hl := mx.ribbon()
	if strings.Join(tabs, "|") != "A|B|C" {
		t.Errorf("ribbon = %v, want three single slots", tabs)
	}
	if hl != 0 {
		t.Errorf("highlight on slot %d, want 0 (the surviving session)", hl)
	}
	mx.mu.Lock()
	stillLive := mx.indexOfLocked(mx.children[1]) >= 0
	mx.mu.Unlock()
	if !stillLive {
		t.Error("the closed pane's session was removed — closing a pane must not kill it")
	}
}

// TestSplitTabLabel pins the merged label: two project names joined by "/", the engine prefix
// dropped, each half truncated INDEPENDENTLY, and never a "payments/" with an empty tail.
func TestSplitTabLabel(t *testing.T) {
	const l, r = "claude · payments", "codex · checkout"
	for _, tc := range []struct {
		budget int
		want   string
	}{
		{40, "payments/checkout"}, // plenty of room — both names in full
		{17, "payments/checkout"}, // exactly enough
		{14, "payme…/checko…"},
		{9, "pay…/che…"},
		{5, "pa/ch"},
		{3, "p/c"},
		{0, "p/c"}, // clamped to the 3-column floor, never an empty half
	} {
		got := splitTabLabel(l, r, tc.budget)
		if got != tc.want {
			t.Errorf("budget %d: got %q, want %q", tc.budget, got, tc.want)
		}
		if lim := tc.budget; len([]rune(got)) > lim && len([]rune(got)) > 3 {
			t.Errorf("budget %d: %q is wider than its budget", lim, got)
		}
		if strings.HasPrefix(got, "/") || strings.HasSuffix(got, "/") {
			t.Errorf("budget %d: %q has an empty half", tc.budget, got)
		}
	}
	// A short half hands its slack to the long one rather than wasting it.
	if got := splitTabLabel("claude · api", "codex · a-very-long-project", 20); got != "api/a-very-long-pro…" {
		t.Errorf("slack redistribution: got %q", got)
	}
	// A renamed session has no " · " separator and is used whole; a label with no project falls
	// back to something short rather than rendering an empty side.
	if got := splitTabLabel("my rename", " · ", 40); got != "my rename/shell" {
		t.Errorf("rename + empty project: got %q, want %q", got, "my rename/shell")
	}
	// The budget itself shrinks with the terminal, so a split tab is clipped like any other.
	if a, b := splitLabelBudget(40), splitLabelBudget(200); a >= b || a < 9 || b > 40 {
		t.Errorf("budget(40)=%d budget(200)=%d, want a smaller, clamped [9,40] pair", a, b)
	}
}

// TestSwitchToRedirectsToBoundPair pins the funnel that makes parking work: every switch — esc out
// of the manager, ⏎ on a live session, a resize repaint, an exiting neighbour — goes through
// switchTo, and a switch onto EITHER half of a bound pair must re-enter that pair rather than paint
// one session full width. This is the guard that stops a split falling apart into two tabs.
func TestSwitchToRedirectsToBoundPair(t *testing.T) {
	mx := muxWithKeys("A", "B", "C")
	st := mx.openSplit(nil)

	// Mid-setup (only the left pane picked) there is nothing to return to yet.
	mx.fill(st, 0, 0)
	mx.mu.Lock()
	half := mx.reentryTargetLocked(0)
	mx.mu.Unlock()
	if half != nil {
		t.Error("a half-filled setup must not be treated as a tab to re-enter")
	}

	mx.fill(st, 1, 1)
	pr := st.pr
	mx.leave(2)
	for _, i := range []int{0, 1} { // both halves route to the same pair
		mx.mu.Lock()
		got := mx.reentryTargetLocked(i)
		mx.mu.Unlock()
		if got != pr {
			t.Errorf("switchTo(child %d) targets %v, want the bound pair", i, got)
		}
	}
	mx.mu.Lock()
	plain := mx.reentryTargetLocked(2)
	oob := mx.reentryTargetLocked(99)
	mx.mu.Unlock()
	if plain != nil {
		t.Error("an unpaired tab must still paint full width")
	}
	if oob != nil {
		t.Error("an out-of-range index must not resolve to a pair")
	}

	// After ctrl-\ x there is no binding, so switches go back to the plain full-width path.
	// (closePane acts on the split ON SCREEN, so return to it first.)
	mx.reenter(pr)
	unbound := mx.parkSplit()
	if unbound != pr {
		t.Fatalf("parkSplit returned %v, want the pair that was on screen", unbound)
	}
	mx.mu.Lock()
	mx.unbindLocked(unbound)
	again := mx.reentryTargetLocked(0)
	mx.mu.Unlock()
	if again != nil {
		t.Error("an unbound pair still redirects switchTo")
	}
}

// TestParkedSetupIsDiscarded: navigating away from a still-being-picked split abandons it exactly
// like esc does, so no invisible binding (and no pair of live in-pane managers) is left behind.
func TestParkedSetupIsDiscarded(t *testing.T) {
	mx := muxWithKeys("A", "B", "C")
	st := mx.openSplit(mx.children[2])
	mx.fill(st, 0, 0) // one side picked, then the user opens something else
	pr := st.pr
	mx.leave(2)
	if mx.bound(pr) {
		t.Error("an abandoned half-filled setup stayed bound")
	}
	tabs, _ := mx.ribbon()
	if strings.Join(tabs, "|") != "A|B|C" {
		t.Errorf("ribbon = %v, want three plain tabs", tabs)
	}
	// A FILLED pair, by contrast, survives the identical navigation.
	st2 := mx.openSplit(nil)
	mx.fill(st2, 0, 0)
	mx.fill(st2, 1, 1)
	mx.leave(2)
	if !mx.bound(st2.pr) {
		t.Error("a filled pair was discarded by navigation")
	}
}
