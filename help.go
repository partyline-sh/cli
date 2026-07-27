package main

import "fmt"

// helpMain prints CLI usage. Shown by `ptln help`, `-h`, `--help`, and on an
// unknown command.
func helpMain() {
	fmt.Print(`partyline — multiplayer LLM dev

USAGE
  ptln                 open your session manager — browse, run, and switch your
                       claude/codex/gemini sessions in ONE terminal (the front door)
  ptln new <tool>      start a fresh AI session — claude | codex | gemini | opencode | goose | antigravity
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
  scribe         distill this directory's newest AI session into durable facts on
                 its context thread (automatic capture, on demand). Runs locally —
                 the raw conversation never leaves this machine, only the facts.
                 ptln scribe [--thread <id>] [--session <id>] [--min-turns <n>]
  wt             session worktrees — list/remove the isolated dirs that
                 "ptln new <tool> --worktree <name>" creates (ptln wt help)
  work           run ONE task autonomously in a sandboxed worktree, then leave a
                 branch for you to review: ptln work "<task>" [--worktree <name>]
                 [--thread <id>] [--allow-bash] [--engine <e>] [--model <m>].
                 Runs the repo's registered engine by default (--engine
                 claude|codex|gemini|opencode|goose overrides; codex/goose require --allow-bash,
                 antigravity can't run headless). Read/edit tools only by default;
                 never pushes — a human merges. The worker atom behind the conductor.
  plan           the Planning agent: it interviews a rough idea into a well-specified,
                 SCORED backlog item (Epic/Feature/Task) in the planning tree — runs
                 the repo's registered engine locally (your auth, no server key). In a
                 registered repo it needs NO flags: the project implies the thread
                 (umbrella-aware, created on first use). ptln plan [--kind
                 epic|feature|task] [--thread <id>] [--quick]. /quick /deep switch
                 modes, /done, /quit. (aliases: shape, describe)
  crank          drive a BACKLOG one task at a time, each in its own worktree, sharing
                 one context thread: ptln crank --file backlog.txt [--thread <id>]
                 [--max N] [--max-tokens N] [--halt-on-fail K] [--engine <e>]
                 [--model <m>] [--literal] [--max-repairs N]. Halts on cap/tokens/
                 failures; prepares N branches for review — nothing is pushed. Blank lines and
                 #-comments are skipped unless --literal (task titles may start with #).
  keepgoing      manage safe auto-continue (status|off) — arm at launch with
                 "ptln new claude --keep-going N" or mid-session with ctrl-\ g
  join-mcp [link] add a Party to the LLM session you're already in (MCP), so it
                 can read + post — --print shows setup for non-Claude tools
  peer           peer consults — read-only Q&A between your agents and a teammate's.
                 ptln peer approve|decline <id> decides a question queued for THIS
                 machine; ptln peer cancel <id> withdraws one YOU asked; agents ask
                 with the ask_peer MCP tool, and ctrl-\ p is the inbox (ask someone,
                 read replies, withdraw an ask). See PEER CONSULTS below
  daemon         let the web launch a grounded agent on this machine, in the right
                 project dir, with one click — no per-launch commands (see DAEMON)
  org            manage your org's members, roles, and invites (alias: team).
                 owner: manage members + billing · admin: invite + create projects ·
                 member: use the system
  project        projects — ls | new | show <label> (machines, runs, threads)
                 agent tool grants: ptln project tools <label> [--role planning|build
                 --allow-shell "gh *" --allow-mcp <name>] — what launched agents may use
  settings       master index: every setting's current state + where to change it
  skill          org skill library — push/list/pull/install Agent Skills
                 (<name>/SKILL.md). Enabled skills are injected into every agent
                 run's workspace; "ptln skill install" adds them to your local
                 ~/.agents/skills + ~/.claude/skills too. Run: ptln skill help
  tray           macOS menu bar companion — live sessions, waiting-on-you native
                 notifications, daemon control, rate-limit display. Starts
                 automatically with the session manager or daemon; "ptln tray on"
                 adds it at login, "ptln tray off" opts out, bare = status (macOS)
  state          machine-readable JSON snapshot: account, daemon, live sessions,
                 waiting-on-you, rate limits — what the tray reads; script-friendly
  welcome        the front-door welcome screen — resume / new / share / plan / find
                 (bare ptln shows it automatically when you have no sessions yet)
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
  --announce           with --team <slug>: post the join code to that team's
                       connected Slack channel
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
                Runs --engine claude by default (codex/gemini/opencode/goose/antigravity too);
                --cmd swaps in a custom command instead. The reply is posted back. No login or MCP setup needed — the
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
                p peer (ask a teammate's agent a read-only question, read the
                questions + replies waiting for you, and withdraw an ask that is
                still out — see PEER CONSULTS) ·
                s share (broadcast this session view-only, or a new shared tab) ·
                | SPLIT the tab in two side-by-side panes (see SPLIT VIEW) ·
                o manager · x close · q quit.
                A dim panel above the tab bar lists every command while the prefix is
                armed, and the bar's right end shows the current mode
                (LIVE · CHORD · SELECT · SPLIT · SETUP) with the keys live in it.
                (The mouse stays native — select to copy, paste with the
                terminal; the agent gets the wheel if it uses one.)
  ptln llms ls [--all]   flat list (newest first); --all includes agent sessions
  ptln llms prune        report sessions whose git worktree no longer exists;
                --apply drops them from the list (never deletes anything on disk)
  ptln llms resume <id>  resume one session by id or unique prefix
  ptln llms --resume     reopen every session you had open last time, each with the
                same model + permission level (saved when you quit the launcher)
                Indexes claude, codex, gemini, and antigravity (agy) stores;
                the llm CLI if installed. Reads each tool's own files — nothing
                leaves the local launcher, no network.

SPLIT VIEW (two sessions side by side in ONE tab)
  ctrl-\ |       open a split. It starts EMPTY, with a session manager in each pane:
                 pick the left one (① in its title), then the right (②) — esc cancels.
                 Once both are filled the pair is BOUND and occupies a single slot on the
                 tab ribbon, so ←/→ and 1-9 treat the pair as one tab.
  ctrl-\ tab     move focus between the panes (ctrl-\ i does the same). The focused pane's
                 title is pink; the other is dimmed.
  ctrl-\ z       zoom the focused pane to full width and back (a toggle; the pair stays
                 bound, so the other session is still there when you unzoom)
  ctrl-\ x       close the FOCUSED pane — the pair unbinds and the other session becomes
                 an ordinary full-width tab. The closed pane's session keeps running.
  Everything else on the ctrl-\ prefix goes to the FOCUSED pane. Switching away parks the
  split and remembers its focus + zoom; coming back re-enters it exactly as you left it.
  Exactly two panes, divided vertically, under one full-width status bar — there is no
  horizontal split and no third pane.

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
  ptln daemon consults <label> [auto|ask]
                                   whether a teammate's READ-ONLY question about this
                                   project is answered without asking you. DEFAULT AUTO
                                   (see PEER CONSULTS); "ask" queues every question for
                                   your approval instead. Bare = show the current setting
  ptln daemon consults --all [auto|ask]
                                   the same switch for the WHOLE MACHINE, and the off
                                   switch you want: "--all ask" outranks every project's
                                   setting (including ones added later), persists, and
                                   takes effect on the next question — no restart, no
                                   reinstall, always-on service included
  ptln daemon deliver <label> [stage|submit]
                                   what happens when a peer's answer lands for a session
                                   in this project: stage it in your prompt, unsubmitted
                                   (DEFAULT), or submit it to your agent for you. Submit
                                   is off by default on purpose — see PEER CONSULTS
  ptln daemon disable              revoke the device token + remove it locally
                The control plane only ever sends a LABEL — never a path or command.
                A teammate clicks "Add agent" on the party page; YOU approve here, and
                only then does it spawn a grounded agent (read-only tools) in that
                project dir, joined to the party. Confirm-first; you can kill anytime.

PEER CONSULTS (read-only Q&A between your agents and a teammate's)
  Your agent asks a teammate's agent for a second opinion — "does this API change break
  your callers?" — and the answer comes back labelled UNTRUSTED. The peer's agent answers
  on THEIR checkout, read-only: it cannot write a file or run a command.

  ctrl-\ p       the inbox, inside ptln: ask a peer, read the questions + replies waiting
                 for you, and withdraw an ask that is still out (open it → withdraw).
                 The question field is MULTI-LINE, for text pasted out of an LLM
                 discussion: a paste arrives whole, newlines and all —
                   ⏎ newline · ctrl-d send · ctrl-e open $EDITOR · esc back
                 It counts characters live against the 32,000 limit and refuses to send
                 an over-long question locally, naming what to trim.
  MCP tools      on the partyline-context-threads server (auto-wired for claude + codex):
                 list_peers      which machines you can ask, and which project labels
                                 each one answers about
                 ask_peer        ask ONE machine about ONE of its projects. It waits up
                                 to 45s for a fast answer, then hands back a CONSULT ID
                                 rather than holding the tool call open
                 check_consult   collect an answer that landed after that, using the id.
                                 Call it once, minutes later — not in a loop. Consults
                                 expire about 10 minutes after they're asked
                 (the same server also carries read_run / read_run_log — see MCP TOOLS)
  ptln peer approve <id>   answer a queued question: ONE read-only turn on this machine's
                           checkout. It prints the question first — the daemon requires a
                           digest of the text that was DISPLAYED, so there is no way to
                           approve a question nobody read.
  ptln peer decline <id>   decline it, freeing the asker now instead of at the timeout.
  ptln peer cancel <id>    withdraw a question YOU asked. Only the asker can cancel (it
                           goes to the control plane with your account token, not to a
                           daemon), and it is the one way an ask stops being answerable
                           before the 10-minute window: the peer's machine drops it and
                           never spends a read-only turn on it. Cancelling one that was
                           already answered or declined is a no-op that tells you so.
  The tray (macOS) shows queued questions with Approve/Decline, what this machine answered
  today, and replies waiting for you. Its notifications carry NAMES AND COUNTS ONLY — never
  question or answer text, because banners land on lock screens and in system logs.

  AUTO-ANSWER IS ON BY DEFAULT — what that means
  Consults are answered AUTOMATICALLY within a daily budget. A human is asked only when the
  budget is spent, or when auto-answer is switched off — NOT for every question. So a
  teammate in your org can cause this machine to run a read-only engine turn on your
  checkout, using your tokens, without asking you. Bounds: the turn is read-only (enforced
  by the engine posture, not by convention), the question is size-limited (32,000 chars),
  only org members can ask, and it is capped per day — past the cap questions QUEUE for
  your approval rather than being dropped. Every consult leaves a durable row to review.

  THE BUDGET  default 24 per project per day, plus 48 for this whole machine across every
  project. The per-project number is a PROJECT SETTING you edit once in the web (project
  settings → "auto-answer allowance"), and every machine advertising that label honours it
  — no per-box env editing. 0 means "never auto-answer in this project; every consult waits
  for a human". Blank means the built-in default. The CAP is project-wide but the SPEND is
  counted per machine, so three machines at 24 can answer 72 between them.
    PARTYLINE_CONSULT_AUTO_DAILY        a CEILING, not a setter: it clamps the project
    PARTYLINE_CONSULT_AUTO_DAILY_TOTAL  setting DOWNWARD on this box (and is the fallback
                                        when the project has no setting). A machine can
                                        tighten itself below what the project asks for;
                                        nothing on the wire can raise it. The compiled
                                        hard ceilings are 200/project and 400/machine
    ptln daemon consults --all ask        turn it off for THIS WHOLE MACHINE. This is the
                                          one to use: it outranks every project's setting,
                                          survives reboots, and the daemon picks it up on
                                          the next question — nothing to restart. Read it
                                          back with the same command, bare, or ptln settings
    ptln daemon consults <label> ask      turn it off for one project (restores the queue)

  AUTO-SUBMIT IS OFF BY DEFAULT — and why it should stay off
  The read-only guarantee protects the machine ANSWERING a question, not the machine that
  asked. Your own session has whatever tools it was launched with — usually write and shell.
  With deliver=stage (the default) a landed answer is pasted into your prompt UNSUBMITTED
  and needs your Enter. With deliver=submit, a teammate's text becomes a prompt in a
  tool-bearing agent with no human reading it first. The "untrusted" label on an answer is a
  convention the model is asked to honour, not an enforcement boundary.
    ptln daemon deliver <label> submit    opt in, per project (default: stage)

MCP TOOLS (the partyline-context-threads server — auto-wired for claude + codex)
  recall · read_context      the thread's current shared context (recall takes an optional
                             entity slug to narrow it to one thing)
  remember                   record ONE durable cross-seam fact: overview | decision |
                             constraint | contract | question | note
  propose_work_item          file one Epic/Feature/Task into the thread's planning tree
  plan_file_tree             file a WHOLE epic ▸ feature ▸ task decomposition in one call
  list_peers · ask_peer · check_consult    peer consults — see PEER CONSULTS
  read_run <run-uuid>        diagnose a run WITHOUT a browser: status, preset,
                             engine/model, merge policy, token spend, wall time, every
                             task's idx/title/status/branch/PR, the plan item it came from
                             and its place in a chain. Read-only, GET only
  read_run_log <run-uuid> [tail]
                             the tail of that run's step output — the worker's own log
                             lines. Default 200 lines, max 1000, capped at 64KB, secrets
                             redacted, fenced and labelled UNTRUSTED (it is third-party
                             build output, not instructions)
  Both run tools need "ptln login" on this machine and only accept a UUID; a run you can't
  see is reported identically to one that doesn't exist.

DOCS  https://partyline.sh/docs
`)
}
