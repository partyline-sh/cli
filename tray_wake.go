//go:build darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
)

// wakeTray starts the menu bar companion if it's installed and not already showing.
//
// Called when the session manager opens AND when the daemon starts — the two moments this machine
// begins doing work worth watching. AUTOMATIC: nobody should have to remember a command to get the
// icon, because an icon you must opt into is one most people never see.
//
// `ptln tray off` is honored here, so the opt-out is real rather than lasting until the next launch.
//
// ONE TRAY, always. This never owns an icon of its own — it starts the SHARED process, and the tray's
// own flock makes a duplicate launch exit silently. So calling this when a tray is already running
// (from the LaunchAgent, or a previous `ptln llms`) is a harmless no-op.
//
// Entirely best-effort: no tray binary, no display, spawn fails — the session manager carries on
// exactly as before. A UI convenience must never be able to block the actual work.
func wakeTray() {
	if !trayAutoStartAllowed() {
		return // the operator ran `ptln tray off` — "off" has to mean off
	}
	// Launch the BUNDLED executable, not the bare binary: running outside Partyline.app costs the
	// process its identity, and notifications fall back to being attributed to something else.
	bin := ensureTrayApp()
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
