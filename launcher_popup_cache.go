package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// launcherCachedSession preserves the private scan fields needed for preview and resume.
type launcherCachedSession struct {
	Session                    aiSession
	StorePath, ResumeDir, Root string
	ResumeArgv                 []string
}

// launcherSnapshot is a scan and the keys of the sessions running in tmux when it was taken.
type launcherSnapshot struct {
	Sessions []launcherCachedSession
	Live     map[string]bool
}

// launcherCacheTitle is how much of each session's title the cache keeps.
const launcherCacheTitle = 1000

// launcherCachePath keeps large session snapshots out of heartbeat readers.
func launcherCachePath() string { return filepath.Join(stateDir(), "watcher-sessions.json") }

// saveLauncherCache atomically publishes a complete scan.
func saveLauncherCache(all []aiSession, live ...map[string]bool) {
	rows := make([]launcherCachedSession, len(all))
	for i, s := range all {
		// A title is the session's whole first message, ~32 KB on average here: kept whole, 700
		// sessions made a 22 MB cache that took as long to decode as the scan it replaces. The
		// list and preview show far less, and the popup's own refresh brings full titles back for
		// search a moment after it paints.
		if r := []rune(s.Title); len(r) > launcherCacheTitle {
			s.Title = string(r[:launcherCacheTitle])
		}
		rows[i] = launcherCachedSession{s, s.storePath, s.resumeDir, s.root, s.resumeArgv}
	}
	snapshot := launcherSnapshot{Sessions: rows}
	if len(live) > 0 {
		snapshot.Live = live[0]
	}
	b, err := json.Marshal(snapshot)
	if err != nil {
		return
	}
	f, err := os.CreateTemp(stateDir(), "watcher-sessions-*.tmp")
	if err != nil {
		return
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return
	}
	if f.Close() == nil {
		_ = os.Rename(f.Name(), launcherCachePath())
	}
}

// loadLauncherSnapshot reads the scan and running pane keys together.
func loadLauncherSnapshot() ([]aiSession, map[string]bool, bool) {
	b, err := os.ReadFile(launcherCachePath())
	if err != nil {
		return nil, nil, false
	}
	var snapshot launcherSnapshot
	if json.Unmarshal(b, &snapshot) != nil || snapshot.Sessions == nil {
		return nil, nil, false
	}
	all := make([]aiSession, len(snapshot.Sessions))
	for i, r := range snapshot.Sessions {
		all[i] = r.Session
		all[i].storePath, all[i].resumeDir, all[i].root, all[i].resumeArgv = r.StorePath, r.ResumeDir, r.Root, r.ResumeArgv
	}
	return all, snapshot.Live, true
}
