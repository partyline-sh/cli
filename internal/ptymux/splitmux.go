package ptymux

// Split view, mux side: binding a pair, PARKING and re-entering it (ctrl-\ | · tab · z · x) and
// the only two paths that unbind. The pure geometry + frame builder live in split.go, painting in
// splitpaint.go, pane filling + key routing in splitpane.go, the one-tab ribbon in splittab.go.

import "os"

func (mx *Mux) splitActive() bool {
	mx.mu.Lock()
	defer mx.mu.Unlock()
	return mx.split != nil
}

// startEmptySplit opens an EMPTY, guided split — reachable BOTH as ctrl-\ | from a live session
// and as a bare `|` in the session manager (HomeAction.SplitSetup), which is the only way in
// when no session is open yet. A manager goes in BOTH panes, LEFT focused and bright, RIGHT
// dimmed and waiting; you fill the sides one at a time, in either order, and the pane you pick
// in is the pane that gets filled, with focus auto-advancing to the side still empty (fillPane).
func (mx *Mux) startEmptySplit() {
	mx.mu.Lock()
	cols, rows, newHome := mx.cols, mx.rows, mx.NewPaneHomeFn
	// origin is where esc must land: the session we came from, or the launcher (nil) when the
	// split was started from the manager itself.
	var origin *child
	if mx.mode == modeLive {
		origin = mx.curLocked()
	}
	bad := mx.split != nil || newHome == nil
	mx.mu.Unlock()
	if bad {
		return
	}
	leftW, rightW, _, ok := splitGeom(cols, rows)
	if !ok || leftW < paneHomeMinCols || rightW < paneHomeMinCols {
		return // too narrow for two managers side by side
	}
	l, r := newHome(), newHome()
	if l == nil || r == nil {
		return
	}
	l.Enter()
	r.Enter()
	pr := newPairSlot(&pane{home: l}, &pane{home: r}, 0, origin)
	mx.mu.Lock()
	mx.pairs = append(mx.pairs, pr) // bound from the start; it owns a ribbon slot once filled
	mx.mu.Unlock()
	mx.beginSplit(pr)
}

// enterPair puts a bound pair on screen: park whatever split is up, point mx.active at the pair's
// focused pane (so typed input, the bar and every "focused child" query stay correct) and paint.
func (mx *Mux) enterPair(pr *pairSlot) {
	mx.parkSplit()
	mx.mu.Lock()
	if ch := pr.focusChild(); ch != nil {
		if i := mx.indexOfLocked(ch); i >= 0 {
			mx.active = i
		}
	}
	mx.mu.Unlock()
	mx.beginSplit(pr)
}

// beginSplit paints bound pair pr as the on-screen split. Callers validated the geometry.
func (mx *Mux) beginSplit(pr *pairSlot) {
	mx.mu.Lock()
	if mx.split != nil { // never stack two splits
		mx.mu.Unlock()
		return
	}
	st := newSplitState(pr)
	mx.split = st
	kids := append([]*child(nil), mx.children...)
	// The split owns the whole screen and routes keys through the live path (splitHomeKey), so
	// starting it from the launcher is a transition INTO live mode with no child focused yet.
	mx.mode = modeLive
	mx.scrolling, mx.scrollOff = false, 0
	mx.mu.Unlock()

	// No child may write to the terminal while split — we paint every row ourselves. That
	// includes whatever was painting full width a moment ago (we may be arriving from a tab, or
	// from another parked pair), so every gate goes inactive, not just this pair's two.
	mx.outMu.Lock()
	for _, c := range kids {
		c.gate.active = false
	}
	for _, i := range [2]int{0, 1} {
		if p := st.pane(i); p.ch != nil {
			p.ch.gate.paused = false
		}
	}
	os.Stdout.Write(modeDefaults()) // clear the previously-focused child's mouse/paste bleed
	// Autowrap belongs to live mode (the launcher turns it off for its absolute rendering), so
	// re-assert it here — the split may have been started from the manager.
	os.Stdout.WriteString("\x1b[?7h")
	mx.outMu.Unlock()
	mx.resizeAll() // panes are told their HALF width (see resizeAll → paneSizeOf)
	mx.paintSplit()
	mx.drawBar()
	go mx.splitLoop(st)
}

// parkSplit stops PAINTING the split without unbinding its pair: both sessions keep running at
// pane width, the pair stays exactly one ribbon slot, and its focus + zoom are remembered, so
// returning to that slot re-enters the very same split. This is the single teardown used by every
// navigation path (switchTo, gotoHome, spawning a session) — there is deliberately NO route from
// any of them to unbinding. Callers are about to repaint, so it does not repaint itself.
func (mx *Mux) parkSplit() *pairSlot {
	mx.mu.Lock()
	st := mx.split
	mx.split = nil
	mx.mu.Unlock()
	if st == nil {
		return nil
	}
	st.dead.Store(true)
	close(st.stop)
	// A setup that never got both panes filled is not a tab, so navigating away discards it
	// rather than leaving an invisible binding (and two live in-pane managers) behind.
	if !st.filled() {
		mx.mu.Lock()
		mx.unbindLocked(st.pr)
		mx.mu.Unlock()
	}
	return st.pr
}

// cancelSplit abandons the whole guided setup (esc in an unfilled pane) and returns to the tab
// the split was opened from. A session already picked into the other pane keeps running as its
// own ordinary tab; because the split itself is gone from the ribbon, nothing phantom is left
// behind for the user to wonder about.
func (mx *Mux) cancelSplit() {
	mx.mu.Lock()
	st := mx.split
	if st != nil && st.filled() {
		mx.mu.Unlock()
		return // a filled split is a real tab now — esc does not dissolve it, only ctrl-\ x does
	}
	mx.mu.Unlock()
	if st == nil {
		return
	}
	pr := mx.parkSplit()
	mx.mu.Lock()
	mx.unbindLocked(pr) // the setup never completed, so the binding goes with it
	mx.mu.Unlock()
	mx.resizeAll()
	if pr.origin == nil { // started from the manager — "where you came from" IS the manager
		mx.gotoHome()
		return
	}
	mx.mu.Lock()
	idx := mx.indexOfLocked(pr.origin)
	if idx < 0 { // that tab exited while the split was open — fall back to the filled pane
		idx = mx.indexOfLocked(pr.other(pr.focusIdx()))
	}
	mx.mu.Unlock()
	if idx < 0 {
		mx.gotoHome()
		return
	}
	mx.switchTo(idx)
}

// closePane closes the FOCUSED pane (ctrl-\ x). This is the ONLY user-facing way to UNBIND a
// pair: the slot collapses to a single full-width tab holding the other session. It closes the
// pane, not the program — the dropped session keeps running as its own ordinary tab.
func (mx *Mux) closePane() {
	mx.mu.Lock()
	st := mx.split
	mx.mu.Unlock()
	if st == nil {
		return
	}
	other := st.pr.other(st.focusIdx())
	pr := mx.parkSplit()
	mx.mu.Lock()
	mx.unbindLocked(pr)
	mx.mu.Unlock()
	mx.resizeAll() // both sessions go back to the full body size
	if other == nil {
		mx.gotoHome()
		return
	}
	mx.mu.Lock()
	idx := mx.indexOfLocked(other)
	mx.mu.Unlock()
	if idx < 0 {
		mx.gotoHome()
		return
	}
	mx.switchTo(idx)
}

// splitFocusNext moves focus to the other pane (ctrl-\ tab). The side is stored on the BINDING,
// so it survives parking and a later re-entry lands on the same pane. mx.active follows it when
// that pane holds a session, so typed input, the status bar and every existing "focused child"
// query stay correct; an empty (manager) pane leaves mx.active where it was.
func (mx *Mux) splitFocusNext() {
	mx.mu.Lock()
	st := mx.split
	if st == nil {
		mx.mu.Unlock()
		return
	}
	st.pr.focus.Store(1 - st.pr.focus.Load())
	if target := st.focusChild(); target != nil {
		if i := mx.indexOfLocked(target); i >= 0 {
			mx.active = i
		}
	}
	mx.mu.Unlock()
	// Clear the outgoing pane's mouse/paste modes once; the frame re-asserts the new one's.
	mx.outMu.Lock()
	os.Stdout.Write(modeDefaults())
	mx.outMu.Unlock()
	mx.paintSplit()
	mx.drawBar()
}

// splitZoom toggles the focused pane to full width (ctrl-\ z). Like focus, the flag lives on the
// binding and is remembered across parking.
func (mx *Mux) splitZoom() {
	mx.mu.Lock()
	st := mx.split
	if st == nil {
		mx.mu.Unlock()
		return
	}
	st.pr.zoom.Store(!st.pr.zoom.Load())
	mx.mu.Unlock()
	mx.resizeAll()
	mx.paintSplit()
	mx.drawBar()
}

// indexOfLocked resolves a child to its tab index (-1 if gone). Caller holds mx.mu.
func (mx *Mux) indexOfLocked(ch *child) int {
	for i, c := range mx.children {
		if c == ch {
			return i
		}
	}
	return -1
}
