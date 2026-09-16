package ptymux

// In-pane session managers: routing keys to the FOCUSED pane's manager and filling that pane
// (and only that pane) with whatever it picked.

// splitHomeFocused reports that the focused pane still shows its session manager, so typed
// keys belong to that manager rather than to a child PTY.
func (mx *Mux) splitHomeFocused() bool {
	mx.mu.Lock()
	defer mx.mu.Unlock()
	return mx.split != nil && mx.split.focusPane().empty()
}

// splitHomeKey feeds one key chunk to the focused pane's manager and acts on the result —
// pane-locally: a Spawn/SwitchKey fills THAT pane, never the whole mux. Returns true to quit.
func (mx *Mux) splitHomeKey(b []byte) bool {
	mx.mu.Lock()
	st := mx.split
	mx.mu.Unlock()
	if st == nil {
		return false
	}
	i := st.focusIdx()
	p := st.pane(i)
	if p.home == nil {
		return false
	}
	act := p.home.HandleKey(b)
	switch {
	case act.Quit:
		return mx.requestQuit()
	case act.SwitchKey != "":
		mx.fillPane(i, Spec{Key: act.SwitchKey})
	case act.Spawn != nil:
		mx.fillPane(i, *act.Spawn)
	case len(act.SpawnMany) > 0:
		mx.fillPane(i, act.SpawnMany[0]) // a pane holds one session; take the first of a batch
	case act.Return: // esc out of an unfilled pane → cancel the whole setup
		mx.cancelSplit()
	case act.Suspend != nil:
		mx.suspend(act.Suspend) // suspend repaints full-width, so rebuild the split frame after
		mx.paintSplit()
		mx.drawBar()
	default:
		mx.paintSplit()
		mx.drawBar()
	}
	return false
}

// fillPane puts the session sp names into pane i — reusing an already-live child with the same
// key, otherwise spawning it. Once both panes hold a session the split is live and the ribbon
// shows it as ONE tab (splittab.go).
func (mx *Mux) fillPane(i int, sp Spec) {
	mx.mu.Lock()
	st := mx.split
	var ch *child
	if sp.Key != "" {
		for _, c := range mx.children {
			if c.key == sp.Key {
				ch = c
				break
			}
		}
	}
	mx.mu.Unlock()
	if st == nil || i < 0 || i > 1 {
		return
	}
	if ch == nil {
		if len(sp.Argv) == 0 || mx.spawn(sp) != nil {
			return
		}
		mx.mu.Lock()
		ch = mx.children[len(mx.children)-1]
		mx.mu.Unlock()
	}
	if st.pane(1-i).ch == ch {
		return // already in the other pane — a session can't be on both sides
	}

	mx.mu.Lock()
	if mx.split != st { // the split went away while we were spawning
		mx.mu.Unlock()
		return
	}
	st.pr.slots[i].Store(&pane{ch: ch})
	// Auto-advance: this is a deliberate two-step setup, so after a pick focus moves to whichever
	// pane still needs filling (either direction). Once both are filled focus stays where it is.
	st.pr.focus.Store(int32(nextFocus(st.pr, i)))
	if idx := mx.indexOfLocked(ch); idx >= 0 {
		mx.active = idx
	}
	mx.mu.Unlock()

	// A newly-spawned child's gate must stay inactive: the split paints every row itself.
	mx.outMu.Lock()
	ch.gate.active, ch.gate.paused = false, false
	mx.outMu.Unlock()
	mx.resizeAll()
	mx.paintSplit()
	mx.drawBar()
}

// nextFocus is the guided flow's focus rule after pane `filled` was just filled: hand focus to
// the OTHER pane while it is still empty, otherwise leave it on the pane just filled. Pure, so
// the state machine is testable without a PTY.
func nextFocus(pr *pairSlot, filled int) int {
	if pr.pane(1-filled).ch == nil {
		return 1 - filled
	}
	return filled
}
