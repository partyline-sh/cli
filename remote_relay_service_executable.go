package main

import (
	"os"
	"os/exec"
	"path/filepath"
)

// Prefer the installed entry point (e.g. Homebrew's stable symlink) only when it
// points to this running binary. An unrelated ptln on PATH must never replace a
// development binary or a different installation in the service definition.
func relayServiceExecutable(exe string) string {
	candidate, err := exec.LookPath("ptln")
	if err != nil {
		return exe
	}
	absolute, err := filepath.Abs(candidate)
	if err != nil {
		return exe
	}
	running, err := os.Stat(exe)
	if err != nil {
		return exe
	}
	installed, err := os.Stat(absolute)
	if err != nil || !os.SameFile(running, installed) {
		return exe
	}
	return absolute
}
