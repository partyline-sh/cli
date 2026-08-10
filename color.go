package main

import (
	"os"

	"golang.org/x/term"
)

// Colour discipline (audit item 13). The ad-hoc ANSI sites outside the TUI used to emit escapes
// unconditionally, so `ptln crank | tee run.log` wrote \x1b[32m into the log. One gate, asked at
// write time rather than cached at init, so a test (or a piped re-exec) sees the truth.
//
// The TUI paths (llms_theme.go, internal/brand, internal/ptymux) deliberately do NOT go through
// this: they only ever draw to a terminal they already own.

// stdoutIsTTY reports whether stdout is a terminal. It's the redraw-safety half of colorOK, kept
// separate because a \r progress line is about legibility, not colour — NO_COLOR must not silence it.
func stdoutIsTTY() bool { return term.IsTerminal(int(os.Stdout.Fd())) }

// colorOK reports whether it's safe to put ANSI escapes on stdout: a real terminal, and the user
// hasn't asked for plain output (no-color.org).
func colorOK() bool { return stdoutIsTTY() && os.Getenv("NO_COLOR") == "" }

// sgr wraps s in an ANSI code when colour is allowed and returns s untouched when it isn't.
func sgr(code, s string) string {
	if !colorOK() {
		return s
	}
	return code + s + cgOff
}

// dim is the one-liner used by prompts and hints (the most-duplicated escape in the codebase).
func dim(s string) string { return sgr(cgDim, s) }
