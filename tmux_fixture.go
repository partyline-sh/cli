package main

import (
	"os"
	"strings"
)

const legacyLoopsKey = "__loops__"
const legacyLauncherKey = "__launcher__"

// isFixtureKey excludes windows left by older binaries from session lists and restores.
func isFixtureKey(key string) bool { return key == legacyLoopsKey || key == legacyLauncherKey }

// retireFixtureWindows closes obsolete fixtures after a usable window exists.
func retireFixtureWindows() {
	out, _ := tmuxCmd("list-windows", "-t", tmuxSessionName, "-F", "#{window_id}\t#{@ptln_key}").Output()
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		f := strings.SplitN(line, "\t", 2)
		if len(f) == 2 && isFixtureKey(f[1]) {
			_ = tmuxCmd("kill-window", "-t", f[0]).Run()
		}
	}
}

// tmuxShellWindow starts a server if needed, otherwise appends a shell window.
func tmuxShellWindow() (string, error) {
	cwd, _ := os.Getwd()
	args := []string{"new-window", "-d", "-P", "-F", "#{window_id}", "-t", tmuxSessionName + ":", "-c", cwd}
	if tmuxCmd("has-session", "-t", tmuxSessionName).Run() != nil {
		args = []string{"new-session", "-d", "-P", "-F", "#{window_id}", "-s", tmuxSessionName, "-c", cwd}
	}
	out, err := tmuxCmd(args...).Output()
	return strings.TrimSpace(string(out)), err
}
