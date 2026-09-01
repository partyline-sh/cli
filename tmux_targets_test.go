package main

import (
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"partyline.sh/partyline/internal/ptymux"
)

// The whole point of the tmux transport: the SAME deliverToAskingSession + pasteBlock that
// serve the built-in mux land a peer answer inside a real tmux pane — staged, not submitted,
// because UnsubmittedInput is unknowable through tmux and unknown must stage.
func TestTmuxTargetsDeliverStagesIntoPane(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	t.Setenv("PARTYLINE_TMUX_SOCKET", "ptln-test-bus")
	t.Cleanup(func() { _ = tmuxCmd("kill-server").Run() })
	_ = tmuxCmd("kill-server").Run()

	// a pane running cat: pasted bytes echo straight back onto the screen
	if out, err := tmuxCmd("new-session", "-d", "-s", tmuxSessionName, "-n", "claude · payments", "--", "cat").CombinedOutput(); err != nil {
		t.Fatalf("new-session: %v\n%s", err, out)
	}
	id, _ := tmuxCmd("list-windows", "-t", tmuxSessionName, "-F", "#{window_id}").Output()
	tagTmuxWindow(strings.TrimSpace(string(id)), ptymux.Spec{Label: "claude · payments", Key: "sess-1", Argv: []string{"cat"}})

	tg := tmuxTargets{}
	if _, _, _, ok := tg.SessionByKey("sess-1"); !ok {
		t.Fatal("SessionByKey did not find the tagged pane")
	}
	// the cat pane just emitted its startup — fresh activity reads as "could be typing"
	if _, known := tg.UnsubmittedInput("sess-1"); known {
		t.Fatal("fresh pane activity must read as unknown (could be a typing echo)")
	}

	m := peerMessage{ID: "c1", Peer: "antonio", Project: "payments", Status: taskCompleted,
		Answer: "the flag lives in config.go line 40", Session: "sess-1"}
	mode, banner := deliverToAskingSession(tg, m, pasteBlock)
	if mode != deliverStage {
		t.Fatalf("want staged delivery (unknown typing state), got %v (banner %q)", mode, banner)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if scr := string(tmuxPane{id: strings.TrimSpace(string(id))}.Snapshot()); strings.Contains(scr, "config.go line 40") {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("pasted answer never appeared inside the pane")
}

// LiveSessions excludes the launcher fixture and reports keyed windows — what ask_session
// resolves names against.
func TestTmuxTargetsLiveSessions(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	t.Setenv("PARTYLINE_TMUX_SOCKET", "ptln-test-bus2")
	t.Cleanup(func() { _ = tmuxCmd("kill-server").Run() })
	_ = tmuxCmd("kill-server").Run()
	out, err := tmuxCmd("new-session", "-d", "-P", "-F", "#{window_id}", "-s", tmuxSessionName, "-n", "claude · x", "--", "sleep", "30").Output()
	if err != nil {
		t.Fatal(err)
	}
	tagTmuxWindow(strings.TrimSpace(string(out)), ptymux.Spec{Label: "claude · x", Key: "k9", Argv: []string{"sleep", "30"}})
	ensureLauncherWindow()
	live := tmuxTargets{}.LiveSessions()
	if len(live) != 1 || live[0].Key != "k9" {
		t.Fatalf("want exactly the keyed session (launcher excluded), got %+v", live)
	}
}

// After the quiet window with no pane output, "no evidence of typing" is claimed — the gate
// ask_session's submit depends on.
func TestTmuxTargetsQuietWindow(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	t.Setenv("PARTYLINE_TMUX_SOCKET", "ptln-test-bus3")
	t.Cleanup(func() { _ = tmuxCmd("kill-server").Run() })
	_ = tmuxCmd("kill-server").Run()
	out, err := tmuxCmd("new-session", "-d", "-P", "-F", "#{window_id}", "-s", tmuxSessionName, "-n", "w", "--", "sleep", "30").Output()
	if err != nil {
		t.Fatal(err)
	}
	tagTmuxWindow(strings.TrimSpace(string(out)), ptymux.Spec{Label: "w", Key: "kq", Argv: []string{"sleep", "30"}})
	time.Sleep(tmuxQuietWindow + time.Second)
	if n, known := (tmuxTargets{}).UnsubmittedInput("kq"); !known || n != 0 {
		t.Fatalf("quiet pane should read (0, true), got (%d, %v)", n, known)
	}
}

// checkupTarget over tmux: the focused window's thread comes back from its stored spec,
// no attached client reads as away, and a banner counts as active while display-time runs.
func TestTmuxTargetsCheckupSurface(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	t.Setenv("PARTYLINE_TMUX_SOCKET", "ptln-test-bus4")
	t.Cleanup(func() { _ = tmuxCmd("kill-server").Run() })
	_ = tmuxCmd("kill-server").Run()
	out, err := tmuxCmd("new-session", "-d", "-P", "-F", "#{window_id}", "-s", tmuxSessionName, "-n", "w", "--", "sleep", "30").Output()
	if err != nil {
		t.Fatal(err)
	}
	tagTmuxWindow(strings.TrimSpace(string(out)), ptymux.Spec{Label: "w", Key: "kt", Argv: []string{"sleep", "30"}, Thread: "th_42"})

	tg := tmuxTargets{}
	if th := tg.ActiveThread(); th != "th_42" {
		t.Errorf("ActiveThread = %q, want th_42", th)
	}
	if idle := tg.IdleSince(); idle < time.Hour {
		t.Errorf("no attached client should read as away, got %v", idle)
	}
	tg.SetBanner("test banner")
	if !tg.BannerActive() {
		t.Error("banner should be active immediately after SetBanner")
	}
}

// The session-menu target's full loop against a live server: read the active window's
// launch back from its tag, then a queued reattach respawns the SAME window with the new
// argv, retags it, renames it, and marks the relaunch wired.
func TestTmuxSessionTargetReattach(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	t.Setenv("PARTYLINE_TMUX_SOCKET", "ptln-test-bus5")
	t.Cleanup(func() { _ = tmuxCmd("kill-server").Run() })
	_ = tmuxCmd("kill-server").Run()
	out, err := tmuxCmd("new-session", "-d", "-P", "-F", "#{window_id}", "-s", tmuxSessionName, "-n", "old", "--", "sleep", "300").Output()
	if err != nil {
		t.Fatal(err)
	}
	win := strings.TrimSpace(string(out))
	tagTmuxWindow(win, ptymux.Spec{Label: "claude · old", Key: "kr", Argv: []string{"sleep", "300"}, Dir: "/tmp", Thread: "th_9"})

	tgt := newTmuxSessionTarget()
	argv, dir, _, key, ok := tgt.ActiveLaunch()
	if !ok || key != "kr" || dir != "/tmp" || strings.Join(argv, " ") != "sleep 300" {
		t.Fatalf("ActiveLaunch = %v %q %q %v", argv, dir, key, ok)
	}
	if th, _ := tgt.ActiveThreadInfo(); th != "th_9" {
		t.Fatalf("thread = %q", th)
	}

	tgt.SetPendingReattach(ptymux.Spec{Label: "claude · new", Key: "kr", Argv: []string{"sleep", "999"}, Dir: "/tmp", Thread: "th_9"})
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

// The tap contract: a reader gets the pane's CURRENT screen first (colors included), then
// every byte the pane emits live; stopping switches the pane's pipe off.
func TestTmuxTapStreamsPane(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	t.Setenv("PARTYLINE_TMUX_SOCKET", "ptln-test-tap")
	t.Cleanup(func() { _ = tmuxCmd("kill-server").Run() })
	_ = tmuxCmd("kill-server").Run()
	if out, err := tmuxCmd("new-session", "-d", "-s", tmuxSessionName, "-n", "w", "--", "sh", "-c", "echo BEFORE-TAP; cat").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	pane, _ := tmuxCmd("display-message", "-p", "-t", tmuxSessionName, "#{pane_id}").Output()
	paneID := strings.TrimSpace(string(pane))
	time.Sleep(300 * time.Millisecond) // let BEFORE-TAP land on the screen

	var mu sync.Mutex
	var buf []byte
	w := writerFunc(func(p []byte) (int, error) { mu.Lock(); buf = append(buf, p...); mu.Unlock(); return len(p), nil })
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- tmuxTap(paneID, w, stop) }()

	time.Sleep(400 * time.Millisecond) // snapshot + pipe armed
	_ = tmuxCmd("send-keys", "-t", paneID, "-l", "LIVE-MARKER-77\r").Run()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		s := string(buf)
		mu.Unlock()
		if strings.Contains(s, "BEFORE-TAP") && strings.Contains(s, "LIVE-MARKER-77") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	mu.Lock()
	s := string(buf)
	mu.Unlock()
	if !strings.Contains(s, "BEFORE-TAP") {
		t.Error("tap missing the pre-existing screen snapshot")
	}
	if !strings.Contains(s, "LIVE-MARKER-77") {
		t.Error("tap missing live pane output")
	}
	close(stop)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("tap did not stop")
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if piped, _ := tmuxCmd("display-message", "-p", "-t", paneID, "#{pane_pipe}").Output(); strings.TrimSpace(string(piped)) == "0" {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Error("pipe-pane still on after stop")
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
