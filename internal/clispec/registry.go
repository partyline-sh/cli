package clispec

// Commands is the registry: every command `ptln` dispatches, declared once.
//
// Scope note, stated plainly so nobody mistakes this for more than it is. Summaries, usage, and
// subcommands are complete. FLAGS are declared for the commands whose parsers were read while
// writing this (crank, work) and are filled in per command as parsing migrates onto the spec —
// a half-declared flag list is honest and useful, whereas a guessed one would be the exact kind of
// confident-and-wrong reference this epic exists to abolish. TestEveryDispatchedCommandHasASpec
// keeps the command list itself complete; there is no equivalent guard for flags yet, and there
// will be once parsing moves here.
var Commands = []Spec{
	{Name: "_contact-sheet", Hidden: true, Summary: "render every UI state as a deterministic plain-text contact sheet", Usage: []string{"ptln _contact-sheet [-o docs/contact-sheet.txt]"}},
	// ---- sessions ----
	{
		Group: "Sessions", Name: "llms", Summary: "the session manager: every AI session on this machine in one list",
		Usage: []string{
			"ptln                        open the session manager (same as `ptln llms`)",
			"ptln llms <id>...           open those sessions in one terminal",
			"ptln llms --resume          reopen the sessions you had open last time",
			"ptln llms ls [--all]        list sessions, newest first (--all includes agent sessions)",
			"ptln llms resume <id>       resume one session in this terminal, without the multiplexer",
			"ptln llms prune [--apply]   list sessions whose directory or worktree is gone (--apply removes them)",
			"ptln llms --roots           where sessions are read from",
			"ptln llms --look-in <dir>   also read sessions under another home directory",
		},
		PassThrough: true,
	},
	{
		Name: "new", Summary: "start a new session",
		Usage: []string{
			"ptln new <claude|codex|gemini|opencode|goose|antigravity> [dir] [flags] [engine args...]",
			"  arguments after the engine name are passed to the engine",
		},
		Flags: []Flag{
			{Name: "worktree", Arg: "<name>", Doc: "start it in a new git worktree on a branch of that name"},
			{Name: "keep-going", Arg: "N", Doc: "answer \"continue?\" up to N times (Claude only)"},
			{Name: "goal", Arg: "<text>", Doc: "the instruction given with each --keep-going continuation"},
		},
		PassThrough: true,
	},
	{
		Name: "tmux", Summary: "the tmux server that hosts sessions (needs tmux 3.3+)",
		Usage: []string{
			"ptln tmux --resume       reopen the saved set of sessions",
			"ptln tmux --quit         save the set, then stop every session and the server",
			"ptln tmux --print-conf   print the generated tmux configuration",
		},
		Flags: []Flag{
			{Name: "resume", Doc: "reopen the saved set of sessions (same as `ptln --resume`)"},
			{Name: "quit", Doc: "save the set, stop every session and the server (also ctrl-\\ Q); `ptln --resume` reopens them"},
			{Name: "print-conf", Doc: "print the generated tmux configuration and exit"},
		},
	},
	{
		Name: "wt", Aliases: []string{"worktree"}, Summary: "the git worktrees sessions were started in",
		Subs: []string{"ls: list them", "rm: remove one by branch", "prune: list worktrees whose session is gone (--yes removes them)"},
	},
	{
		Name: "keepgoing", Summary: "the keep-going hook on Claude sessions",
		Subs: []string{"status: list sessions with keep-going armed (the default)", "off: disarm keep-going on every session"},
	},

	// ---- repo memory ----
	{
		Group: "Repo memory", Name: "memory", Summary: "facts about a repository, kept as files in memory/",
		Usage: []string{
			"ptln memory build [--apply]           set the repo up: a project overview, build/test/CI facts, open questions",
			"ptln memory build --apply --refresh   rewrite the overview from the last commit; list facts whose files changed",
			"ptln memory add <kind> \"...\"         record a fact (overview|decision|constraint|contract|gotcha|question)",
			"ptln memory ls [--all]                list current facts (--all includes superseded ones)",
			"ptln memory brief                     what a session is told when it starts",
			"ptln memory edit <id> \"...\"          rewrite a fact",
			"ptln memory append <id> \"...\"        add to a fact",
			"ptln memory verify <id>...            mark facts as checked against the code at HEAD",
			"ptln memory rm <id>                   delete a fact",
			"ptln memory where                     where this repo's facts are, and how many",
			"ptln memory map                       rewrite the generated block in AGENTS.md, CLAUDE.md and GEMINI.md",
			"ptln memory hooks [--install]         which engines refresh facts during a session, and which need a trust grant",
			"ptln memory harvest [-n]              record facts declared in commit trailers (-n: show only)",
			"ptln memory propose --from <src> <kind> \"...\"   record a fact taken from another system, with its source",
			"",
			"  Facts are files in <repo>/memory/. partyline does not commit them; you commit them with your change.",
			"  A fact about a machine, a ticket's status or this session alone is refused, with the reason.",
		},
		Flags: []Flag{
			{Name: "tags", Arg: "a,b", Doc: "subjects; `architecture` places a fact under Architecture in the generated block"},
			{Name: "supersedes", Arg: "<id>", Doc: "the fact this one replaces; the old one is no longer briefed"},
			{Name: "since", Arg: "<rev>", Doc: "harvest this git range instead of the commits since the last harvest"},
			{Name: "repo", Arg: "<name>", Doc: "legacy side-repo projects: the member repo a fact is about"},
			{Name: "all", Doc: "include superseded facts"},
		},
	},

	// ---- chat ----
	{
		Group: "Chat", Name: "chat", Summary: "one room where you and your running sessions talk, and they talk to each other",
		Usage: []string{
			"ptln chat                     open the room; plain text goes to everyone, @11 or @codex to one session",
			"  in the room: /add <tab>... · /remove <tab> · /who · /stop [tab] · /go [tab] · /clear · /help (every key) · ctrl-c leave",
			"ptln chat add <tab>...        add sessions from outside the room",
			"ptln chat remove <tab>...     take sessions out",
			"ptln chat who                 who is in the room, and whether each one is listening",
			"ptln chat send \"...\"          post as this session (sessions run this to reply)",
			"ptln chat read                print the messages waiting for this session",
			"ptln chat listen              wait for a message, print it, exit (Claude sessions run this in the background)",
		},
	},

	{Group: "Mobile remote", Name: "remote", Summary: "pair, list, and revoke phones", Usage: []string{"ptln remote pair", "ptln remote phones", "ptln remote revoke <id>"}, Subs: []string{"pair: show a private QR popup, then approve the phone fingerprint with the code", "phones: list paired phones, fingerprints, paired time, and last seen", "revoke: remove a phone and revoke its relay device token"}},

	{Group: "Mobile remote", Name: "relay", Summary: "deploy and connect machines to your Cloudflare relay",
		Usage: []string{"ptln relay", "ptln relay deploy", "ptln relay invite", "ptln relay join <string>", "ptln relay status", "ptln relay forget-token"},
		Subs:  []string{"(none): the relay settings screen: deploy or update, the saved token, pairing", "deploy: deploy or update the embedded Worker on your Cloudflare account's workers.dev", "invite: print a one-use invite valid for 10 minutes", "join: redeem an invite for this machine", "status: show URL, RP ID, this machine's connection and whether the phone app is current", "forget-token: delete the saved Cloudflare token"},
	},

	// ---- this machine ----
	{Group: "This machine", Name: "state", Summary: "a JSON snapshot: live sessions, how many are waiting on you, the provider rate limit"},

	{
		Name: "tray", Summary: "the macOS menu-bar icon",
		Subs: []string{"on: run it now and at login", "off: stop it and remove the login item"},
	},
	{Name: "theme", Summary: "pick the colour palette and dark or light background",
		Usage: []string{"ptln theme [Partyline|Blue|Mono|Paperwhite] [--background auto|dark|light]"}},
	{Name: "upgrade", Aliases: []string{"update"}, Summary: "update partyline the way it was installed (Homebrew or the install script)"},
	{Name: "version", Aliases: []string{"--version"}, Summary: "print the version, and whether a newer one exists"},
	{Name: "welcome", Summary: "the first-run screen"},
	{Name: "man", Summary: "the manual"},
	{Name: "help", Aliases: []string{"-h", "--help"}, Summary: "this command list"},

	// ---- spawned by other programs, never typed ----
	{Name: "loop", Summary: "cross-model review of the current work (experimental)", Hidden: true,
		Usage: []string{
			"ptln loop " + LoopGuide + "   workflow for the calling session’s engine",
			"ptln loop review [--by claude,codex[:model]] [--base <ref>] [--brief <file>] [--report <file>]",
			"ptln loop " + LoopStart + " --brief <file> --checks \"<cmd>\" [--checks ...] [--review claude,codex] [--config <file>]   (run by the session)",
			"ctrl-\\ then a                 approve, decide disputes, or stop in the approval popup",
			"ptln loop " + LoopReady + " <id> --report <file>   the session says it is done; partyline checks and reviews",
			"ptln loop " + LoopDispute + " <id> <finding> --reason \"...\"",
			"ptln loop pr <id> <url-or-number>   the session records the PR it opened at pr_ready; the board shows it and its CI",
			"ptln loop resume <id>           run a paused review again (a reviewer could not run; a limit resumes by itself)",
			"ptln loop delete <id>           stop a loop, or its whole batch, and remove it for good (d on its card in ctrl-\\ l)",
			"ptln loop brief <id> --file <file>",
			"ptln loop " + LoopListen + " <id> · wait for results (Claude background task)",
			"ptln loop " + LoopInbox + " <id> · read results when woken",
			"ptln loop status [<id>]",
			"ptln loop tab [--contractor <id>]   Loops full-screen; select a contractor; popup with ctrl-\\ l",
			"ptln loop " + LoopBatch + " " + LoopStart + " --brief <file> --brief <file> [...] --checks \"<cmd>\" [--config <file>]   one approval for every step (run by the session)",
			"ptln loop batch revise <id> --brief <file> ... [--dispute <finding>=<reason>] · next <id> · status <id>",
			"  reviews the working tree against <ref> (default: where the branch left main)",
			"  exit 0 approve · 1 a P1 (changes_required if verified, unverified if not) · 2 a reviewer failed",
		}},
	{Name: "keepgoing-hook", Summary: "engine hook for --keep-going", Hidden: true},
	{Name: "memory-hook", Summary: "engine hook: re-brief a running session with new project memory", Hidden: true},
	{Name: "memory-capture-hook", Summary: "engine hook: record what a session learned, without being asked", Hidden: true},
	{Name: "chat-hook", Summary: "engine hook: hand waiting chat messages to a session", Hidden: true},
	{Name: "_watcher", Summary: "background watcher: loop indicators, and chat wakes when no chat view is open", Hidden: true},
	{Name: "_contractor", Summary: "runs one contractor (an agent a loop started) inside its window", Hidden: true},
}
