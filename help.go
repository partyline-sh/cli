package main

import "fmt"

// helpMain prints CLI usage. Shown by `ptln help`, `-h`, `--help`, and on an
// unknown command.
func helpMain() {
	fmt.Print(`partyline — your terminal, multiplayer

USAGE
  ptln                 start a shared shell (your $SHELL) and print a join link
  ptln start           same as bare ptln — start a shared shell
  ptln [flags]         start a session with options (see below)
  ptln -- <program>    share a specific program instead of your shell
  ptln <command>

  (installed as "partyline"; "ptln" is the short alias — use either.)

COMMANDS
  start          start a shared shell (same as bare ptln)
  login          authenticate this machine with partyline.sh (device flow)
  logout         remove the saved token from this machine
  whoami         show the logged-in account
  sessions       list your live sessions
  join [link]    join by link, or (no link) pick from sessions you're invited to
  party [link]   bring an AI agent into a Party (humans + agents channel)
  party up       bring up a whole room of agents from a partyline.yml file
  llms           AI session launcher: browse, run, and switch between your
                 claude/codex/gemini/llm sessions in ONE terminal (ctrl-\ w to
                 switch). llms <id>... opens those; llms --resume reopens last set.
  team           manage teams + invites (alias: org)
  upgrade        update to the latest version (brew, or the installer)
  version        print the version (and whether a newer one is out)
  help           show this help

SESSION FLAGS
  --invite-only        require a partyline account to join (default; =false allows
                       anonymous view-only)
  --open               guests can type immediately (default: view-only until granted)
  --allow <users>      comma-separated GitHub usernames allowed to join
  --invite <emails>    comma-separated emails to invite when the session opens
  --team <slug>        host for a team (default: your personal space)
  --announce           post the join code to the team's connected Slack channel
  --insecure-any-key   accept any ssh key without identity checks (trusted LAN only)
  --port <n>           ssh port joiners connect to (default 2222)
  --relay <host:port>  relay to use (default pppp.sh:22; '' to disable)

IN A SESSION
  ctrl-\ then:  ? open the command menu · w who's on the line ·
                g grant/open guest typing (host) · r request control (guest) ·
                h session HUD · l lock joiners (host) · d leave · q end (host)
                (the ctrl-\ prefix works even inside full-screen apps like vim/claude;
                 press ctrl-\ twice to send a literal ctrl-\ through to the app)
  typed:        /pwho · /phud [on|off] · /pgrant [name] · /pinvite <email> · /plock · /pexit · /phelp
                (typed /p commands only work at a shell prompt, not inside full-screen apps)
  screen size:  the shared terminal is sized to the SMALLEST connected screen, so
                everyone sees the same thing without wrapping/redraw artifacts. If a
                joiner has a small window, the whole session shrinks to fit — resize
                your window and the session resizes live (largest-possible, clamped).

TEAM ACCESS
  ptln team access <handle|email> full|viewer   promote/demote a teammate
                full = a driver seat (can be granted typing); viewer = watch-only.
                They pick it up on their next (re)join.

PARTY (humans + agents)
  ptln party '<link>' --name <name> --role "<what it does>" [--cmd "<agent>"]
                Bring an agent into a Party — a coordination channel for people and
                AI agents, started with /partyline party in Slack or from the web.
                The runner watches the channel and wakes your agent for one turn
                whenever it's addressed: @name (one) · @all (everyone) · @any (free).
                --cmd defaults to "claude -p"; the prompt is fed on stdin and the
                captured reply is posted back. No login or MCP setup needed — the
                join link carries a party-scoped token.
                Each party has a shared working doc; agents propose edits to it
                (humans approve on the web). See: ptln party --help.

AI SESSIONS (local)
  ptln llms              browse your AI CLI sessions in an interactive menu.
                Keys: arrows move · / search · s cycle sort (last-used/oldest/
                project) · R refresh the list · p pin · x archive · a reveal ·
                ⏎ resume here (pick a permission mode — default / accept-edits /
                plan / bypass⚠ / custom — for the tool) · o open in a NEW tab
                (tmux · iTerm2 · WezTerm · kitty · GNOME Terminal · Konsole).
                Master/detail: list left; right pane shows location + git
                branch, tokens, span, uncommitted count, model, and a preview.
  ptln llms ls [--all]   flat list (newest first); --all includes agent sessions
  ptln llms resume <id>  resume one session by id or unique prefix
  ptln llms --resume     reopen every session you had open last time, each with the
                same model + permission level (saved when you quit the launcher)
                Indexes claude, codex, and gemini session stores on this machine;
                the llm CLI if installed. Reads each tool's own files — nothing
                leaves the machine, no daemon, no network.

DOCS  https://partyline.sh/docs
`)
}
