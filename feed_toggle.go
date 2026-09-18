package main

// Showing and hiding the feed. tmux owns the grid, so this is a split and a kill — the agent's
// pane simply resizes around it, and nothing has to know the feed exists.

import (
	"strings"
)

// feedPaneWidth is a reading column: wide enough for a name and a wrapped sentence, narrow
// enough that the agent beside it keeps a usable terminal.
const feedPaneWidth = "42"

// feedPaneOf returns the id of the feed pane in the current window, or "".
//
// It looks for the MARKER, not for a command line or a pane title: a pane's command changes as
// the process inside it does, and matching on it would leave a feed that cannot be closed.
func feedPaneOf(window string) string {
	out, err := tmuxCmd("list-panes", "-t", window, "-F", "#{pane_id}\t#{@ptln_feed}").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.SplitN(line, "\t", 2)
		if len(f) == 2 && strings.TrimSpace(f[1]) == "1" {
			return f[0]
		}
	}
	return ""
}

// tmuxFeedToggle is `ptln tmux --feed`: show the feed beside this pane, or hide it.
func tmuxFeedToggle() {
	window := currentTmuxWindow()
	if window == "" {
		return
	}
	if pane := feedPaneOf(window); pane != "" {
		_ = tmuxCmd("kill-pane", "-t", pane).Run()
		return
	}
	// -d: the split opens WITHOUT taking focus. The feed is something you glance at; stealing
	// the cursor from the agent would make every toggle cost a keystroke to undo.
	out, err := tmuxCmd("split-window", "-h", "-d", "-l", feedPaneWidth, "-t", window,
		"-P", "-F", "#{pane_id}", selfExe(), "feed").Output()
	if err != nil {
		_ = tmuxCmd("display-message", "could not open the feed").Run()
		return
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return
	}
	_ = tmuxCmd("set-option", "-p", "-t", id, "@ptln_feed", "1").Run()
	// A feed is not a place to type. Turning off remain-on-exit and marking it means a stray
	// click lands somewhere harmless rather than in a pane that looks interactive and is not.
	_ = tmuxCmd("select-pane", "-t", id, "-T", "activity").Run()
}

// currentTmuxWindow is the window the caller is in, or "" outside tmux.
func currentTmuxWindow() string {
	out, err := tmuxCmd("display-message", "-p", "#{window_id}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
