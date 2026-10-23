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
	// ---- the front door ----
	{
		Group: "Sessions on this machine", Name: "llms", Summary: "browse, run, and switch every local AI session in one terminal",
		Usage: []string{
			"ptln                    the front door — same as `ptln llms`",
			"ptln llms <id>...       open those sessions",
			"ptln llms --resume      reopen the set you had open last time",
			"ptln llms --roots       where it looks for sessions, and what each place holds",
			"ptln llms --look-in <dir>   also look under this HOME (a service account, a second login)",
		},
		PassThrough: true,
	},
	{
		Name: "memory", Summary: "what the team has learned about this repo — files in the repo",
		Usage: []string{
			"ptln memory brief            what a session is told before you type: decisions, constraints, gotchas",
			"ptln memory add <kind> \"...\"  record something durable (decision|constraint|contract|gotcha|question)",
			"ptln memory ls [--all]       every current fact; --all includes superseded ones",
			"ptln memory edit <id> \"...\"   rewrite a fact that turned out wrong",
			"ptln memory append <id> \"...\" add to a fact you have learned more about",
			"ptln memory rm <id>          delete a fact (shows in git status before you commit)",
			"ptln memory where            which directory holds this repo's facts, and how many",
			"ptln memory build [--apply]  read the repo and seed facts from evidence; what it cannot derive becomes a question",
			"ptln memory map              rewrite the summary in AGENTS.md/CLAUDE.md/GEMINI.md from the facts",
			"ptln memory hooks [--install]  which engines refresh the facts mid-session, and which need trusting",
			"ptln memory harvest [-n]     record what this repo's new commits declare",
			"ptln memory propose --from <src> <kind> \"...\"   record a fact mined from another system",
			"ptln memory sync             legacy side-repo projects only; a no-op on your own repo",
			"",
			"  Facts live in `<repo>/memory`, one markdown file each, and travel with the code: a",
			"  teammate who clones gets them with no account and no server. Partyline NEVER commits",
			"  your repo — a fact is left in the working tree for you to commit with the change that",
			"  taught it. A fact is refused unless it would still be true and useful to someone who",
			"  opens the repo in three months. Agents use the remember tool; these are for repair.",
		},
		Flags: []Flag{
			{Name: "since", Arg: "<rev>", Doc: "harvest this git range instead of what is new (e.g. v1.0..HEAD)"},
			{Name: "repo", Arg: "<name>", Doc: "the project repo a fact is about (default: the one you are in)"},
			{Name: "supersedes", Arg: "<id>", Doc: "the fact this replaces — the old one stops being briefed"},
			{Name: "tags", Arg: "a,b", Doc: "subjects; facts sharing a tag that disagree are surfaced as a conflict"},
			{Name: "all", Doc: "include superseded facts"},
		},
	},
	{
		Name: "tmux", Summary: "host sessions in tmux (the default backend; needs tmux 3.3+)",
		Usage: []string{
			"ptln tmux               one window running your quick engine",
			"ptln tmux --resume      one window per saved workspace session",
			"ptln tmux --quit        shut EVERY session down (saved first) — update, then --resume",
			"PARTYLINE_MUX=classic ptln  opt out — use the built-in multiplexer instead",
		},
		Flags: []Flag{
			{Name: "resume", Doc: "Reopen the saved workspace (same set as `ptln llms --resume`)"},
			{Name: "quit", Doc: "Save the workspace, then stop every session and the host — the update-friendly exit (also ctrl-\\ Q); `ptln --resume` brings each conversation back"},
			{Name: "print-conf", Doc: "Print the generated tmux conf and exit"},
		},
	},
	{
		Name: "new", Summary: "start a fresh AI session",
		Usage: []string{
			"ptln new <claude|codex|gemini|opencode|goose|antigravity> [flags]",
		},
		Flags: []Flag{
			{Name: "worktree", Arg: "<name>", Doc: "Isolate it in its own git worktree"},
			{Name: "keep-going", Arg: "N", Doc: "Let the engine auto-continue up to N turns"},
			{Name: "goal", Arg: "<text>", Doc: "The done condition for --keep-going"},
		},
		PassThrough: true,
	},

	// ---- autonomous work ----
	{
		Name: "keepgoing", Summary: "let an engine auto-continue up to a hard turn cap",
	},

	// ---- fleet and projects ----
	{
		Name: "state", Summary: "a JSON snapshot of this machine: sessions, what is waiting on you, rate limits",
	},

	// ---- context and skills ----

	// ---- collaboration ----
	{
		Name: "wt", Aliases: []string{"worktree"}, Summary: "the git worktrees sessions create — list, remove, prune",
		Subs: []string{"ls: list worktrees", "rm: remove one", "prune: remove worktrees whose session is gone (dry run; --yes applies)"},
	},

	// ---- account and machine ----
	// Read-only, and named as a plain verb on purpose: it is what every error message points at,
	// so it has to be the thing a person guesses without being told.
	{Name: "upgrade", Aliases: []string{"update"}, Summary: "update the CLI in place"},
	{Name: "version", Aliases: []string{"--version"}, Summary: "print the version"},
	{Name: "man", Summary: "the full manual"},
	{Name: "help", Aliases: []string{"-h", "--help"}, Summary: "every command"},
	{Name: "welcome", Summary: "the first-run introduction"},
	{Name: "tray", Summary: "the menubar companion"},

	// ---- spawned by other programs, never typed ----
	{Name: "keepgoing-hook", Summary: "engine hook for --keep-going", Hidden: true},
	{Name: "memory-hook", Summary: "engine hook: re-brief a running session with new project memory", Hidden: true},
	{Name: "memory-capture-hook", Summary: "engine hook: record what a session learned, without being asked", Hidden: true},
}
