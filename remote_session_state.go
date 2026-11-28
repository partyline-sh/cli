package main

import (
	"sync"
	"time"
)

// A session's state for the phone's list. The watcher only probes chat-room members, so a pane
// outside the room had no entry and every such session showed idle. Those are read from their own
// screen here, at most once per remoteStateTTL each: the list is rebuilt every send window.

const remoteStateTTL = 2 * time.Second

var remoteStates = struct {
	sync.Mutex
	m map[string]remoteState
}{m: map[string]remoteState{}}

type remoteState struct {
	a  memberActivity
	at time.Time
}

// remotePaneActivity is what the pane's screen shows: working, approval or idle.
func remotePaneActivity(pane string, now time.Time) memberActivity {
	remoteStates.Lock()
	defer remoteStates.Unlock()
	if s, ok := remoteStates.m[pane]; ok && now.Sub(s.at) < remoteStateTTL {
		return s.a
	}
	a := memberActivity{State: "idle"}
	if out, err := tmuxCmd("capture-pane", "-p", "-t", pane).Output(); err == nil {
		a = screenActivity(screenBottom(string(out), 15))
	}
	remoteStates.m[pane] = remoteState{a, now}
	return a
}
