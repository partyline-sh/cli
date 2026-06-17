package main

import (
	"fmt"
	"time"

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

func (h *llmsHome) HandleKey(b []byte) ptymux.HomeAction {
	m := h.m
	// ⏎ on an already-live session jumps straight to it — no resume modal, no duplicate.
	if !m.picking && !m.filter && !m.help && len(b) > 0 && (b[0] == '\r' || b[0] == '\n') {
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
	// A pending shell-out (the diff pager) — let the mux suspend the terminal for it.
	if m.suspendFn != nil {
		fn := m.suspendFn
		m.suspendFn = nil
		return ptymux.HomeAction{Suspend: fn}
	}
	if chosen != nil {
		m.markUsed(chosen.ID) // opening it = using it; keeps it atop "last used" after close
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
