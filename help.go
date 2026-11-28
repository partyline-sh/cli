package main

// helpMain prints `ptln help` — the index, not the manual.
//
// It used to be a 2,600-word hand-written blob that restated each command's flags, the mux keys,
// the session-manager keys, the daemon's subcommands and the MCP tool list. Every one of those
// facts already exists in a declaration somewhere, so the blob's only real function was to
// disagree with them: it advertised deleted and never-built commands while never mentioning the
// ones that existed. The prose that remains below is covered by TestHelpNamesNoPhantomCommand.
//
// So it renders the declarations instead, and points at the places that document themselves —
// `ptln <command> --help`, the ctrl-\ menu, and `?` in the session manager.

import (
	"fmt"
	"os"

	"partyline.sh/partyline/internal/clispec"
)

func helpMain() {
	w := os.Stdout
	fmt.Fprint(w, `partyline — a session manager for AI coding agents, repository memory, and a chat between sessions

USAGE
  ptln                   open the session manager
  ptln new <engine>      start a session: claude, codex, gemini, opencode, goose or antigravity
  ptln --resume          reopen the sessions you had open last time
  ptln memory build      set up repository memory in this repo
  ptln chat              open the chat room
  ptln <command> --help  usage, subcommands and flags for one command

  The binary is "partyline"; "ptln" is a symlink to it.
  There is no server and no account. partyline.sh serves the documentation and the installer.

COMMANDS
`)
	clispec.WriteIndex(w, "  ")
	fmt.Fprint(w, `
KEYS
  ctrl-\        in a session: open the Sessions / Commands menu.
                tab/shift+tab switch tabs; arrows preview Sessions or move Commands.
                Enter keeps a session or runs a command; hotkeys work in either tab.
                / filters Sessions by label or engine (also from Commands); backspace edits.
                Enter ends filtering; Enter again keeps. Esc clears, then closes.
                While editing, text replaces hotkeys; afterwards even hidden hotkeys act.
                ctrl-\ closes it.
  M-1 … M-9     in a session: Alt-1 through Alt-9 select a window directly.
  PgUp / PgDn   in the launcher: page the session list.
  j / k         in the launcher: move down / up.
  +             in the menu: bring the highlighted session in beside this one (also |).
  x / Q         in the menu: press twice to close a pane / quit everything.
  Tab/shift-Tab in Loops: switch Board / Contractors; ↑↓/jk select, ⏎ full output, esc back.
                Contractors shows live output beside the list at 100+ columns; q closes.
  k             in the menu: Loops on its Contractors tab, first contractor selected.
                Click a contractor chip above the tabs to open Loops with it selected.
  r             in the menu: on Sessions, rename the highlighted session; on Commands,
                the relay settings.
  ,             in the menu: loop settings: who builds and reviews loops, failover, the quality
                standard, and whether loops decide for themselves (the default) or ask you.
  d             in Loops: press twice to stop a loop and delete it for good.
  ?             in the session manager: list every key.

DOCS
  ptln man                             the manual
  https://partyline.sh/docs            the documentation
  https://partyline.sh/llms-full.txt   the documentation as one plain-text file, for an AI assistant
`)
}
