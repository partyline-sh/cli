package main

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// One watcher per machine: a second exits while the first holds the lock; a tick writes the
// heartbeat, refreshes the loop indicators and runs the hooks hung on it; ensureWatcher starts
// one only when the heartbeat is stale and the server is up.
func TestWatcher(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f, err := os.OpenFile(watcherLockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { watcherMain(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("a second watcher kept running while the first held the lock")
	}
	_ = f.Close()

	refreshed, hooked := 0, 0
	w := newWatcher()
	w.refreshIndicators = func() { refreshed++ }
	w.probe = func() map[string]memberActivity { return nil }
	w.wakeIdle = func(map[string]time.Time, func(string, string, bool)) {}
	w.onTick = []func(){func() { hooked++ }}
	now := time.Now()
	w.tick(now)
	if s := loadWatcherState(); !s.Heartbeat.Equal(now) || s.PID != os.Getpid() || refreshed != 1 || hooked != 1 {
		t.Errorf("after a tick: %+v refreshed=%d hooked=%d", s, refreshed, hooked)
	}
	if !watcherAlive() {
		t.Error("a fresh heartbeat does not count as alive")
	}

	spawned := 0
	saved, savedUp := spawnWatcher, tmuxServerUp
	defer func() { spawnWatcher, tmuxServerUp = saved, savedUp }()
	spawnWatcher = func() error { spawned++; return nil }
	tmuxServerUp = func() bool { return true }
	ensureWatcher()
	if spawned != 0 {
		t.Error("started a watcher while one is alive")
	}
	saveWatcherState(watcherState{Heartbeat: now.Add(-time.Minute)})
	ensureWatcher()
	if spawned != 1 {
		t.Errorf("a stale heartbeat started %d watchers", spawned)
	}
	tmuxServerUp = func() bool { return false }
	ensureWatcher()
	if spawned != 1 {
		t.Error("started a watcher with no tmux server")
	}
}

// The watcher wakes idle members but delivers nothing: messages stay unread for the hooks and
// listeners to hand over.
func TestWatcherDoesNotDeliver(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_ = withChatLock(func(r *chatRoom) error {
		r.Members = append(r.Members, chatMember{Key: "pane:%9", Name: "codex-1", Engine: "codex"})
		_, err := postLocked(r, chatMessage{From: "you", Text: "hello"})
		return err
	})
	before := len(unreadFor(readChat(), loadChatRoom().Members[0]))
	woken := 0
	w := newWatcher()
	w.refreshIndicators = func() {}
	w.probe = func() map[string]memberActivity { return nil }
	w.wakeIdle = func(tries map[string]time.Time, set func(string, string, bool)) { woken++; set("pane:%9", "", false) }
	w.tick(time.Now())
	if woken != 1 {
		t.Errorf("wake ran %d times with no chat view open", woken)
	}
	if after := len(unreadFor(readChat(), loadChatRoom().Members[0])); after != before || before == 0 {
		t.Errorf("unread before %d, after %d: the watcher delivered", before, after)
	}
}

// While a chat view is open, it wakes its own members; the watcher does not.
func TestWatcherSkipsALockedRoom(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	release := holdChatView()
	defer release()
	if !chatViewOpen() {
		t.Fatal("an open view is not seen")
	}
	woken := 0
	w := newWatcher()
	w.refreshIndicators = func() {}
	w.probe = func() map[string]memberActivity { t.Error("probed panes while a view is open"); return nil }
	w.wakeIdle = func(map[string]time.Time, func(string, string, bool)) { woken++ }
	w.tick(time.Now())
	if woken != 0 {
		t.Error("the watcher woke members while a chat view was open")
	}
	release()
	if chatViewOpen() {
		t.Error("a closed view still counts as open")
	}
}

// The watcher started as its own process exits when partyline's tmux server is gone.
func TestWatcherExitsWithoutTheServer(t *testing.T) {
	if os.Getenv("PTLN_WATCHER_CHILD") == "1" {
		tmuxServerUp = realTmuxServerUp // ask the (absent) server for real
		watcherMain()
		return
	}
	t.Setenv("HOME", t.TempDir())
	cmd := exec.Command(os.Args[0], "-test.run", "TestWatcherExitsWithoutTheServer")
	cmd.Env = append(os.Environ(), "PTLN_WATCHER_CHILD=1", "PARTYLINE_TMUX_SOCKET=ptln-watcher-test-none")
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the watcher kept running with no tmux server")
	}
}

// A member that worked for 30 s or more and goes idle gets an "is free" note; a short burst does not.
func TestWatcherNotesAFreeMember(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_ = withChatLock(func(r *chatRoom) error {
		r.Members = append(r.Members, chatMember{Key: "pane:%1", Name: "codex-1", Engine: "codex"}, chatMember{Key: "pane:%2", Name: "claude-2", Engine: "claude"})
		return nil
	})
	w := newWatcher()
	t0 := time.Now()
	w.noteFree(map[string]memberActivity{"pane:%1": {State: "working"}, "pane:%2": {State: "working"}}, t0)
	w.noteFree(map[string]memberActivity{"pane:%1": {State: "idle"}, "pane:%2": {State: "working"}}, t0.Add(40*time.Second))
	w.noteFree(map[string]memberActivity{"pane:%2": {State: "idle"}}, t0.Add(41*time.Second))
	if len(w.notes) != 2 {
		t.Fatalf("notes: %+v", w.notes)
	}
	w.noteFree(map[string]memberActivity{"pane:%1": {State: "working"}}, t0.Add(50*time.Second))
	w.noteFree(map[string]memberActivity{"pane:%1": {State: "idle"}}, t0.Add(55*time.Second))
	if len(w.notes) != 2 {
		t.Errorf("a 5 s burst was noted: %+v", w.notes)
	}
}
