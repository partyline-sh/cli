package main

import (
	"time"
)

// The mux half of the activity marker: notice that a session called a partyline tool and ask for a
// status-row repaint, so the ☎ lights while it happens rather than at the next keystroke.
//
// Structured like startSessionAskWatch (ask_session_watch.go): a slow poll over a local store, in the
// mux process because that is the only place the children live.
//
// It polls rather than watching the filesystem because the thing it is driving is a ~3s light — a
// missed edge costs nothing, an fsnotify watch per session costs descriptors and a platform surface,
// and the poll only ever stats a handful of files. It also repaints ONLY on a transition, so an agent
// making twenty calls in a turn produces one paint when the light comes on and one when it goes off,
// not twenty.

// activityPoll is well under toolActivityFresh (3s), so a call cannot come and go between two polls.
const activityPoll = 500 * time.Millisecond

// activityTarget is the mux surface this needs, as an interface so the loop is testable without
// real PTY children (same reason as askTarget).
type activityTarget interface {
	LiveSessions() []LiveSessionKey
	WakeBar()
}

// LiveSessionKey is the sliver of a live session this needs. Declared here rather than reusing
// ptymux.LiveSession so the test fake does not have to import the mux package.
type LiveSessionKey struct{ Key string }

func startToolActivityWatch(mx activityTarget) {
	if mx == nil {
		return
	}
	go func() {
		lit := map[string]bool{}
		for {
			time.Sleep(activityPoll)
			changed := false
			seen := map[string]bool{}
			for _, s := range mx.LiveSessions() {
				seen[s.Key] = true
				_, live := readToolActivity(s.Key)
				if lit[s.Key] != live {
					lit[s.Key] = live
					changed = true
				}
			}
			// A session that ended stops being polled; drop it so a relaunch under the same key
			// starts dark instead of inheriting a stale "lit" from its predecessor.
			for k := range lit {
				if !seen[k] {
					delete(lit, k)
					forgetToolActivity(k)
				}
			}
			if changed {
				mx.WakeBar()
			}
		}
	}()
}
