package main

// tmuxSessionTarget binds the per-session menus to one tmux window — the one that was
// active when the menu opened. Reads come from the window's stored Spec (@ptln_spec);
// writes queue like the mux's pending* and are DRAINED by the --session-menu runner after
// the menu returns: a reattach becomes respawn-window -k (same window, new argv), an open
// becomes a merge + select. The wired bit rides @ptln_wired: a record-only SetActiveThread
// must not claim the agent has recall/remember.

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"

	"partyline.sh/partyline/internal/ptymux"
	"partyline.sh/partyline/internal/ptysess"
)

type tmuxSessionTarget struct {
	win             string // window id, captured at open
	pendingOpen     *ptymux.Spec
	pendingReattach *ptymux.Spec
}

// newTmuxSessionTarget captures the active window. ok=false when it has no stored spec
// (a bare shell window, the launcher) — the menus' own "open an AI session first" paths.
func newTmuxSessionTarget() *tmuxSessionTarget {
	out, err := tmuxCmd("display-message", "-p", "-t", tmuxSessionName, "#{window_id}").Output()
	if err != nil {
		return &tmuxSessionTarget{}
	}
	return &tmuxSessionTarget{win: strings.TrimSpace(string(out))}
}

func (t *tmuxSessionTarget) spec() (ptymux.Spec, bool) {
	if t.win == "" {
		return ptymux.Spec{}, false
	}
	out, err := tmuxCmd("show-options", "-wqv", "-t", t.win, "@ptln_spec").Output()
	if err != nil {
		return ptymux.Spec{}, false
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
	if err != nil {
		return ptymux.Spec{}, false
	}
	var sp ptymux.Spec
	if json.Unmarshal(b, &sp) != nil || len(sp.Argv) == 0 || sp.Key == tmuxLauncherKey {
		return ptymux.Spec{}, false
	}
	return sp, true
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
	out, _ := tmuxCmd("show-options", "-wqv", "-t", t.win, "@ptln_wired").Output()
	return sp.Thread, strings.TrimSpace(string(out)) != "0"
}

func (t *tmuxSessionTarget) SetActiveThread(id, label string) {
	sp, ok := t.spec()
	if !ok {
		return
	}
	sp.Thread, sp.ThreadLabel = id, label
	tagTmuxWindow(t.win, sp)
	_ = tmuxCmd("set-option", "-w", "-t", t.win, "@ptln_wired", "0").Run() // record-only
}

func (t *tmuxSessionTarget) SetPendingOpen(sp ptymux.Spec)     { t.pendingOpen = &sp }
func (t *tmuxSessionTarget) SetPendingReattach(sp ptymux.Spec) { t.pendingReattach = &sp }

// drain applies what the menu queued. Reattach = respawn the SAME window with the new argv
// (the mux's ReplaceActive), then retag so the workspace snapshot and future menus see the
// new launch; a relaunch with a thread is wired by construction.
func (t *tmuxSessionTarget) drain() {
	if sp := t.pendingReattach; sp != nil {
		t.pendingReattach = nil
		args := []string{"respawn-window", "-k", "-t", t.win}
		if sp.Dir != "" {
			args = append(args, "-c", sp.Dir)
		}
		args = append(args, "--")
		args = append(args, sp.Argv...)
		if err := tmuxCmd(args...).Run(); err == nil {
			tagTmuxWindow(t.win, *sp)
			_ = tmuxCmd("set-option", "-w", "-t", t.win, "@ptln_wired", "1").Run()
			if sp.Label != "" {
				_ = tmuxCmd("rename-window", "-t", t.win, tmuxWindowName(sp.Label)).Run()
			}
		}
	}
	if sp := t.pendingOpen; sp != nil {
		t.pendingOpen = nil
		if target, err := tmuxMerge([]ptymux.Spec{*sp}); err == nil {
			_ = tmuxCmd("select-window", "-t", target).Run()
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

// ActiveSessionIO: the bound window's pane as an injectable session — nil for untagged
// windows (launcher, shells), which the inbox renders as "no active session to inject into".
func (t *tmuxSessionTarget) ActiveSessionIO() (sessIO, string) {
	sp, ok := t.spec()
	if !ok {
		return nil, ""
	}
	out, err := tmuxCmd("display-message", "-p", "-t", t.win, "#{pane_id}").Output()
	if err != nil {
		return nil, sp.Label
	}
	return tmuxPane{id: strings.TrimSpace(string(out))}, sp.Label
}

func (t *tmuxSessionTarget) SetBanner(s string) { tmuxTargets{}.SetBanner(s) }

// tmuxTaps: one live tap Session per pane (pipe-pane allows a single pipe). Reused across
// menu opens so "already sharing" state and the stop path find the same Session the relay
// is serving.
var tmuxTaps = struct {
	mu sync.Mutex
	m  map[string]*ptysess.Session
}{m: map[string]*ptysess.Session{}}

// ActiveShareable: a tap Session for the bound window's pane, created on first need. The
// child is `ptln tmux --tap <pane>` — see tmux_tap.go for why this makes the whole relay
// stack work unchanged. View-only by construction (openLine=false).
func (t *tmuxSessionTarget) ActiveShareable() (*ptysess.Session, string) {
	sp, ok := t.spec()
	if !ok {
		return nil, ""
	}
	out, err := tmuxCmd("display-message", "-p", "-t", t.win, "#{pane_id}").Output()
	if err != nil {
		return nil, sp.Label
	}
	pane := strings.TrimSpace(string(out))
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

// SetActiveShared lights the ribbon's share marker on the bound window.
func (t *tmuxSessionTarget) SetActiveShared(on bool) {
	v := ""
	if on {
		v = "1"
	}
	_ = tmuxCmd("set-option", "-w", "-t", t.win, "@ptln_shared", v).Run()
}

// dropIdleTap tears down a tap the share menu created but never used (opened the menu,
// pressed esc) — an idle pipe-pane serves nobody.
func (t *tmuxSessionTarget) dropIdleTap() {
	out, err := tmuxCmd("display-message", "-p", "-t", t.win, "#{pane_id}").Output()
	if err != nil {
		return
	}
	pane := strings.TrimSpace(string(out))
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
