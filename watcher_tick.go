package main

import (
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// One watcher tick: the loop indicators, chat wakes when no chat view is open, the CI state of
// Done loops' PRs, and the heartbeat.

type watcher struct {
	tries map[string]time.Time // last wake attempt per member
	wakes map[string]string
	busy  map[string]time.Time // when each member was first seen working
	notes []watcherNote
	// The pieces a tick drives; variables in tests.
	refreshIndicators func()
	wakeIdle          func(tries map[string]time.Time, setWake func(key, why string, drop bool))
	probe             func() map[string]memberActivity
	onTick            []func() // later steps hang work on the tick here (the approval popup)
}

func newWatcher() *watcher {
	return &watcher{
		tries:             map[string]time.Time{},
		wakes:             map[string]string{},
		busy:              map[string]time.Time{},
		refreshIndicators: loopRefreshIndicators,
		wakeIdle:          chatWakeIdle,
		probe:             probeMembers,
		// The approval popup opens itself when something waits and the person is idle; the update
		// popup likewise when a release is out.
		onTick: []func(){func() { newGateOpener().tick(time.Now()) }, func() { newUpdateOpener().tick(time.Now()) }, loopWakeIdle, refreshLauncherCache, refreshPRChecksInBackground, contractorsReconcile, buildersResume, pausedResume, watchForUpdates},
	}
}

func (w *watcher) tick(now time.Time) {
	w.refreshIndicators()
	activity := map[string]memberActivity{}
	// A chat view from an older build holds the view lock and wakes its own members; waking from
	// here too would wake twice. The chat popup never wakes anyone, so it takes no lock.
	if !chatViewOpen() {
		activity = w.probe()
		w.noteFree(activity, now)
		w.wakeIdle(w.tries, func(key, why string, drop bool) {
			if drop {
				delete(w.wakes, key)
			} else {
				w.wakes[key] = why
			}
		})
	}
	for _, f := range w.onTick {
		f()
	}
	saveWatcherState(watcherState{PID: os.Getpid(), Heartbeat: now, Wakes: w.wakes, Activity: activity, Notes: w.notes})
}

// noteFree records "X is free" when a member that worked for 30 s or more goes idle: the chat view
// showed this when it probed panes itself, and now reads it from the watcher.
func (w *watcher) noteFree(activity map[string]memberActivity, now time.Time) {
	room := loadChatRoom()
	for _, m := range room.Members {
		a, ok := activity[m.Key]
		if !ok {
			continue
		}
		if a.State == "working" {
			if w.busy[m.Key].IsZero() {
				w.busy[m.Key] = now
			}
			continue
		}
		if since := w.busy[m.Key]; !since.IsZero() {
			if worked := now.Sub(since); worked >= 30*time.Second && a.State == "idle" {
				w.notes = append(w.notes, watcherNote{At: now, Text: chatFreeNote(m.Name, worked)})
			}
			delete(w.busy, m.Key)
		}
	}
	if len(w.notes) > 20 {
		w.notes = w.notes[len(w.notes)-20:]
	}
}

// probeMembers is what each chat member is doing in its tab.
func probeMembers() map[string]memberActivity {
	out := map[string]memberActivity{}
	room := loadChatRoom()
	panes := chatPanes()
	for _, m := range room.Members {
		if p, ok := chatPaneFor(panes, m.Key); ok {
			out[m.Key] = probeActivity(p)
		}
	}
	return out
}

// chatViewLockPath is held by an open chat view for its whole life.
func chatViewLockPath() string { return filepath.Join(chatDir(), "view.lock") }

// holdChatView takes the view lock for an open chat view; release it on exit.
func holdChatView() (release func()) {
	_ = os.MkdirAll(chatDir(), 0o700)
	f, err := os.OpenFile(chatViewLockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return func() {}
	}
	if syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB) != nil {
		_ = f.Close()
		return func() {}
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }
}

// chatViewOpen: some chat view holds the view lock (shared, so several views can be open).
func chatViewOpen() bool {
	f, err := os.OpenFile(chatViewLockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false
	}
	defer f.Close()
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return true
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}
