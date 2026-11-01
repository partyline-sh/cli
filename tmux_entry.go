package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// restorableSpec excludes permanent fixtures, including workspaces from older binaries.
func restorableSpec(sp Spec) bool {
	return !isFixtureKey(sp.Key)
}

// tmuxBarePrepare starts the server and keeps its current selection when it already exists.
var tmuxBarePrepare = func() error {
	refreshTmuxBackground()
	if err := writeTmuxConf(); err != nil {
		return err
	}
	if tmuxCmd("has-session", "-t", tmuxSessionName).Run() != nil {
		specs := dropHeldForks(loadWorkspace())
		_, err := tmuxMerge(specs)
		if errors.Is(err, errNothingToOpen) {
			_, err = tmuxShellWindow()
		}
		if err != nil {
			return err
		}
	}
	// A server consisting only of obsolete fixtures needs a shell before retiring them.
	if len((tmuxTargets{}).LiveSessions()) == 0 {
		out, _ := tmuxCmd("list-windows", "-t", tmuxSessionName, "-F", "#{@ptln_key}").Output()
		onlyFixtures := true
		for _, key := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
			if !isFixtureKey(key) {
				onlyFixtures = false
			}
		}
		if onlyFixtures {
			if _, err := tmuxShellWindow(); err != nil {
				return err
			}
		}
	}
	retireFixtureWindows()
	_ = tmuxCmd("source-file", filepath.Join(stateDir(), "tmux.conf")).Run()
	loopRefreshIndicators()
	ensureWatcher()
	return nil
}

// tmuxBareAttach transfers the terminal and schedules the launcher on the attached client.
var tmuxBareAttach = func() error {
	home := shQuote(selfExe()) + " tmux --home"
	if launcherSearch {
		home += " --search"
	}
	cmd := tmuxCmd("attach-session", "-t", tmuxSessionName, ";", "run-shell", "-b", home)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// tmuxBareWake wakes the shared tray on the bare entry path.
var tmuxBareWake = wakeTray

// runTmuxBare clears the ticker-painted splash before tmux takes the terminal.
func runTmuxBare() error {
	if err := requireTmux(); err != nil {
		return err
	}
	boot := newBootDisplay()
	tmuxBareWake()
	boot.Step("Starting tmux")
	err := tmuxBarePrepare()
	boot.Done()
	if err != nil {
		return err
	}
	return tmuxBareAttach()
}

// refreshTmuxBackground asks the outer terminal before generating the tmux theme.
func refreshTmuxBackground() {
	if insidePtlnTmux() {
		return
	}
	if bg := loadBackground(); bg.Choice == "auto" && bg.Detected != "none" {
		if d := detectBackground(bg.Detected); d != bg.Detected {
			bg.Detected = d
			_ = saveBackground(bg)
		}
	}
}
