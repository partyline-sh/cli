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
	fmt.Fprint(w, `partyline — every AI coding session in one place, and a repo that remembers

USAGE
  ptln                   the session manager: browse, run and switch this machine's AI sessions
  ptln new <engine>      start a fresh session — claude · codex · gemini · opencode · goose · antigravity
  ptln --resume          reopen every session you had open last time
  ptln memory <verb>     the facts this repo carries in memory/ — add, edit, build, hooks, map
  ptln <command> --help  that command's usage, subcommands and flags

  Installed as "partyline"; "ptln" is the short alias. Use either.

  No server, no account. partyline.sh serves the docs and the installer, nothing else.

COMMANDS
`)
	clispec.WriteIndex(w, "  ")
	fmt.Fprint(w, `
KEYS
  ctrl-\        open the menu in a session. Every command is on it, with its key.
                ←/→ or 1-9 switch sessions. Press ctrl-\ twice to send a literal one.
                It works inside full-screen apps like vim and claude.
  ?             in the session manager: every key it has.

MEMORY
  Facts live in <repo>/memory/, one file each, and are rendered into AGENTS.md, CLAUDE.md
  and GEMINI.md so every engine reads them at session start. partyline never commits your
  repo. "ptln memory hooks" says which engines also refresh them mid-session.

DOCS
  ptln man                        the full manual
  https://partyline.sh/docs
  https://partyline.sh/llms-full.txt   the whole product as one plain-text file, for an
                                       AI assistant with nothing installed. No account.
`)
}
