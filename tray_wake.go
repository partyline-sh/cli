//go:build darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
)

// wakeTray starts the menu bar companion if it's installed and not already showing.
//
// Called when the session manager opens: running `ptln llms` is exactly the moment the tray earns
// its keep, because that's when there are sessions whose "waiting on you" state is worth surfacing
// while your terminal is buried.
//
// ONE TRAY, always. This never owns an icon of its own — it starts the SHARED process, and the tray's
// own flock makes a duplicate launch exit silently. So calling this when a tray is already running
// (from the LaunchAgent, or a previous `ptln llms`) is a harmless no-op.
//
// Entirely best-effort: no tray binary, no display, spawn fails — the session manager carries on
// exactly as before. A UI convenience must never be able to block the actual work.
func wakeTray() {
	bin := trayBinary()
	if bin == "" {
		return
	}
	cmd := exec.Command(bin)
	cmd.Stdout, cmd.Stderr = nil, nil
	cmd.Dir = filepath.Dir(bin)
	if err := cmd.Start(); err != nil {
		return
	}
	_ = cmd.Process.Release() // detach: the tray outlives this CLI invocation
	_ = os.Getpid()
}
