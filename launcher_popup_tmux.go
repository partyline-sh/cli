package main

import (
	"os"
	"strconv"
	"time"
)

// launcherPopupSize is the launcher's size on a client of cols x rows: 144x45, half again the
// approval popup's 96x30 because the list and its detail sit side by side, or the client less a
// border where it is smaller (tmux refuses a popup larger than its client and shows nothing).
func launcherPopupSize(cols, rows int) (int, int) {
	w, h := 144, 45
	if cols > 0 {
		w = min(w, max(1, cols-2))
	}
	if rows > 0 {
		h = min(h, max(1, rows-2))
	}
	return w, h
}

// launcherPopupCommand fits the client even when it is smaller than the usual popup minimum.
func launcherPopupCommand(self string, cols, rows int) []string {
	w, h := launcherPopupSize(cols, rows)
	cmd := []string{"display-popup", "-E", "-w", strconv.Itoa(w), "-h", strconv.Itoa(h), "-T", "#[align=centre fg=" + tmuxTheme().Focus + ",bold] ☎ sessions ", self, "tmux", "--launcher-tui"}
	if launcherSearch {
		cmd = append(cmd, "--search")
	}
	return cmd
}

// launcherSearch opens the launcher popup with search already active: set by --search on
// `ptln tmux --home` / `--launcher-tui`, or by the welcome screen's "/ find a session" door. The
// popup is a separate process from whoever asks for it, so it travels as that flag.
var launcherSearch bool

// launcherFocusSession reuses pane-key deduplication and selects both window and pane.
func launcherFocusSession(s aiSession, meta map[string]sessMeta) error {
	mt := meta[s.ID]
	mt.LastUsed = time.Now()
	meta[s.ID] = mt
	saveLLMMeta(meta)
	spec := prepareLaunchSpec(Spec{Label: muxLabelFor(s, meta), Key: s.ID, Model: sessionModel(s), Argv: s.resumeArgv, Dir: s.resumeDir})
	target, err := tmuxMerge([]Spec{spec})
	if err != nil {
		return err
	}
	return tmuxFocus(target)
}

// launcherLiveKeys identifies sessions running in tmux for shared sorting and expansion.
func launcherLiveKeys() map[string]bool {
	live := map[string]bool{}
	for _, s := range (tmuxTargets{}).LiveSessions() {
		live[s.Key] = true
	}
	return live
}

// launcherCacheAge is how stale the cached scan may get before the watcher scans again. The scan
// costs ~120 ms and the watcher ticks every 2 s, so it is not run every tick; the popup scans
// again itself once it has painted, so the cache only has to be close.
const launcherCacheAge = 30 * time.Second

// refreshLauncherCache publishes a watcher scan and its running sessions when the cache is stale.
func refreshLauncherCache() {
	if fi, err := os.Stat(launcherCachePath()); err == nil && time.Since(fi.ModTime()) < launcherCacheAge {
		return
	}
	saveLauncherCache(collectSessions(), launcherLiveKeys())
}
