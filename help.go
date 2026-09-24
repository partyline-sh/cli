package main

// helpMain prints `ptln help` — the index, not the manual.
//
// It used to be a 2,600-word hand-written blob that restated each command's flags, the mux keys,
// the session-manager keys, the daemon's subcommands and the MCP tool list. Every one of those
// facts already exists in a declaration somewhere, so the blob's only real function was to
// disagree with them: it advertised `ptln trigger` (deleted), `ptln project doc|env|tools` (never
// existed) and the MCP tools propose_work_item, plan_file_tree and create_project (none of them
// real), while never mentioning memory, bus or feed.
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
	fmt.Fprint(w, `partyline — multiplayer LLM dev

USAGE
  ptln                   the session manager: browse, run and switch this machine's AI sessions
  ptln new <engine>      start a fresh session — claude · codex · gemini · opencode · goose · antigravity
  ptln --resume          reopen every session you had open last time
  ptln start             host a shared shell and print a join link
  ptln <command> --help  that command's usage, subcommands and flags

  Installed as "partyline"; "ptln" is the short alias. Use either.

  There is no hosted partyline. Run an instance with "ptln server install", or point this
  machine at one with "ptln login <url>".

COMMANDS
`)
	clispec.WriteIndex(w, "  ")
	fmt.Fprint(w, `
KEYS
  ctrl-\        open the menu in a session. Every command is on it, with its key.
                ←/→ or 1-9 switch sessions. Press ctrl-\ twice to send a literal one.
                It works inside full-screen apps like vim and claude.
  ?             in the session manager: every key it has.
  /phelp        in a shared session, typed at a shell prompt.

MCP TOOLS
  Wired into claude and codex automatically. Server: partyline-context-threads.

`)
	writeCgToolIndex(w, "  ")
	fmt.Fprint(w, `
  read_run and read_run_log need "ptln login <url>" and accept only a UUID; a run you
  cannot see is reported identically to one that does not exist.

DOCS
  ptln man                        the full manual
  https://partyline.sh/docs
  https://partyline.sh/llms-full.txt   the whole product as one plain-text file, for an
                                       AI assistant with nothing installed. No account.
`)
}
