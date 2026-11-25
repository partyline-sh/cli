package main

import (
	"sync"
	"time"
)

type agentObservation struct {
	agent   *detectedAgent
	err     error
	checked time.Time
}

var agentObservations = struct {
	sync.Mutex
	entries map[string]agentObservation
}{entries: make(map[string]agentObservation)}

// Display metadata may lag by a second. Input and transcript identity checks
// deliberately call detectPaneAgent directly; cached observations cannot authorize.
func snapshotPaneAgent(pid, tty string) (*detectedAgent, error) {
	key := pid + "\x00" + tty
	now := time.Now()
	agentObservations.Lock()
	entry, ok := agentObservations.entries[key]
	agentObservations.Unlock()
	if ok && now.Sub(entry.checked) < time.Second {
		return entry.agent, entry.err
	}
	agent, err := detectPaneAgent(pid, tty)
	agentObservations.Lock()
	defer agentObservations.Unlock()
	if len(agentObservations.entries) >= 128 {
		var oldest string
		for k, entry := range agentObservations.entries {
			if oldest == "" || entry.checked.Before(agentObservations.entries[oldest].checked) {
				oldest = k
			}
		}
		delete(agentObservations.entries, oldest)
	}
	agentObservations.entries[key] = agentObservation{agent, err, now}
	return agent, err
}
