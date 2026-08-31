package main

// sessionMenuTarget is what the per-session menus (ctrl-\ c/m/w/g, New/Run) need from
// whichever backend hosts the focused session. *ptymux.Mux satisfies it unchanged; the tmux
// backend binds one to the active window (tmux_session_target.go), which is how the same
// menu code serves both.
import (
	"partyline.sh/partyline/internal/ptymux"
	"partyline.sh/partyline/internal/ptysess"
)

type sessionMenuTarget interface {
	ActiveLaunch() (argv []string, dir, label, key string, ok bool)
	ActiveMCPs() []string
	ActiveThreadInfo() (thread string, wired bool)
	SetActiveThread(id, label string)
	SetPendingOpen(sp ptymux.Spec)
	SetPendingReattach(sp ptymux.Spec)
}

// peerMenuTarget is the peer inbox's need: who the focused session is, and a place to
// paste an answer. Nil sessIO = no injectable session (the launcher, a shell window).
type peerMenuTarget interface {
	ActiveLaunch() (argv []string, dir, label, key string, ok bool)
	ActiveSessionIO() (sessIO, string)
	SetBanner(string) // the modal arms a background watch on esc — its result lands as a banner
}

// shareMenuTarget: the share overlay's need — the focused session as a real *ptysess.Session
// (the relay host serves its participant hub), and the tab's live-share marker.
type shareMenuTarget interface {
	ActiveShareable() (*ptysess.Session, string)
	SetActiveShared(bool)
}
