package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

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

// The dim/reset pair the menus paint with. These were declared beside the planning menu that is
// now deleted; they are styling, so they belong here with the rest of it.
const (
	cgBold = "\x1b[1m"
	cgDim  = "\x1b[38;5;245m"
	cgOff  = "\x1b[0m"
)

// Status colours for the launcher's inline reports.
const (
	cgKey  = "\x1b[36m"      // action keys
	cgWire = "\x1b[38;5;39m" // wired (matches the mux's tab marker)
	cgOK   = "\x1b[32m"
	cgBad  = "\x1b[31m"
)

// cgFrame clears the screen and draws a menu's title bar. cgItem prints one aligned action row.
// Both moved here from the planning menu that used to own them; they are now shared chrome for
// every full-screen menu the launcher opens.
func cgFrame(title string) {
	fmt.Print("\x1b[2J\x1b[H")
	fmt.Printf("\n  %s☎  %s%s\n", cgBold, title, cgOff)
	fmt.Println("  \x1b[38;5;240m────────────────────────────────────────────\x1b[0m")
}

func cgItem(key, label, note string) {
	if note == "" {
		fmt.Printf("    %s%s%s  %s\n", cgKey, key, cgOff, label)
		return
	}
	pad := 26 - len(label)
	if pad < 2 {
		pad = 2
	}
	fmt.Printf("    %s%s%s  %s%s%s%s%s\n", cgKey, key, cgOff, label, strings.Repeat(" ", pad), cgDim, note, cgOff)
}

// pause holds a full-screen menu open until the reader presses enter.
func pause(in *bufio.Reader) {
	fmt.Printf("\n  %s(enter to continue)%s ", cgDim, cgOff)
	_, _ = in.ReadString('\n')
}
