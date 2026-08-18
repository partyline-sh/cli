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
		Name: "llms", Summary: "browse, run, and switch every local AI session in one terminal",
		Usage: []string{
			"ptln                    the front door — same as `ptln llms`",
			"ptln llms <id>...       open those sessions",
			"ptln llms --resume      reopen the set you had open last time",
		},
		PassThrough: true,
	},
	{
		Name: "new", Summary: "start a fresh AI session",
		Usage: []string{
			"ptln new <claude|codex|gemini|opencode|goose|antigravity> [flags]",
		},
		Flags: []Flag{
			{Name: "thread", Arg: "<id>", Doc: "Attach the session to a context thread"},
			{Name: "worktree", Arg: "<name>", Doc: "Isolate it in its own git worktree"},
			{Name: "keep-going", Arg: "N", Doc: "Let the engine auto-continue up to N turns"},
			{Name: "goal", Arg: "<text>", Doc: "The done condition for --keep-going"},
		},
		PassThrough: true,
	},
	{
		Name: "start", Summary: "host a shared shell and print a join link",
		Usage: []string{
			"ptln start [flags]",
			"ptln start -- <program>   share a specific program instead of your $SHELL",
		},
		PassThrough: true,
	},

	// ---- autonomous work ----
	{
		Name: "work", Summary: "run ONE task autonomously in a sandboxed worktree, then leave a branch",
		Usage: []string{`ptln work "<task>" [flags]`},
		Flags: []Flag{
			{Name: "worktree", Arg: "<name>", Doc: "Name the isolated worktree (alias: --wt)"},
			{Name: "thread", Arg: "<id>", Doc: "Attach the run to a context thread"},
			{Name: "engine", Arg: "<e>", Doc: "claude | codex | gemini | opencode | goose"},
			{Name: "model", Arg: "<m>", Doc: "Engine-specific model name"},
			{Name: "allow-bash", Doc: "Grant shell access (required by codex and goose)"},
			{Name: "timeout", Arg: "<dur>", Doc: "Wall-clock budget for the task, e.g. 20m"},
		},
	},
	{
		Name: "crank", Summary: "run a backlog of tasks through the fleet, one branch each",
		Usage: []string{
			"ptln crank --file <backlog.txt> [flags]",
			"ptln crank --claim --run <id> --workers N     drain a run's tasks in parallel",
		},
		Flags: []Flag{
			{Name: "file", Arg: "<path>", Doc: "Worklist file, one task per line"},
			{Name: "literal", Doc: "Treat --file's value as the task text itself"},
			{Name: "thread", Arg: "<id>", Doc: "Attach every task to a context thread"},
			{Name: "run", Arg: "<uuid>", Doc: "Report task lifecycle to this run in the control plane"},
			{Name: "claim", Doc: "Fleet mode: claim tasks from the run store instead of a file"},
			{Name: "workers", Arg: "N", Doc: "Concurrent workers in claim mode"},
			{Name: "resume", Doc: "Continue a run, skipping tasks already done"},
			{Name: "restart", Doc: "Discard prior attempts and rebuild from the base branch"},
			{Name: "max", Arg: "N", Doc: "Stop after N tasks"},
			{Name: "max-tokens", Arg: "N", Doc: "Token ceiling; pauses for approval when reached"},
			{Name: "max-repairs", Arg: "N", Doc: "Auto-repair rounds allowed after a failed gate"},
			{Name: "halt-on-fail", Arg: "K", Doc: "Stop after K consecutive failures"},
			{Name: "timeout", Arg: "<dur>", Doc: "Per-task wall-clock budget"},
			{Name: "engine", Arg: "<e>", Doc: "Engine to build with"},
			{Name: "model", Arg: "<m>", Doc: "Model to build with"},
			{Name: "allow-bash", Doc: "Grant workers shell access"},
			{Name: "no-commit", Doc: "Leave changes uncommitted in the worktree"},
			{Name: "branch", Arg: "<name>", Doc: "Chain mode: every task builds on this one branch"},
			{Name: "base", Arg: "<name>", Doc: "Fork point and pull-request target"},
			{Name: "merge-policy", Arg: "<p>", Doc: "manual | pr | auto"},
			{Name: "draft", Doc: "Open pull requests as drafts"},
			{Name: "git-provider", Arg: "<p>", Doc: "Which host's CLI to speak (github)"},
			{Name: "land", Doc: "Merge train: land verified branches onto the base one at a time"},
			{Name: "visual", Doc: "Enable the visual verify lane"},
			{Name: "visual-routes", Arg: "<file>", Doc: "App paths for the visual lane to render"},
			{Name: "globals-file", Arg: "<path>", Doc: "Project rules injected into each worktree"},
			{Name: "grants-file", Arg: "<path>", Doc: "Credential grants for this run"},
			{Name: "skills-dir", Arg: "<path>", Doc: "Skills to materialise in each worktree"},
		},
	},
	{
		Name: "plan", Aliases: []string{"shape", "describe"},
		Summary: "the Planning agent: turn an idea into a scored, buildable work item",
		Usage: []string{
			`ptln plan ["<idea>"]`,
			`ptln plan ls`,
			`ptln plan show <thread-id>`,
			`ptln plan rm <thread-id>`,
		},
	},
	{
		Name: "keepgoing", Summary: "let an engine auto-continue up to a hard turn cap",
	},

	// ---- fleet and projects ----
	{
		Name: "daemon", Summary: "the always-on worker: run work this machine is asked to do",
		Subs: []string{
			"start: run the daemon in the foreground",
			"install: install and start it as a system service",
			"status: show whether it is running and what it is working on",
			"destinations: list the directories this machine advertises",
			"add-project: register a directory as a named project",
			"projects: list the projects this machine serves",
		},
	},
	{
		Name: "project", Summary: "the durable substrate for shared context",
		Subs: []string{"ls: list projects", "show: show one project", "use: set the current project"},
	},
	{
		Name: "peer", Summary: "talk to other partyline sessions and machines",
	},
	{
		Name: "state", Summary: "show what this machine believes about itself",
	},

	// ---- context and skills ----
	{
		Name: "thread", Summary: "shared context across people, machines, and engines",
		Subs: []string{
			"new: create a thread",
			"ls: list threads",
			"show: print a thread's facts",
			"connect: wire an engine to a thread by default",
			"disconnect: unwire an engine",
			"share: share a thread with a team",
		},
	},
	{
		Name: "scribe", Summary: "distil this directory's newest session into durable facts, locally",
		Usage: []string{"ptln scribe [--thread <id>] [--session <id>] [--min-turns <n>]"},
	},
	{
		Name: "skill", Aliases: []string{"skills"}, Summary: "the org skill library",
		Subs: []string{"push: publish a skill directory", "list: list skills", "pull: fetch one", "install: install into an engine"},
	},
	{
		Name: "template", Aliases: []string{"templates"}, Summary: "reusable agent templates",
	},

	// ---- collaboration ----
	{
		Name: "party", Summary: "humans and agents in one channel",
		Usage: []string{
			"ptln party            start a new party, or join a running one",
			"ptln party <link>     bring an AI agent into that party",
			"ptln party up         bring up a room of agents from partyline.yml",
			"ptln party context    switch a live party's persona or project",
		},
	},
	{
		Name: "join", Summary: "join a shared session by link, or pick from your invitations",
		Usage: []string{"ptln join [link]"},
	},
	{
		Name: "sessions", Summary: "list your live sessions",
	},
	{
		Name: "summon", Summary: "bring a teammate or agent into the session you are hosting",
	},
	{
		Name: "chat", Summary: "talk to partyline from Slack, Telegram, or Discord",
	},
	{
		Name: "wt", Aliases: []string{"worktree"}, Summary: "the isolated directories sessions and crank create",
		Subs: []string{"ls: list worktrees", "rm: remove one", "prune: clear finished crank worktrees (dry run; --yes applies)"},
	},

	// ---- account and machine ----
	{Name: "login", Summary: "authenticate this machine with the control plane (device flow)"},
	{
		Name: "setup", Summary: "connect this machine end to end: account, worker, engine, projects, memory",
		Flags: []Flag{{Name: "redo", Doc: "Re-ask everything; Enter keeps the current answer"}},
	},
	// Read-only, and named as a plain verb on purpose: it is what every error message points at,
	// so it has to be the thing a person guesses without being told.
	{Name: "doctor", Summary: "check whether this repo can plan and run work, and name the fix for anything that can't"},
	{Name: "logout", Summary: "remove this machine's saved token"},
	{Name: "whoami", Summary: "show the logged-in account"},
	{Name: "me", Aliases: []string{"profile"}, Summary: "your profile"},
	{Name: "team", Aliases: []string{"org"}, Summary: "your team: members, roles, invitations"},
	{Name: "key", Aliases: []string{"keys"}, Summary: "API tokens for this account"},
	{Name: "notify", Aliases: []string{"notifications"}, Summary: "notification preferences",
		Usage: []string{"ptln notify [ls|on|off|quiet]"}},
	{Name: "settings", Summary: "this machine's local settings"},
	{
		Name: "server", Summary: "self-host diagnostics for a partyline box",
		Usage: []string{
			"ptln server doctor           which features this machine's environment configures",
			"ptln server doctor --json    the same report, machine-readable",
		},
		Subs: []string{
			"doctor: report each feature as configured / not-configured, naming the variables a not-configured one is missing",
		},
		Flags: []Flag{
			{Name: "json", Doc: "Machine-readable report (doctor)"},
		},
	},
	{Name: "trigger", Aliases: []string{"triggers"}, Summary: "fire work on a schedule or an event"},
	{Name: "webhook", Aliases: []string{"webhooks"}, Summary: "inbound webhooks that start work"},
	{Name: "upgrade", Aliases: []string{"update"}, Summary: "update the CLI in place"},
	{Name: "version", Aliases: []string{"--version"}, Summary: "print the version"},
	{Name: "man", Summary: "the manual page"},
	{Name: "help", Aliases: []string{"-h", "--help"}, Summary: "every command"},
	{Name: "welcome", Summary: "the first-run introduction"},
	{Name: "tray", Summary: "the menubar companion"},

	// ---- spawned by other programs, never typed ----
	{Name: "cg-mcp", Summary: "context-threads MCP server (stdio)", Hidden: true},
	{Name: "run-mcp", Summary: "run MCP server (stdio)", Hidden: true},
	{Name: "party-mcp", Summary: "party MCP server (stdio)", Hidden: true},
	{Name: "join-mcp", Summary: "join MCP server (stdio)", Hidden: true},
	{Name: "keepgoing-hook", Summary: "engine hook for --keep-going", Hidden: true},
	{Name: "evidence-spike", Summary: "evidence-gate harness", Hidden: true},
}
