package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// The watcher (`ptln _watcher`, docs/plans/bubbletea-ui.md §5): one detached process per machine
// that keeps the loop indicators right and wakes idle chat members when no chat view is open to do
// it. It is not a tmux window (that would shift M-1…M-9, the menu digits and chat's /add numbers),
// it delivers nothing (the Stop hook and the listeners do), and it exits with the tmux server.

const (
	watcherTick  = 2 * time.Second
	watcherStale = 30 * time.Second // a heartbeat older than this means no watcher is running
)

func watcherLockPath() string  { return filepath.Join(stateDir(), "watcher.lock") }
func watcherStatePath() string { return filepath.Join(stateDir(), "watcher-state.json") }

// watcherState is what the watcher knows, written every tick for the Loops board and the popups.
type watcherState struct {
	PID       int                       `json:"pid"`
	Heartbeat time.Time                 `json:"heartbeat"`
	Wakes     map[string]string         `json:"wakes,omitempty"`    // member key → why its wake failed, "" when it was woken
	Activity  map[string]memberActivity `json:"activity,omitempty"` // member key → what it is doing
	Notes     []watcherNote             `json:"notes,omitempty"`    // "X is free" notes, newest last, for the chat view
}

type watcherNote struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

func loadWatcherState() watcherState {
	var s watcherState
	if b, err := os.ReadFile(watcherStatePath()); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

func saveWatcherState(s watcherState) {
	b, _ := json.MarshalIndent(s, "", "  ")
	tmp := watcherStatePath() + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, watcherStatePath())
	}
}

// watcherAlive: the heartbeat is recent.
func watcherAlive() bool { return time.Since(loadWatcherState().Heartbeat) < watcherStale }

// ensureWatcher starts a watcher when none is running and partyline's tmux server is up. Called
// from runTmuxApp, the prompt and Stop hooks, and every `ptln loop` and `ptln chat` command; cheap
// when the heartbeat is fresh (one small file read).
func ensureWatcher() {
	if watcherAlive() || !tmuxServerUp() {
		return
	}
	_ = spawnWatcher()
}

// spawnWatcher starts `ptln _watcher` detached; a variable so tests do not start one.
var spawnWatcher = func() error {
	cmd := exec.Command(selfExe(), "_watcher")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// tmuxServerUp: partyline's own tmux server is running.
var tmuxServerUp = func() bool { return tmuxCmd("list-sessions").Run() == nil }

// watcherMain is `ptln _watcher`. A second one exits at once: the lock is held for the process's
// whole life, so exactly one runs.
func watcherMain() {
	lf, err := os.OpenFile(watcherLockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return
	}
	if syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return // another watcher holds it
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go watchRelay(ctx)
	exe := selfExe()
	built := binaryStamp(exe)
	w := newWatcher()
	for {
		if !tmuxServerUp() {
			return // the server is gone: nothing to watch
		}
		// An update replaced the binary: run the new one, so no old code watches for long. The
		// lock is released by exec's close-on-exec and taken again by the new image.
		if s := binaryStamp(exe); !s.IsZero() && !s.Equal(built) {
			_ = lf.Close()
			_ = syscall.Exec(exe, []string{exe, "_watcher"}, os.Environ())
			return
		}
		w.tick(time.Now())
		time.Sleep(watcherTick)
	}
}

func binaryStamp(path string) time.Time {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}
