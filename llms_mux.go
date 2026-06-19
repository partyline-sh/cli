package main

import (
	"fmt"
	"time"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/ptymux"
)

// llmsHome adapts the aiMenu (the `ptln llms` browser) to the ptymux.Home interface, so
// the mux can host it as the persistent launcher screen. The mux owns the terminal; this
// adapter just renders the menu and translates its keys into mux actions.
type llmsHome struct {
	m   *aiMenu
	mux *ptymux.Mux
}

func (h *llmsHome) Render(cols, rows int) {
	h.m.w, h.m.h = cols, rows
	if h.m.w < 40 || h.m.h < 10 {
		h.m.w, h.m.h = 80, 24
	}
	// Mark sessions already running in the mux. When that set changes (you opened or
	// closed one), re-sort so live sessions float to the top in recent sort — keeping the
	// highlighted session selected across the reorder so the cursor doesn't jump.
	live := h.mux.LiveKeys()
	if !sameLive(h.m.live, live) {
		var sel string
		if s := h.m.selected(); s != nil {
			sel = s.ID
		}
		h.m.live = live
		h.m.applyFilter()
		if sel != "" {
			h.m.selectByID(sel)
		}
	}
	h.m.render()
}

// markedSpecs builds open-specs for the sessions selected with space, in display order.
// They open with default permissions (the bare resume argv) — open one on its own (⏎ with
// nothing selected) if you want to pick a permission mode for it.
func (h *llmsHome) markedSpecs() []ptymux.Spec {
	m := h.m
	var specs []ptymux.Spec
	for i := range m.view {
		s := m.view[i]
		if !m.marked[s.ID] || s.resumeArgv == nil {
			continue
		}
		m.markUsed(s.ID)
		specs = append(specs, ptymux.Spec{Label: muxLabelFor(s, m.meta), Key: s.ID, Model: sessionModel(s), Argv: s.resumeArgv, Dir: s.resumeDir})
	}
	return specs
}

// sameLive reports whether two live-session sets are equal (both hold only true values).
func sameLive(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// Enter resets transient view state when the mux returns to the launcher, so coming back
// from a session always shows the full list — a lingering search filter was hiding sessions
// (you'd see only the matches from before you opened one). Selection + sort/theme persist.
func (h *llmsHome) Enter() {
	if h.m.query != "" || h.m.filter {
		h.m.filter, h.m.query = false, ""
		h.m.applyFilter()
	}
}

func (h *llmsHome) HandleKey(b []byte) ptymux.HomeAction {
	m := h.m
	enter := len(b) > 0 && (b[0] == '\r' || b[0] == '\n')
	ready := !m.picking && !m.filter && !m.help && !m.renaming
	// 'w' from the launcher pops the live-session picker (jump straight to an open session),
	// the same modal as ctrl-\ w inside a session. No-op (with a hint) if nothing's open.
	if ready && len(b) == 1 && b[0] == 'w' {
		if len(h.mux.LiveKeys()) > 0 {
			return ptymux.HomeAction{OpenPicker: true}
		}
		m.flash = "no open sessions — ⏎ to open one"
		return ptymux.HomeAction{}
	}
	// 'S' shares the highlighted session over the relay; pre-check login so we can say so
	// clearly instead of failing inside the picker or the spawned host.
	if ready && len(b) == 1 && b[0] == 'S' {
		if s := m.selected(); s != nil && s.resumeArgv != nil && api.LoadToken() == "" {
			m.flash = "sign in to share — run: ptln login"
			return ptymux.HomeAction{}
		}
	}
	// ⏎ with rows selected (space) opens them all into one multiplexed terminal at once.
	if enter && ready && len(m.marked) > 0 {
		specs := h.markedSpecs()
		m.marked = nil
		if len(specs) > 0 {
			return ptymux.HomeAction{SpawnMany: specs}
		}
	}
	// ⏎ on an already-live session jumps straight to it — no resume modal, no duplicate.
	if enter && ready {
		if s := m.selected(); s != nil && h.mux.LiveKeys()[s.ID] {
			m.markUsed(s.ID) // jumping back to a live one counts as using it
			return ptymux.HomeAction{SwitchKey: s.ID}
		}
	}
	done, chosen := m.handleKey(b)
	// Keep a live session's mux label in sync with a rename just made in the launcher, so
	// the picker + status bar match the new title (idempotent + a no-op if it's not live).
	if s := m.selected(); s != nil {
		h.mux.Relabel(s.ID, muxLabelFor(*s, m.meta))
	}
	// 'n' opened a plain shell as a new mux window.
	if m.spawnShell {
		m.spawnShell = false
		return ptymux.HomeAction{Spawn: shellSpec()}
	}
	// A pending shell-out (the diff pager) — let the mux suspend the terminal for it.
	if m.suspendFn != nil {
		fn := m.suspendFn
		m.suspendFn = nil
		return ptymux.HomeAction{Suspend: fn}
	}
	if chosen != nil {
		m.markUsed(chosen.ID) // opening it = using it; keeps it atop "last used" after close
		// 'S' share: host this one session over the relay via the mux's suspend (your other
		// open sessions stay put; you return to the launcher when the share ends).
		if m.sharing {
			m.sharing = false
			return ptymux.HomeAction{Suspend: shareClosure(*chosen)}
		}
		spec := ptymux.Spec{Label: muxLabelFor(*chosen, m.meta), Key: chosen.ID, Model: sessionModel(*chosen), Argv: chosen.resumeArgv, Dir: chosen.resumeDir}
		return ptymux.HomeAction{Spawn: &spec}
	}
	if done {
		return ptymux.HomeAction{Quit: true}
	}
	return ptymux.HomeAction{} // stayed in home → mux repaints
}

// runLLMSApp builds the persistent launcher (home = the llms browser) and runs the mux.
// initial specs (if any) open straight into live sessions; empty → start at the launcher.
func runLLMSApp(initial []ptymux.Spec) error {
	loadTheme() // restore the user's chosen colour theme (persisted)
	all := collectSessions()
	if len(all) == 0 && len(initial) == 0 {
		fmt.Println("no AI CLI sessions found (looked for claude, codex, gemini, llm)")
		return nil
	}
	m := &aiMenu{all: all, tagline: taglines[time.Now().Minute()%len(taglines)], meta: loadLLMMeta()}
	m.applyFilter()
	h := &llmsHome{m: m}
	mx, err := ptymux.New(h, initial)
	if err != nil {
		return err
	}
	h.mux = mx
	mx.BeforeQuit = saveWorkspace // snapshot open sessions on quit, for `--resume`
	mx.Skin = themed              // re-colour the picker / quit prompt / status bar to the theme
	// Resolve a live child's key (= session id) to "waiting"/"active" for the quit prompt,
	// with a fresh tail-read so the counts reflect the moment you try to quit.
	byID := make(map[string]aiSession, len(all))
	for _, s := range all {
		byID[s.ID] = s
	}
	mx.StatusFn = func(key string) string {
		if s, ok := byID[key]; ok {
			return liveStatus(s)
		}
		return ""
	}
	return mx.Run()
}
