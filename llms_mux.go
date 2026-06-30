package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/ptymux"
)

// engineBin maps a tool name (as shown in the launcher) to its executable. `agy` is
// accepted as an alias for antigravity. This is the one place new-session launching and
// the tool list agree on what to exec.
var engineBin = map[string]string{
	"claude": "claude", "codex": "codex", "gemini": "gemini",
	"antigravity": "agy", "agy": "agy",
}

// llmsNew starts a FRESH session of an engine in the mux — the new-session counterpart to
// resuming. `ptln llms new <claude|codex|gemini|antigravity> [dir]`; dir defaults to cwd.
func llmsNew(args []string) {
	if len(args) == 0 {
		fatal(fmt.Errorf("usage: ptln llms new <claude|codex|gemini|antigravity> [dir]"))
	}
	bin := engineBin[strings.ToLower(args[0])]
	if bin == "" {
		fatal(fmt.Errorf("unknown tool %q — try: claude, codex, gemini, antigravity", args[0]))
	}
	if _, err := exec.LookPath(bin); err != nil {
		fatal(fmt.Errorf("%s not found on PATH — is it installed?", bin))
	}
	dir, _ := os.Getwd()
	if len(args) > 1 && strings.TrimSpace(args[1]) != "" {
		if abs, err := filepath.Abs(args[1]); err == nil {
			dir = abs
		}
	}
	spec := ptymux.Spec{
		Label: bin,
		Key:   fmt.Sprintf("new-%s-%d", bin, time.Now().UnixNano()),
		Argv:  []string{bin},
		Dir:   dir,
	}
	if err := runLLMSApp([]ptymux.Spec{spec}); err != nil {
		fatal(err)
	}
}

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
	h.syncLive()
	h.m.render()
}

// syncLive marks the sessions currently running in the mux and, when that set changed,
// re-sorts so live sessions float to the top — preserving the highlighted session across
// the reorder so the cursor doesn't move. Called from Enter() too (before the FIRST paint),
// so the launcher opens already in final order instead of rendering once then jumping.
func (h *llmsHome) syncLive() {
	if h.mux == nil {
		return
	}
	live := h.mux.LiveKeys()
	if sameLive(h.m.live, live) {
		return
	}
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
	m := h.m
	// Dismiss any transient modal/intent left open in a prior visit (permission picker,
	// rename, share) so returning via ctrl-o lands on a clean launcher — not a stale picker.
	m.picking, m.pickArmed, m.pickCustom = false, false, false
	m.renaming, m.renameBuf = false, ""
	m.sharing = false
	m.confirmInstall = false
	if m.query != "" || m.filter {
		m.filter, m.query = false, ""
		m.applyFilter()
	}
	h.syncLive() // float live sessions to the top BEFORE the first paint (no open-time jump)
}

func (h *llmsHome) HandleKey(b []byte) ptymux.HomeAction {
	m := h.m
	// A consent modal (launch approval, or always-on install) owns input — route straight to
	// the menu so the mux-level esc/enter/S shortcuts below can't hijack y/n/esc.
	if m.apprActive() || m.confirmInstall {
		m.handleKey(b)
		return ptymux.HomeAction{}
	}
	enter := len(b) > 0 && (b[0] == '\r' || b[0] == '\n')
	ready := !m.picking && !m.filter && !m.help && !m.renaming
	// esc (when idle — not searching/renaming/in the detail pane, nothing multi-selected)
	// jumps back to the session you came from. Otherwise esc falls through to the menu (it
	// clears a search / a multi-select / the detail focus). Pairs with ctrl-\ o (→ manager).
	if ready && !m.focusR && len(m.marked) == 0 && len(b) == 1 && b[0] == 0x1b {
		if len(h.mux.LiveKeys()) > 0 {
			return ptymux.HomeAction{Return: true}
		}
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
	m := &aiMenu{all: all, tagline: taglines[time.Now().Minute()%len(taglines)], meta: loadLLMMeta(), presence: &daemonPresence{}}
	m.reloadJoinable()                    // S2: show which projects are advertised to parties
	m.setAccount(api.LoadAccount().Email) // "logged in as …" from cache — instant, no network
	m.applyFilter()
	defer m.presence.goOffline() // drop the daemon stream when the launcher exits (Offline)
	h := &llmsHome{m: m}
	mx, err := ptymux.New(h, initial)
	if err != nil {
		return err
	}
	h.mux = mx
	m.presence.wake = mx.Wake // let the daemon stream surface approval banners in real time
	// If the identity cache is empty (logged in before this existed), fetch it once in the
	// background and repaint — never block the launcher's startup on the network.
	if m.account() == "" && api.LoadToken() != "" {
		go func() {
			if p, err := api.New().Me(); err == nil && p.Email != "" {
				_ = api.SaveAccount(api.Account{Email: p.Email, Name: p.DisplayName})
				m.setAccount(p.Email)
				mx.Wake()
			}
		}()
	}
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
