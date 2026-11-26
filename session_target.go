package main

type sessionMenuTarget interface {
	ActiveLaunch() (argv []string, dir, label, key string, ok bool)
	ActiveMCPs() []string
	ActiveThreadInfo() (thread string, wired bool)
	SetActiveThread(id, label string)
	SetPendingOpen(sp Spec)
	SetPendingReattach(sp Spec)
}

// peerMenuTarget is the peer inbox's need: who the focused session is, and a place to
// paste an answer. Nil sessIO = no injectable session (the launcher, a shell window).
type peerMenuTarget interface {
	ActiveLaunch() (argv []string, dir, label, key string, ok bool)
	ActiveSessionIO() (sessIO, string)
	SetBanner(string) // the modal arms a background watch on esc — its result lands as a banner
}

// LiveSession identifies an open session by its stable key and display name.
type LiveSession struct {
	Key   string
	Label string
}
