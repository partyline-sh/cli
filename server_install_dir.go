package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// server_install_dir.go — where the stack goes, and why it is not /opt by default any more.
//
// WHAT HAPPENED ON A REAL BOX. `ptln server install` defaulted to /opt/partyline, hit
//
//	/opt is not writable by this user — re-run with sudo, or pick a --dir you own
//
// and the operator did exactly what it said:
//
//	$ sudo ptln server install
//	sudo: ptln: command not found
//
// The CLI installs to ~/.local/bin, which is not on root's PATH, so the advice was a dead end. The
// installer had sent them to a directory they could not write and then to a command that does not
// resolve.
//
// /opt is the FHS answer for a system-wide service and it is a fine choice for someone who wants it.
// It is a poor DEFAULT, because it converts a first install into a permissions problem before
// anything has been decided. The stack runs under docker either way; nothing about it needs root
// beyond writing the directory.
//
// So: default to a place the operator already owns, and offer /opt only when it is actually usable.

// defaultInstallDirFor picks the install directory when the operator did not name one.
//
// /opt/partyline if this user can create it — the familiar location, and what our own boxes use.
// Otherwise ~/partyline, which needs no privilege and no decision.
func defaultInstallDirFor(home string, parentWritable func(string) bool) string {
	if parentWritable("/opt") {
		return defaultInstallDir
	}
	if home != "" {
		return filepath.Join(home, "partyline")
	}
	return defaultInstallDir
}

// liveDefaultInstallDir is defaultInstallDirFor against the real filesystem.
func liveDefaultInstallDir() string {
	home, _ := os.UserHomeDir()
	return defaultInstallDirFor(home, func(dir string) bool { return unixWritable(dir) == nil })
}

// sudoHint names the command that would actually work, which is not the one the operator typed.
//
// `sudo ptln` fails when ptln lives in ~/.local/bin, and that is where install.sh puts it whenever
// /usr/local/bin is not writable — i.e. exactly the situation where someone reaches for sudo. The
// absolute path is the difference between advice and a dead end.
func sudoHint(args string) string {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		exe = "ptln"
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil && resolved != "" {
		exe = resolved
	}
	return fmt.Sprintf("sudo %s %s", exe, args)
}
