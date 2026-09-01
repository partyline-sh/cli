//go:build darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// reapStrayTrays SIGTERMs every ptln-tray already running, so a wake converges to EXACTLY one icon. The
// tray's own flock now holds correctly (cmd/ptln-tray: the lock file is kept for the process lifetime),
// so a simultaneous wake can't double up — but flock does NOTHING about trays that were ALREADY
// orphaned: a daemon/mux restart (every `ptln update`) leaves its old child tray running under PID 1,
// and pre-fix binaries never held the lock at all. Reaping before we spawn is the deterministic cleanup
// for both. Best-effort: no pgrep, nothing to kill, a kill that fails — we still spawn our one below.
func reapStrayTrays() {
	// EVERY partyline tray this user is running, not just our own bundle.
	//
	// Scoping the reap to our own bundle was right when the only other bundle was a different
	// ENVIRONMENT (staging vs production) that deserved its own icon. Bundles are now named per
	// ENDPOINT, and one machine routinely ends up with two: the daemon knows which instance it
	// belongs to, a bare `ptln` did not, and each half launched — and reaped — a different bundle.
	// The operator saw two phones in the menu bar for one partyline.
	//
	// The icon reports THIS MACHINE, so one machine means one icon. Anything else is an orphan,
	// whichever bundle it came from.
	out, err := exec.Command("pgrep", "-f", "/Contents/MacOS/ptln-tray").Output()
	if err != nil {
		return // no pgrep or nothing matched
	}
	self := os.Getpid()
	for _, f := range strings.Fields(string(out)) {
		pid, e := strconv.Atoi(f)
		if e != nil || pid == self {
			continue
		}
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
}

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
	// Clear any tray already up (an orphan from a restarted daemon/mux, or a stale pre-fix binary)
	// BEFORE spawning, so we converge to exactly one. Our fresh tray then holds the (now-working) flock.
	reapStrayTrays()
	cmd := exec.Command(bin)
	cmd.Stdout, cmd.Stderr = nil, nil
	cmd.Dir = filepath.Dir(bin)
	// Hand the tray OUR binary path. It shells out to ptln for every value it displays, and a bare
	// "ptln" from PATH resolves to whatever is installed system-wide — so a staging daemon's tray
	// would otherwise report the production CLI's version, account and web URLs.
	cmd.Env = append(os.Environ(), "PTLN_BIN="+selfExe())
	if err := cmd.Start(); err != nil {
		return
	}
	_ = cmd.Process.Release() // detach: the tray outlives this CLI invocation
	_ = os.Getpid()
}
