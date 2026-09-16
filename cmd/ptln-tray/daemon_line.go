//go:build darwin && tray

package main

// daemon_line.go — the menu's connection light.
//
// Split out and kept pure so the wording and the colour rules are testable without a menu bar.
//
// WHY EMOJI AND NOT "●". A menu item's text is drawn in the system label colour, so the old "●"
// was the same grey whether the daemon was connected, stopped or shouting into a void. A status
// light that cannot change colour is decoration. Emoji carry their own colour through NSMenu, which
// is how Docker's green dot works too.

const (
	dotConnected = "🟢"
	dotDegraded  = "🟠" // the process is alive; the instance is not answering it
	dotStopped   = "⚪"
)

// daemonLine renders the status for a daemon the OS reports as running.
//
// The distinction it exists to make: "running" and "connected" are different questions with
// different fixes. Restarting a daemon that is running fine but cannot reach a moved instance wastes
// your time; the line now tells you which one you have.
func daemonLine(d daemon) string {
	// An older `ptln` sends no link object. Claiming green from `Active` alone is exactly the lie
	// being removed, so an unknown link says so plainly rather than guessing in either direction.
	if d.Link == nil {
		return dotStopped + " running (connection unknown — update ptln)"
	}
	if d.Link.Connected {
		return dotConnected + " " + d.Link.Detail
	}
	return dotDegraded + " " + d.Link.Detail
}

// daemonTooltip is the hover text: the same truth, prefixed so it reads on its own in a tooltip
// that has no menu around it for context.
func daemonTooltip(d daemon) string {
	switch {
	case !d.Enabled:
		return "not enrolled — run `ptln login`"
	case !d.Installed:
		return "not installed as a service — run `ptln daemon install`"
	case !d.Active:
		return "daemon stopped"
	case d.Link == nil:
		return "daemon running; connection state unknown (update ptln)"
	default:
		return "daemon " + d.Link.Detail
	}
}
