package main

import (
	"bufio"
	"fmt"
	"os"

	"golang.org/x/term"

	uitheme "partyline.sh/partyline/internal/theme"
)

// Colour discipline (audit item 13). The ad-hoc ANSI sites outside the TUI used to emit escapes
// unconditionally, so `ptln crank | tee run.log` wrote colour escapes into the log. One gate, asked at
// write time rather than cached at init, so a test (or a piped re-exec) sees the truth.
//
// The TUI paths (Bubble Tea views and internal/brand) deliberately do NOT go through
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

// The dim/reset pair the menus paint with. These were declared beside the planning menu that is
// now deleted; they are styling, so they belong here with the rest of it.
const (
	cgBold = "\x1b[1m"
	cgDim  = uitheme.CLIMuted
	cgOff  = "\x1b[0m"
)

// Status colours for the launcher's inline reports.
const (
	cgKey  = uitheme.CLIKey  // action keys
	cgWire = uitheme.CLIWire // wired (matches the mux's tab marker)
	cgOK   = uitheme.CLIOK
	cgBad  = uitheme.CLIBad
)

// pause holds a full-screen menu open until the reader presses enter.
func pause(in *bufio.Reader) {
	fmt.Printf("\n  %s(enter to continue)%s ", cgDim, cgOff)
	_, _ = in.ReadString('\n')
}
