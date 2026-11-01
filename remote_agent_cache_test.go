package main

import (
	"testing"
	"time"
)

func TestDetectedSnapshotCacheNeverAuthorizesInput(t *testing.T) {
	oldRead := readAgentProcesses
	t.Cleanup(func() { readAgentProcesses = oldRead })
	rows := parseAgentProcesses("42 0 42 43 Sun Nov 1 12:00:00 2026 zsh\n43 42 43 43 Sun Nov 1 12:00:01 2026 claude\n")
	calls := 0
	readAgentProcesses = func(string) ([]agentProcess, error) { calls++; return rows, nil }
	const tty = "/dev/partyline-cache-test"
	first, err := snapshotPaneAgent("42", tty)
	if err != nil || first == nil {
		t.Fatal("agent missing", err)
	}
	rows = rows[:1]
	rows[0].foreground = rows[0].group
	cached, err := snapshotPaneAgent("42", tty)
	if err != nil || cached == nil || calls != 1 {
		t.Fatal("snapshot did not reuse observation", calls, err)
	}
	fresh, err := detectPaneAgent("42", tty)
	if err != nil || fresh != nil || calls != 2 {
		t.Fatal("fresh identity used cached actor", calls, err)
	}
	agentObservations.Lock()
	key := "42\x00" + tty
	entry := agentObservations.entries[key]
	entry.checked = time.Now().Add(-2 * time.Second)
	agentObservations.entries[key] = entry
	agentObservations.Unlock()
	after, err := snapshotPaneAgent("42", tty)
	if err != nil || after != nil || calls != 3 {
		t.Fatal("expired observation did not refresh", calls, err)
	}
	agentObservations.Lock()
	delete(agentObservations.entries, key)
	agentObservations.Unlock()
}
