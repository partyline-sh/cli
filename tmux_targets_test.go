package main

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The whole point of the tmux transport: the SAME deliverToAskingSession + pasteBlock that
// serve the built-in mux land a peer answer inside a real tmux pane — staged, not submitted,
func TestTmuxTargetsLiveSessions(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	t.Setenv("PARTYLINE_TMUX_SOCKET", "ptln-test-bus2")
	t.Cleanup(func() { _ = tmuxCmd("kill-server").Run() })
	tmuxKillTestServer(t)
	out, err := tmuxCmd("new-session", "-d", "-P", "-F", "#{window_id}", "-s", tmuxSessionName, "-n", "claude · x", "--", "sleep", "30").Output()
	if err != nil {
		t.Fatal(err)
	}
	tagTmuxWindow(strings.TrimSpace(string(out)), Spec{Label: "claude · x", Key: "k9", Argv: []string{"sleep", "30"}})
	addLegacyFixture(t)
	live := tmuxTargets{}.LiveSessions()
	if len(live) != 1 || live[0].Key != "k9" {
		t.Fatalf("want exactly the keyed session (launcher excluded), got %+v", live)
	}
}

func TestTmuxSessionTargetReattach(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	t.Setenv("PARTYLINE_TMUX_SOCKET", "ptln-test-bus5")
	t.Cleanup(func() { _ = tmuxCmd("kill-server").Run() })
	tmuxKillTestServer(t)
	out, err := tmuxCmd("new-session", "-d", "-P", "-F", "#{window_id}", "-s", tmuxSessionName, "-n", "old", "--", "sleep", "300").Output()
	if err != nil {
		t.Fatal(err)
	}
	win := strings.TrimSpace(string(out))
	tagTmuxWindow(win, Spec{Label: "claude · old", Key: "kr", Argv: []string{"sleep", "300"}, Dir: "/tmp", Thread: "th_9"})

	tgt := newTmuxSessionTarget()
	argv, dir, _, key, ok := tgt.ActiveLaunch()
	if !ok || key != "kr" || dir != "/tmp" || strings.Join(argv, " ") != "sleep 300" {
		t.Fatalf("ActiveLaunch = %v %q %q %v", argv, dir, key, ok)
	}
	if th, _ := tgt.ActiveThreadInfo(); th != "th_9" {
		t.Fatalf("thread = %q", th)
	}

	tgt.SetPendingReattach(Spec{Label: "claude · new", Key: "kr", Argv: []string{"sleep", "999"}, Dir: "/tmp", Thread: "th_9"})
	tgt.drain()

	cmdOut, _ := tmuxCmd("display-message", "-p", "-t", win, "#{pane_current_command}").Output()
	if !strings.Contains(string(cmdOut), "sleep") {
		t.Errorf("respawned pane not running the new command: %q", cmdOut)
	}
	nameOut, _ := tmuxCmd("display-message", "-p", "-t", win, "#{window_name}").Output()
	if !strings.Contains(string(nameOut), "new") {
		t.Errorf("window not renamed: %q", nameOut)
	}
	tgt2 := newTmuxSessionTarget()
	argv2, _, _, _, _ := tgt2.ActiveLaunch()
	if strings.Join(argv2, " ") != "sleep 999" {
		t.Errorf("retag lost the new argv: %v", argv2)
	}
	if _, wired := tgt2.ActiveThreadInfo(); !wired {
		t.Error("a relaunch with a thread must read as wired")
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// New windows append after retiring an obsolete fixture.
func TestTmuxMergeAppendsAfterRetiringFixture(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	t.Setenv("PARTYLINE_TMUX_SOCKET", "ptln-test-launcher-target")
	t.Cleanup(func() { _ = tmuxCmd("kill-server").Run() })
	tmuxKillTestServer(t)

	if _, err := tmuxMerge([]Spec{{Label: "w1", Key: "k1", Argv: []string{"sleep", "60"}, Dir: "/tmp"}}); err != nil {
		t.Fatalf("first merge: %v", err)
	}
	addLegacyFixture(t)
	retireFixtureWindows()
	// the failing case: launcher exists, a fresh session opens
	if _, err := tmuxMerge([]Spec{{Label: "w2", Key: "k2", Argv: []string{"sleep", "60"}, Dir: "/tmp"}}); err != nil {
		t.Fatalf("open with launcher present: %v", err)
	}
	out, _ := tmuxCmd("list-windows", "-t", tmuxSessionName, "-F", "#{window_name}").Output()
	names := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(names) != 2 || names[len(names)-1] != "w2" {
		t.Fatalf("new session must be last, got %v", names)
	}
}

// tmuxKillTestServer stops the test socket's server and waits until it has gone. kill-server
// returns before the old server has exited, and a new-session that reaches the dying server fails
// with "server exited unexpectedly" (seen about one run in five on this machine).
func tmuxKillTestServer(t *testing.T) {
	t.Helper()
	_ = tmuxCmd("kill-server").Run()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if tmuxCmd("list-sessions").Run() != nil {
			out, _ := tmuxCmd("list-sessions").CombinedOutput()
			if strings.Contains(string(out), "no server running") || strings.Contains(string(out), "error connecting") {
				return
			}
		}
	}
	t.Fatal("the test tmux server was still running 3 s after kill-server")
}

func addLegacyFixture(t *testing.T) string {
	t.Helper()
	out, err := tmuxCmd("new-window", "-d", "-P", "-F", "#{window_id}", "-t", tmuxSessionName, "-n", "Loops", "sleep 30").Output()
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSpace(string(out))
	tagTmuxWindow(id, Spec{Key: legacyLoopsKey, Argv: []string{"sleep", "30"}})
	return id
}
