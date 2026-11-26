package main

import (
	"strings"
)

// launcherTmuxRun is the command boundary for joining and naming session panes.
var launcherTmuxRun = func(args ...string) error { return tmuxCmd(args...).Run() }
var launcherWindow = func(pane string) (string, error) {
	out, err := tmuxCmd("display-message", "-p", "-t", pane, "#{window_id}").Output()
	return strings.TrimSpace(string(out)), err
}
var launcherSave = tmuxSaveWorkspace

// launcherSplitSessions merges each session, joining existing panes into the first window.
func launcherSplitSessions(sessions []aiSession, meta map[string]sessMeta) error {
	first, window := "", ""
	var labels []string
	for _, s := range sessions {
		sp := launcherSpec(s, meta)
		pane, err := launcherMerge([]Spec{sp})
		if err != nil {
			return err
		}
		win, err := launcherWindow(pane)
		if err != nil {
			return err
		}
		if first == "" {
			first, window = pane, win
		} else if win != window {
			if err := launcherTmuxRun("join-pane", "-h", "-s", pane, "-t", first); err != nil {
				return err
			}
		}
		labels = append(labels, sp.Label)
	}
	if first == "" {
		return nil
	}
	if err := launcherTmuxRun("rename-window", "-t", window, tmuxPairWindowName(strings.Join(labels, " │ "))); err != nil {
		return err
	}
	launcherSave()
	return launcherFocus(first)
}
