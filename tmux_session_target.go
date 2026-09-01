package main

// tmuxSessionTarget binds the per-session menus to one tmux PANE — the one that was active
// when the menu opened. Reads come from the pane's stored Spec (@ptln_spec);
// writes queue like the mux's pending* and are DRAINED by the --session-menu runner after
// the menu returns: a reattach becomes respawn-window -k (same window, new argv), an open
// becomes a merge + select. The wired bit rides @ptln_wired: a record-only SetActiveThread
// must not claim the agent has recall/remember.

import (
	"strings"
	"sync"

	"partyline.sh/partyline/internal/ptymux"
	"partyline.sh/partyline/internal/ptysess"
)

type tmuxSessionTarget struct {
	pane            string // pane id, captured at open — a merged window holds two sessions
	pendingOpen     *ptymux.Spec
	pendingReattach *ptymux.Spec
}

// newTmuxSessionTarget captures the active pane. ok=false when it has no stored spec
// (a bare shell window, the launcher) — the menus' own "open an AI session first" paths.
func newTmuxSessionTarget() *tmuxSessionTarget {
	out, err := tmuxCmd("display-message", "-p", "-t", tmuxSessionName, "#{pane_id}").Output()
	if err != nil {
		return &tmuxSessionTarget{}
	}
	return &tmuxSessionTarget{pane: strings.TrimSpace(string(out))}
}

func (t *tmuxSessionTarget) spec() (ptymux.Spec, bool) {
	if t.pane == "" {
		return ptymux.Spec{}, false
	}
	return paneSpec(t.pane)
}

func (t *tmuxSessionTarget) ActiveLaunch() (argv []string, dir, label, key string, ok bool) {
	sp, ok := t.spec()
	if !ok {
		return nil, "", "", "", false
	}
	return sp.Argv, sp.Dir, sp.Label, sp.Key, true
}

func (t *tmuxSessionTarget) ActiveMCPs() []string {
	sp, _ := t.spec()
	return sp.MCPs
}

func (t *tmuxSessionTarget) ActiveThreadInfo() (string, bool) {
	sp, ok := t.spec()
	if !ok || sp.Thread == "" {
		return "", false
	}
	out, _ := tmuxCmd("display-message", "-p", "-t", t.pane, "#{@ptln_wired}").Output()
	return sp.Thread, strings.TrimSpace(string(out)) != "0"
}

func (t *tmuxSessionTarget) SetActiveThread(id, label string) {
	sp, ok := t.spec()
	if !ok {
		return
	}
	sp.Thread, sp.ThreadLabel = id, label
	tagTmuxPane(t.pane, sp)
	_ = tmuxCmd("set-option", "-p", "-t", t.pane, "@ptln_wired", "0").Run() // record-only
}

func (t *tmuxSessionTarget) SetPendingOpen(sp ptymux.Spec)     { t.pendingOpen = &sp }
func (t *tmuxSessionTarget) SetPendingReattach(sp ptymux.Spec) { t.pendingReattach = &sp }

// drain applies what the menu queued. Reattach = respawn the SAME pane with the new argv
// (the mux's ReplaceActive), then retag so the workspace snapshot and future menus see the
// new launch; a relaunch with a thread is wired by construction.
func (t *tmuxSessionTarget) drain() {
	if sp := t.pendingReattach; sp != nil {
		t.pendingReattach = nil
		args := []string{"respawn-pane", "-k", "-t", t.pane}
		if sp.Dir != "" {
			args = append(args, "-c", sp.Dir)
		}
		args = append(args, "--")
		args = append(args, sp.Argv...)
		if err := tmuxCmd(args...).Run(); err == nil {
			tagTmuxPane(t.pane, *sp)
			_ = tmuxCmd("set-option", "-p", "-t", t.pane, "@ptln_wired", "1").Run()
			// The window name belongs to the whole window: renaming it for one session of a
			// merged pair would relabel its neighbour too, so only a lone session may rename.
			if sp.Label != "" && tmuxPaneCount(t.pane) == 1 {
				_ = tmuxCmd("rename-window", "-t", t.pane, tmuxWindowName(sp.Label)).Run()
			}
		}
	}
	if sp := t.pendingOpen; sp != nil {
		t.pendingOpen = nil
		if target, err := tmuxMerge([]ptymux.Spec{*sp}); err == nil {
			_ = tmuxFocus(target)
		}
	}
}

// tmuxSessionMenu is the --session-menu runner: bind, show, drain.
func tmuxSessionMenu(which string) {
	t := newTmuxSessionTarget()
	switch which {
	case "c":
		cgSessionMenu(t)
	case "m":
		mcpSessionMenu(t)
	case "w":
		wtMenu(t)
	case "g":
		keepGoingToggleMenu(t)
	case "n":
		newRunMenu(t)
	case "p":
		peerMenu(t)
	case "s":
		shareMenu(t)
		t.dropIdleTap()
	}
	t.drain()
}

// ActiveSessionIO: the bound pane as an injectable session — nil for untagged panes
// (launcher, shells), which the inbox renders as "no active session to inject into".
func (t *tmuxSessionTarget) ActiveSessionIO() (sessIO, string) {
	sp, ok := t.spec()
	if !ok {
		return nil, ""
	}
	return tmuxPane{id: t.pane}, sp.Label
}

func (t *tmuxSessionTarget) SetBanner(s string) { tmuxTargets{}.SetBanner(s) }

// tmuxTaps: one live tap Session per pane (pipe-pane allows a single pipe). Reused across
// menu opens so "already sharing" state and the stop path find the same Session the relay
// is serving.
var tmuxTaps = struct {
	mu sync.Mutex
	m  map[string]*ptysess.Session
}{m: map[string]*ptysess.Session{}}

// ActiveShareable: a tap Session for the bound pane, created on first need. The
// child is `ptln tmux --tap <pane>` — see tmux_tap.go for why this makes the whole relay
// stack work unchanged. View-only by construction (openLine=false).
func (t *tmuxSessionTarget) ActiveShareable() (*ptysess.Session, string) {
	sp, ok := t.spec()
	if !ok {
		return nil, ""
	}
	pane := t.pane
	tmuxTaps.mu.Lock()
	defer tmuxTaps.mu.Unlock()
	if s, ok := tmuxTaps.m[pane]; ok {
		return s, sp.Label
	}
	sess, err := ptysess.New([]string{selfExe(), "tmux", "--tap", pane}, "host", false)
	if err != nil {
		return nil, sp.Label
	}
	tmuxTaps.m[pane] = sess
	go func() { // pane gone or tap stopped → forget it
		<-sess.Done
		tmuxTaps.mu.Lock()
		delete(tmuxTaps.m, pane)
		tmuxTaps.mu.Unlock()
	}()
	return sess, sp.Label
}

// SetActiveShared lights the ribbon's share marker. Window-scoped on purpose: the ribbon is a
// strip of windows, and its format expands at window scope — a pane option would be invisible
// there. "Something in this window is shared" is the honest reading of that marker.
func (t *tmuxSessionTarget) SetActiveShared(on bool) {
	v := ""
	if on {
		v = "1"
	}
	_ = tmuxCmd("set-option", "-w", "-t", t.pane, "@ptln_shared", v).Run()
}

// dropIdleTap tears down a tap the share menu created but never used (opened the menu,
// pressed esc) — an idle pipe-pane serves nobody.
func (t *tmuxSessionTarget) dropIdleTap() {
	pane := t.pane
	if pane == "" {
		return
	}
	tmuxTaps.mu.Lock()
	sess := tmuxTaps.m[pane]
	tmuxTaps.mu.Unlock()
	if sess == nil {
		return
	}
	shareMu.Lock()
	live := shares[sess] != nil
	shareMu.Unlock()
	if !live {
		sess.End()
	}
}
