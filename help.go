package main

import "fmt"

// helpMain prints CLI usage. Shown by `ptln help`, `-h`, `--help`, and on an
// unknown command.
func helpMain() {
	fmt.Print(`partyline — multiplayer LLM dev

USAGE
  ptln                 open the session manager: browse, run, and switch your
                       local AI sessions in one terminal
  ptln new <tool>      start a fresh session — claude | codex | gemini |
                       opencode | goose | antigravity
  ptln --resume        reopen every session you had open last time
  ptln start [flags]   host a shared shell (your $SHELL) and print a join link
  ptln start -- <prog> share a specific program instead of your shell
  ptln <command>       see COMMANDS below

  (installed as "partyline"; "ptln" is the short alias — use either.)

  There is no hosted partyline. Run an instance with "ptln server install",
  or point this machine at one with "ptln login <url>".

COMMANDS
  llms           the session manager (same as bare ptln). Browse, run, and
                 switch claude/codex/gemini/opencode/goose/antigravity/llm
                 sessions in one terminal; ctrl-\ ←/→ switches.
                 llms <id>... opens those; llms --resume reopens the last set
  new            start a fresh AI session. --thread <id> attaches a context
                 thread; --worktree <name> isolates it in its own git
                 worktree; both together give parallel agents isolated files
                 and one shared memory. --keep-going <N> [--goal "..."] lets
                 claude auto-continue up to N turns, or until it prints the
                 done token — a hard cap
  start          host a shared shell and print a join link (see SESSION FLAGS)
  join [link]    join by link, or with no link pick from your invitations
  sessions       list your live sessions
  summon         bring a teammate or agent into the session you are hosting

  login <url>    authenticate this machine against an instance (device flow)
                 and pin its identity key, printing the fingerprint. With no
                 url it refuses and names the two commands that work.
                 --accept-new-key re-pins after a key change
  logout         remove the saved token from this machine
  whoami         show the logged-in account
  setup          connect this machine end to end: account → always-on worker →
                 engine → code locations → projects → PRs → agent memory.
                 Runs after every login; asks only about what is missing.
                 --redo re-asks everything (Enter keeps the current answer)
  doctor         check whether this repo can plan and run work, and name the
                 fix for anything that cannot

  thread         shared context across people, machines and engines: a
                 private-by-default feed of decisions, constraints and
                 contracts your sessions read and write. ptln thread help
  scribe         distil this directory's newest AI session into durable facts
                 on its context thread. Runs locally — the raw conversation
                 never leaves this machine, only the facts.
                 ptln scribe [--thread <id>] [--session <id>] [--min-turns <n>]

  plan           the Planning agent: interviews a rough idea into a scored
                 backlog item (Epic/Feature/Task) using the repo's registered
                 engine, on your own auth. In a registered repo it needs no
                 flags. ptln plan [--kind epic|feature|task] [--thread <id>]
                 [--quick]; /quick /deep switch modes, /done, /quit.
                 (aliases: shape, describe)
  work           run ONE task autonomously in a sandboxed worktree, then leave
                 a branch to review: ptln work "<task>" [--worktree <name>]
                 [--thread <id>] [--allow-bash] [--engine <e>] [--model <m>].
                 Read/edit tools only by default; never pushes — a human
                 merges. codex and goose require --allow-bash; antigravity
                 cannot run headless
  crank          drive a BACKLOG one task at a time, each in its own worktree,
                 sharing one context thread: ptln crank --file backlog.txt
                 [--thread <id>] [--max N] [--max-tokens N] [--halt-on-fail K]
                 [--engine <e>] [--model <m>] [--literal] [--max-repairs N].
                 Halts on cap, tokens or failures; prepares N branches for
                 review — nothing is pushed. Blank lines and #-comments are
                 skipped unless --literal
  review         mark up a worked example and have the model rebuild it from
                 your marks: ptln review <work-item-id> [--version <n>]
                 [--file <path>] [--serve] [--port <n>]
  keepgoing      manage auto-continue (status|off) — arm it at launch with
                 "ptln new claude --keep-going N" or mid-session with ctrl-\ g
  wt             session worktrees — list/remove the isolated dirs that
                 "ptln new --worktree" and crank create. "ptln wt prune"
                 clears finished crank worktrees (dry run; --yes applies,
                 never a branch, never one with uncommitted work)
  models         what the engines installed here can actually run

  party          humans and agents in one channel. Bare: start a new party or
                 join a running one. party <link> brings an agent in;
                 party up brings up a room from partyline.yml; party context
                 switches a live party's persona or project
  join-mcp       add a party to the LLM session you are already in (MCP), so
                 it can read and post — --print shows setup for non-Claude
                 tools. Posts go out under YOUR login if you have one;
                 without one they read as an agent.
                 "ptln join-mcp status" lists every party MCP registration on
                 THIS MACHINE (they accumulate, one per party, across configs)
                 and says whether each party is live, ended, or uncheckable.
                 Read-only, --json, exits 1 if any has ended
  peer           peer consults — read-only Q&A between your agents and a
                 teammate's. ptln peer approve|decline <id> decides a question
                 queued for THIS machine; ptln peer cancel <id> withdraws one
                 YOU asked. Agents ask with the ask_peer MCP tool; ctrl-\ p is
                 the inbox. See PEER CONSULTS below
  daemon         let the web launch a grounded agent on this machine, in the
                 right project dir, with one click (see DAEMON)

  board          the work board, right here — five columns (backlog · building ·
                 blocked · review · accepted), the same board the web renders.
                 ⏎ does a card's primary move, a lists every move, d opens the
                 detail with its live run log, s attaches the run's session,
                 n files new work. Piped, it prints the board once.
                 S switches to another board entirely — an Odoo project, a Jira
                 board — from any MCP server that declares one (see
                 docs/plans/board-providers.md); p picks which project it shows
                 and i imports one of its cards into partyline as planned work
  project        projects — ls | new | show <label> (machines, runs, threads)
                 setup [<label>]  set THIS repo up as a project: creates it,
                 pins its thread in .partyline.json, and REGISTERS this
                 directory so your team's agents may build here unattended
                 (undo: daemon remove-project <label>)
                 doc <label>  the globals injected into every run on it
                 env <label>  the deploy chain (staging=develop,prod=main)
                 tools <label> [--role planning|build --allow-shell "gh *"
                 --allow-mcp <name>]  what launched agents may use
  org            your org's members, roles and invites (alias: team).
                 owner: members + billing · admin: invite + create projects ·
                 member: use the system
  skill          org skill library — push/list/pull/install Agent Skills
                 (<name>/SKILL.md). Enabled skills are injected into every
                 agent run's workspace; "ptln skill install" adds them to
                 ~/.agents/skills and ~/.claude/skills. ptln skill help
  template       reusable agent personas a trigger can run: ls|show|create|rm
  trigger        inbound entry points — an address other software POSTs to,
                 which starts work here. ptln trigger help
                 on a CLOCK: ptln trigger set <slug> --cron '0 3 * * *'
                 what they have done: trigger activity · trigger log <slug>
  webhook        outbound — where your team's events go: ls | add | rm
  key            API keys for CI and scripts: ls | create | revoke
  chat           reach your projects from Telegram or Discord: link | unlink
  me             your profile — name, handle, timezone: ptln me [set]
  notify         what partyline tells you about, and where: on | off | quiet
  settings       master index: every setting's current state, and where to
                 change it

  server         self-host a partyline BOX (not your laptop).
                 "ptln server install" brings one up from this binary alone:
                 preflight, plan, apply, verify, report. Idempotent;
                 --dry-run prints the plan and writes nothing.
                 "ptln server doctor" reports each feature configured or NOT
                 configured, naming the variables a not-configured one is
                 missing. Names and set/unset only — a value is never
                 printed, so the output is safe to paste into an issue. It
                 also prints this instance's identity fingerprint.
                 "ptln server bootstrap" checks a fresh box and PRINTS the
                 exact ordered install commands, writing nothing. Exit 1 if a
                 prerequisite is missing. --json for the machine-readable form
  state          machine-readable JSON snapshot: account, daemon, live
                 sessions, waiting-on-you, rate limits — what the tray reads
  tray           macOS menu bar companion — live sessions, waiting-on-you
                 notifications, daemon control, rate-limit display. Starts
                 with the session manager or daemon; "ptln tray on" adds it at
                 login, "ptln tray off" opts out, bare = status
  welcome        the front-door welcome screen — resume / new / share / plan /
                 find (bare ptln shows it when you have no sessions yet)
  upgrade        update to the latest version (same as: ptln update)
  version        print the version (and whether a newer one is out)
  man            the full manual (ptln man · ptln man --raw for the source)
  help           show this help

SESSION FLAGS
  --invite-only        require a partyline account to join (default; =false
                       allows anonymous view-only)
  --open               guests can type immediately (default: view-only until
                       granted)
  --allow <users>      comma-separated GitHub usernames allowed to join
  --invite <targets>   comma-separated emails and/or teammate @handles to
                       invite when the session opens. Prints who was actually
                       reached; a target that resolved to nobody, and one that
                       resolved but wasn't delivered, are each named — neither
                       is ever counted as sent
  --announce           with --team <slug>: post the join code to that team's
                       connected Slack channel
  --insecure-any-key   accept any ssh key without identity checks (trusted LAN
                       only)
  --port <n>           ssh port joiners connect to (default 2222)
  --relay <host:port>  relay to use (default: the relay your instance
                       registers; '' disables it; PARTYLINE_RELAY sets it for
                       the shell)

IN A SESSION
  ctrl-\ then:  ? command menu · w who's on the line · g grant/open guest
                typing (host) · r request control (guest) · h session HUD ·
                l lock joiners (host) · d leave · q end (host)
  in the mux:   ←/→ or 1-9 switch · [ scroll back · n new/run · c context ·
                m mcp · w worktree · p peer · s share · | split · o manager ·
                x close · q quit
  typed:        /pwho · /phud [on|off] · /pgrant [name] ·
                /pinvite <email|@handle> · /plock · /pexit · /phelp
                (only at a shell prompt, not inside full-screen apps)
  The prefix works inside full-screen apps like vim and claude; press ctrl-\
  twice to send a literal ctrl-\. The shared terminal is sized to the SMALLEST
  connected screen; resize and it resizes live.

SESSION MANAGER (ptln llms)
  arrows move · / search · s cycle sort · R refresh · p pin · x archive ·
  a reveal · Enter resume here (pick a permission mode) · o open in a NEW tab
  (tmux · iTerm2 · WezTerm · kitty · GNOME Terminal · Konsole).
  Tab markers: ☎ attached to a context thread (cyan wired · dim record-only) ·
  ◉ shared live over the relay.
  ptln llms ls [--all]   flat list; --all includes agent sessions
  ptln llms prune        report sessions whose worktree is gone; --apply drops
                         them from the list and deletes nothing on disk
  ptln llms resume <id>  resume one by id or unique prefix
  ptln llms --resume     reopen the last set, same model and permission level
  It indexes claude, codex, gemini and antigravity stores, plus the llm CLI if
  installed, reading each tool's own files. Nothing leaves this machine.

SPLIT VIEW (two sessions side by side in ONE tab)
  ctrl-\ |     open a split; pick the left session (①) then the right (②).
               esc cancels. The bound pair takes ONE slot on the tab ribbon.
  ctrl-\ tab   move focus between panes (ctrl-\ i does the same)
  ctrl-\ z     zoom the focused pane to full width and back
  ctrl-\ x     close the FOCUSED pane; the pair unbinds and the other session
               becomes an ordinary tab, still running
  Every other prefix command goes to the focused pane. Exactly two panes,
  divided vertically — no horizontal split, no third pane.

DAEMON (let the web start agents on this machine)
  enable / disable            enrol or revoke this machine's device-scoped
                              token (separate from your login)
  run                         connect and listen, with an approve/deny console
  install                     run always-on as a service (launchd/systemd)
  add-project <label> [dir] [--preset spec|chat] [--engine <e>]
  scan-root add|rm|ls [dir]   advertise repos beyond your home directory
  consults <label> [auto|ask] whether a teammate's read-only question about
                              this project is answered without asking you.
                              DEFAULT AUTO — see PEER CONSULTS
  consults --all [auto|ask]   the same for the WHOLE MACHINE. "--all ask"
                              outranks every project's setting, persists, and
                              takes effect on the next question
  deliver <label> [stage|submit]   a landed peer answer is staged in your
                              prompt unsubmitted (DEFAULT), or submitted for
                              you (opt-in)
  The control plane only ever sends a LABEL — never a path or command. A
  teammate clicks "Add agent"; YOU approve here, and only then does it spawn a
  grounded agent (read-only tools) in that project dir. You can kill it
  anytime.

PEER CONSULTS (read-only Q&A between your agents and a teammate's)
  Your agent asks a teammate's agent for a second opinion; the answer comes
  back labelled UNTRUSTED. The peer's agent answers on THEIR checkout,
  read-only: it cannot write a file or run a command. ctrl-\ p is the inbox;
  agents ask with list_peers, ask_peer and check_consult.

  AUTO-ANSWER IS ON BY DEFAULT. A teammate in your org can cause this machine
  to run a read-only engine turn on your checkout, using your tokens, without
  asking you. Bounds: the turn is read-only (enforced by the engine posture),
  the question is capped at 32,000 chars, only org members can ask, and it is
  capped per day — past the cap questions QUEUE for your approval rather than
  being dropped. Default 24 per project per day plus 48 per machine; the
  per-project number is a project setting in the web. The environment
  variables PARTYLINE_CONSULT_AUTO_DAILY and PARTYLINE_CONSULT_AUTO_DAILY_TOTAL
  clamp it DOWNWARD on this box only. Turn it off with:
    ptln daemon consults --all ask     the whole machine, outranks everything
    ptln daemon consults <label> ask   one project

  AUTO-SUBMIT IS OFF BY DEFAULT. The read-only guarantee protects the machine
  ANSWERING, not the one that asked; your own session has write and shell.
  With deliver=submit a teammate's text becomes a prompt in a tool-bearing
  agent with no human reading it first. The "untrusted" label is a convention
  the model is asked to honour, not an enforcement boundary.

MCP TOOLS (partyline-context-threads — auto-wired for claude + codex)
  recall · read_context      the thread's shared context (recall narrows to
                             one entity slug)
  remember                   record ONE durable fact: overview | decision |
                             constraint | contract | question | note
  send_to_partyline          send work to the backlog to be BUILT. Checks it
                             is buildable first and hands back the questions
                             to ask you when something is missing
  propose_work_item · plan_file_tree   file one item, or a whole
                             epic ▸ feature ▸ task decomposition
  create_project             set this repo up as a project, from the session
  list_sessions · ask_session   ask another LIVE session on this machine and
                             get the answer from its warm context
  capabilities               what THIS session can do: tools, granted shell
                             commands and MCP servers, and their scope limits
  list_peers · ask_peer · check_consult   peer consults
  read_run <uuid>            diagnose a run without a browser. Read-only
  read_run_log <uuid> [tail] the tail of its step output — default 200 lines,
                             max 1000, 64KB cap, secrets redacted, fenced and
                             labelled UNTRUSTED
  setup_read · setup_write   what this instance still needs; name it and open
                             or close signups. Instance-admin only
  Both run tools need "ptln login <url>" and accept only a UUID; a run you
  can't see is reported identically to one that doesn't exist.

Full manual, including every flag and the trust model:  ptln man

DOCS  https://partyline.sh/docs
      https://partyline.sh/llms-full.txt — the whole product as one plain-text
      file, for an AI assistant that has nothing installed. No account needed.
`)
}
