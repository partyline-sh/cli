package main

import (
	"os"
	"path/filepath"
)

func themePath() string { return filepath.Join(stateDir(), "llms-theme") }

// cycleTheme moves to the next palette, persists it, redraws tmux in it, and returns its name and
// variant (for a footer flash).
func cycleTheme() (string, string) {
	next := nextPalette(savedThemeName())
	_ = os.MkdirAll(filepath.Dir(themePath()), 0o700)
	_ = os.WriteFile(themePath(), []byte(next+"\n"), 0o600)
	applyChosenTheme()
	t := chosenTheme()
	return t.Palette, map[bool]string{true: "dark", false: "light"}[t.Dark]
}
