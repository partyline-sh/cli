package main

// tmuxSessionTarget binds the per-session menus to one tmux PANE — the one that was active
// when the menu opened. Reads come from the pane's stored Spec (@ptln_spec);
// writes queue like the mux's pending* and are DRAINED by the --session-menu runner after
// the menu returns: a reattach becomes respawn-window -k (same window, new argv), an open
// becomes a merge + select. The wired bit rides @ptln_wired: a record-only SetActiveThread
// must not claim the agent has recall/remember.

import (
	"os"
	"strings"
)

type tmuxSessionTarget struct {
	pane            string // pane id, captured at open — a merged window holds two sessions
	pendingOpen     *Spec
	pendingReattach *Spec
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

func (t *tmuxSessionTarget) spec() (Spec, bool) {
	if t.pane == "" {
		return Spec{}, false
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

func (t *tmuxSessionTarget) SetPendingOpen(sp Spec)     { t.pendingOpen = &sp }
func (t *tmuxSessionTarget) SetPendingReattach(sp Spec) { t.pendingReattach = &sp }

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
		// Carry the identity across a relaunch. A respawn replaces the process, so without this a
		// session that was wired to a thread (or otherwise reattached) came back WITHOUT
		// PARTYLINE_SESSION_KEY and silently lost the ability to use ask_session — the same defect
		// the spawn path had, but appearing only after a reattach, which is harder to notice.
		args = append(args, tmuxIdentityEnv(*sp)...)
		args = append(args, "--")
		args = append(args, sp.Argv...)
		if err := tmuxCmd(args...).Run(); err == nil {
			tagTmuxPane(t.pane, *sp)
			_ = tmuxCmd("set-option", "-p", "-t", t.pane, "@ptln_wired", "1").Run()
			// Name the window for the sessions actually in it. The old rule — only a lone session
			// may rename — avoided relabelling a neighbour, but it also meant a window never
			// recovered a correct name once a pair broke up.
			tmuxRenameForPanes(t.pane)
		}
	}
	if sp := t.pendingOpen; sp != nil {
		t.pendingOpen = nil
		if target, err := tmuxMerge([]Spec{*sp}); err == nil {
			_ = tmuxFocus(target)
		}
	}
}

// tmuxSessionMenu is the --session-menu runner: bind, show, drain.
func tmuxSessionMenu(which string) {
	t := newTmuxSessionTarget()
	sessionTargetMenu(t, which)
	t.drain()
}

// sessionTargetMenu dispatches a popup against its captured session.
func sessionTargetMenu(t sessionMenuTarget, which string) {
	switch which {
	case "w":
		wtMenu(t)
	case "g":
		keepGoingToggleMenu(t)
	case "n":
		newRunMenu(t)
	case "N":
		// Straight to starting an AI session, skipping the "what kind of thing" question. It
		// still asks engine and permissions — the point of N is fewer questions, not none.
		dir, _ := os.Getwd()
		if _, d, _, _, ok := t.ActiveLaunch(); ok && d != "" {
			dir = d // start where the focused session is, same as newRunMenu
		}
		newSessionFlow(t, dir)
	}
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
