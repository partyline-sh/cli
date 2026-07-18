package main

import "fmt"

// helpMain prints CLI usage. Shown by `ptln help`, `-h`, `--help`, and on an
// unknown command.
func helpMain() {
	fmt.Print(`partyline — multiplayer LLM dev

USAGE
  ptln                 open your session manager — browse, run, and switch your
                       claude/codex/gemini sessions in ONE terminal (the front door)
  ptln new <tool>      start a fresh AI session — claude | codex | gemini | antigravity
                       (--thread <id> attaches it to a context thread; --worktree <name>
                        isolates it in its own git worktree — combine them and parallel
                        agents get isolated files with ONE shared memory; --keep-going <N>
                        [--goal "..."] lets claude auto-continue up to N turns, or until it
                        prints the done token — a hard cap, never a runaway)
  ptln --resume        reopen every session you had open last time (same as: ptln llms --resume)
  ptln <command>       see COMMANDS below
  ptln start [flags]   host a shared shell (your $SHELL) and print a join link
  ptln start -- <prog> share a specific program instead of your shell

  (installed as "partyline"; "ptln" is the short alias — use either.)

COMMANDS
  (bare ptln)    your session manager — same as: ptln llms
  llms           AI session launcher: browse, run, and switch between your
                 claude/codex/gemini/llm sessions in ONE terminal (ctrl-\ ←/→ to
                 switch). llms <id>... opens those; llms --resume reopens last set.
  start          host a shared shell (your $SHELL) + print a join link
  login          authenticate this machine with partyline.sh (device flow)
  logout         remove the saved token from this machine
  whoami         show the logged-in account
  sessions       list your live sessions
  join [link]    join by link, or (no link) pick from sessions you're invited to
  party          interactive: start a new party, or join a running party
  party [link]   bring an AI agent into a Party (humans + agents channel)
  party up       bring up a whole room of agents from a partyline.yml file
  thread         shared context across people/machines/engines (Context Threads):
                 a private-by-default feed of decisions/constraints/contracts your
                 sessions read + write. Run: ptln thread help
  wt             session worktrees — list/remove the isolated dirs that
                 "ptln new <tool> --worktree <name>" creates (ptln wt help)
  work           run ONE task autonomously in a sandboxed worktree, then leave a
                 branch for you to review: ptln work "<task>" [--worktree <name>]
                 [--thread <id>] [--allow-bash] [--engine <e>] [--model <m>].
                 Runs the repo's registered engine by default (--engine
                 claude|codex|gemini overrides; codex requires --allow-bash,
                 antigravity can't run headless). Read/edit tools only by default;
                 never pushes — a human merges. The worker atom behind the conductor.
  shape          shape a plan: the agent interviews a rough idea into a well-specified,
                 SCORED backlog item (Epic/Feature/Task) in the planning tree — runs
                 the repo's registered engine locally (your auth, no server key). In a
                 registered repo it needs NO flags: the project implies the thread
                 (umbrella-aware, created on first use). ptln shape [--kind
                 epic|feature|task] [--thread <id>] [--quick]. /quick /deep switch
                 modes, /done, /quit. (alias: describe)
  crank          drive a BACKLOG one task at a time, each in its own worktree, sharing
                 one context thread: ptln crank --file backlog.txt [--thread <id>]
                 [--max N] [--max-tokens N] [--halt-on-fail K] [--engine <e>]
                 [--model <m>]. Halts on cap/tokens/failures; prepares N branches
                 for review — nothing is pushed or merged.
  join-mcp [link] add a Party to the LLM session you're already in (MCP), so it
                 can read + post — --print shows setup for non-Claude tools
  daemon         let the web launch a grounded agent on this machine, in the right
                 project dir, with one click — no per-launch commands (see DAEMON)
  org            manage your org's members, roles, and invites (alias: team).
                 owner: manage members + billing · admin: invite + create projects ·
                 member: use the system
  skill          org skill library — push/list/pull/install Agent Skills
                 (<name>/SKILL.md). Enabled skills are injected into every agent
                 run's workspace; "ptln skill install" adds them to your local
                 ~/.agents/skills + ~/.claude/skills too. Run: ptln skill help
  tray           macOS menu bar icon for the daemon: status, restart, stop, updates.
                 "ptln tray on" makes it start at login (macOS only)
  upgrade        update to the latest version (same as: ptln update)
  version        print the version (and whether a newer one is out)
  man            the full manual — every command, the ctrl-\ keys, files, and
                 example use cases (ptln man · ptln man --raw for the source)
  help           show this help

SESSION FLAGS
  --invite-only        require a partyline account to join (default; =false allows
                       anonymous view-only)
  --open               guests can type immediately (default: view-only until granted)
  --allow <users>      comma-separated GitHub usernames allowed to join
  --invite <emails>    comma-separated emails to invite when the session opens
  --announce           post the join code to your org's connected Slack channel
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

ORG ACCESS
  ptln org members                              list your org's members + roles
  ptln org invite <email> [--role admin|member] add someone (owner/admin only)
  ptln org access <handle|email> full|viewer    seat type: full = a driver seat
                (can be granted typing); viewer = watch-only. Picked up on next (re)join.

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
                Tab markers: ☎ = attached to a context thread (cyan wired · dim
                record-only) · ◉ = being shared live over the relay.
                In a live session, the ctrl-\ prefix: ←/→ or 1-9 switch ·
                [ scroll back · n new/run (start a session, run an autonomous
                task, or crank a backlog — no dropping to a terminal) ·
                c context (record/view shared context) ·
                m mcp (wire MCP servers into this session — your catalog at
                ~/.partyline/mcp.json, partyline's own servers pinned) ·
                w worktree (fork this session into a parallel git worktree —
                optionally carrying uncommitted work; inherits thread + MCPs) ·
                s share (broadcast this session view-only, or a new shared tab) ·
                o manager · x close · q quit.
                (The mouse stays native — select to copy, paste with the
                terminal; the agent gets the wheel if it uses one.)
  ptln llms ls [--all]   flat list (newest first); --all includes agent sessions
  ptln llms resume <id>  resume one session by id or unique prefix
  ptln llms --resume     reopen every session you had open last time, each with the
                same model + permission level (saved when you quit the launcher)
                Indexes claude, codex, gemini, and antigravity (agy) stores;
                the llm CLI if installed. Reads each tool's own files — nothing
                leaves the local launcher, no network.

DAEMON (remote launch — let the web start agents on this machine)
  ptln daemon enable               enrol this machine (needs ptln login); mints a
                                   device-scoped token, separate from your login
  ptln daemon add-project <label> [dir] [--preset spec|chat] [--engine <e>]
                                   register a project the web may launch into
                                   (the absolute path stays here; only the LABEL is
                                   mirrored to the web for an "Add agent" picker;
                                   --engine sets this project's default engine)
  ptln daemon run                  connect + listen; an interactive confirm console
                                   (approve <id> / deny <id> / kill <id> / list)
  ptln daemon install              run it always-on as a background service
                                   (launchd/systemd); restart re-execs in place
  ptln daemon disable              revoke the device token + remove it locally
                The control plane only ever sends a LABEL — never a path or command.
                A teammate clicks "Add agent" on the party page; YOU approve here, and
                only then does it spawn a grounded agent (read-only tools) in that
                project dir, joined to the party. Confirm-first; you can kill anytime.

DOCS  https://partyline.sh/docs
`)
}
