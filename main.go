// partyline — let's launch a shared shell.
// Default: your $SHELL, shared. Run whatever you want in it.
package main

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"

	"partyline.sh/partyline/internal/obs"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	defer obs.Init("cli")()
	defer obs.Recover() // report panics (no-op unless SENTRY_DSN is set)
	// Anonymous, opt-out usage ping (throttled ≤1/day; see telemetry.go). Skipped for the hidden
	// stdio subcommands — they're spawned per-launch (share the install's id anyway) and must not
	// write to their protocol streams.
	if len(os.Args) < 2 || (os.Args[1] != "cg-mcp" && os.Args[1] != "party-mcp") {
		maybeTelemetryPing()
		// #557 — the "install → MCP installed" guarantee. Asked once per engine, on an interactive
		// terminal only, and skipped for the stdio subcommands above (a prompt on a protocol stream
		// is a hang, not a question). See mcp_firstrun.go for why it prompts rather than just doing it.
		maybeOfferMCPConnect()
	}
	if len(os.Args) > 1 {
		arg := os.Args[1]
		switch arg {
		case "help", "-h", "--help":
			helpMain()
			return
		case "login":
			loginMain()
			return
		case "logout":
			logoutMain()
			return
		case "sessions":
			sessionsMain()
			return
		case "whoami":
			whoamiMain()
			return
		case "team", "org": // "org" kept as a back-compat alias
			teamMain(os.Args[2:])
			return
		case "skill", "skills": // org skill library — push/list/pull/install Agent Skills
			skillMain(os.Args[2:])
			return
		case "join":
			joinMain(os.Args[2:])
			return
		case "party":
			// Mode 2: bring an agent into a Party (humans + agents channel). Always-on
			// runner that wakes an episodic agent (claude) when it's addressed.
			partyMain(os.Args[2:])
			return
		case "cg-mcp":
			// Hidden (machine-invoked): a stdio MCP server exposing a Common Ground thread's
			// shared-context feed (recall/remember/read_context) to an AI engine. The mux wires
			// it at spawn (thread + identity via env). Not meant to be run by hand.
			cgMCPMain(os.Args[2:])
			return
		case "party-mcp":
			// Hidden (machine-invoked): a stdio MCP server exposing the party channel
			// to an AI engine as tools (Epic B). The runner spawns it inside the engine
			// via that engine's MCP config — not meant to be run by hand.
			partyMCPMain(os.Args[2:])
			return
		case "join-mcp":
			// Register the party MCP server into your OWN already-running LLM session
			// (from a join link), so the session you're working in can join the party.
			joinMCPMain(os.Args[2:])
			return
		case "evidence-spike":
			// Hidden/dev: the evidence-gate harness — research → cited position → re-fetch
			// → verify → drop. Run from a repo to gut-check grounded-party behavior.
			evidenceSpikeMain(os.Args[2:])
			return
		case "new":
			// Start a FRESH AI session in the session manager — top-level (the front door IS
			// the manager). Same as `ptln llms new`. e.g. `ptln new claude --thread <id>`.
			llmsNew(os.Args[2:])
			return
		case "thread":
			// Common Ground: shared context across people/machines/engines. A thread is a
			// team-scoped, private-by-default feed of seam facts. See docs/COMMON-GROUND.md.
			threadMain(os.Args[2:])
			return
		case "wt", "worktree":
			// Session worktrees: list/remove the isolated dirs `ptln new --worktree` creates.
			wtMain(os.Args[2:])
			return
		case "keepgoing":
			// E4.0 — the safe auto-continue: `ptln keepgoing status|off`. Arm it at launch
			// with `ptln new claude --keep-going N`.
			keepgoingMain(os.Args[2:])
			return
		case "work":
			// E4.1 — the worker atom: one bounded, sandboxed (worktree), tool-scoped,
			// thread-wired autonomous task run. Leaves a reviewable branch.
			workMain(os.Args[2:])
			return
		case "plan", "shape", "describe": // "plan" is the name (the Planning agent, #576); shape/describe stay compat aliases
			// Requirements agent — conversational, SCORED task authoring that runs your local
			// claude and enqueues a well-specified Backlog card. `ptln plan` in a project dir.
			describeMain(os.Args[2:])
			return
		case "crank":
			// E4.8 — the worklist loop: drive a backlog one task at a time, each in its own
			// worktree, sharing one thread; halts on cap/failures. Prepares branches to review.
			crankMain(os.Args[2:])
			return
		case "man":
			// Show the embedded man page (formatted via mandoc/nroff, or --raw for the source).
			manMain(os.Args[2:])
			return
		case "keepgoing-hook":
			// Hidden: the Claude Stop hook `--keep-going` installs. Reads the hook payload on
			// stdin and prints a continuation decision. Not for humans.
			keepgoingHookMain(os.Args[2:])
			return
		case "trigger", "triggers":
			triggerCmd(os.Args[2:])
			return
		// #829 — every settings surface has a CLI path, so an agent can configure the whole
		// account without a human clicking through the web app.
		case "template", "templates":
			templateCmd(os.Args[2:])
			return
		case "webhook", "webhooks":
			webhookCmd(os.Args[2:])
			return
		case "chat":
			// Connect Telegram / Discord to this account, so you can reach your projects from the
			// chat app you already have open. See docs/epics/chat-transports.md.
			chatCmd(os.Args[2:])
			return
		case "key", "keys":
			keyCmd(os.Args[2:])
			return
		case "me", "profile":
			meCmd(os.Args[2:])
			return
		case "notify", "notifications":
			notifyCmd(os.Args[2:])
			return
		case "project":
			// Common Ground: a project is the durable substrate (a repo/component's canon);
			// threads graduate facts into it. See docs/COMMON-GROUND.md §3.
			projectMain(os.Args[2:])
			return
		case "settings":
			// #577: the master index of every settings surface — state + where to change it.
			settingsMain(os.Args[2:])
			return
		case "daemon":
			// Epic R remote-launch (MVP, in progress): register projects the web may launch
			// agents into, on this machine. See docs/DAEMON-MVP.md. Reference-not-command.
			daemonMain(os.Args[2:])
			return
		case "welcome":
			// The front-door welcome screen (also the switchboard's empty state) — the big
			// wordmark plus a few doors: resume / new / share / plan / find.
			welcomeMain(os.Args[2:])
			return
		case "llms":
			// Local cross-tool launcher for your AI CLI sessions (claude/codex/gemini/llm):
			// browse, open several into one multiplexed terminal, and switch between them.
			llmsMain(os.Args[2:])
			return
		case "summon":
			// EXPERIMENTAL (proto/summon-agents): spawn an AI agent — resumed from
			// your llms inventory or fresh — into a watchable, E2EE partyline session.
			summonMain(os.Args[2:])
			return
		case "start":
			// Host a shared shell — the live-host path (POSTs /api/v1/sessions as the
			// logged-in user, like the web launchpad). This is now the ONLY entry to the
			// shared shell: bare `ptln` opens the session manager (below), so hosting a
			// shell is always explicit. Drop the verb so shellMain's flag parser sees
			// only flags (e.g. `partyline start --invite-only -- vim`).
			os.Args = append(os.Args[:1], os.Args[2:]...)
			shellMain()
			return
		case "version", "--version":
			versionMain() // prints version + a synchronous up-to-date / behind check
			return
		case "state":
			stateMain() // one JSON snapshot of this machine (account + daemon + live sessions)
			return
		case "peer":
			// ask_peer's answering edge: approve/decline a teammate's queued question via this
			// machine's daemon. What ptln-tray shells so it never needs a token or a socket.
			peerMain(os.Args[2:])
			return
		case "scribe":
			// Mode-4 context capture: distill this session's engine jsonl into durable thread
			// facts. Manual trigger for now (the end-to-end harness); the cadence automates it.
			scribeMain(os.Args[2:])
			return
		case "tray":
			trayMain(os.Args[2:]) // macOS menu bar companion: on | off | status
			return
		case "upgrade", "update":
			upgradeMain() // brew upgrade (mac) or re-run the installer (linux/curl)
			return
		}
		// Bare `--` historically meant "host a shared shell with the rest as the command";
		// that's now explicit as `ptln start … -- <cmd>`, so point there.
		if arg == "--" {
			fmt.Fprintf(os.Stderr, "ptln: to host a shared shell, run `ptln start %s`\n", strings.Join(os.Args[1:], " "))
			os.Exit(2)
		}
		// The front door IS the session manager, so its flags (e.g. --resume / --restore)
		// work at the top level — `ptln --resume`, not just `ptln llms --resume`.
		if strings.HasPrefix(arg, "-") {
			llmsMain(os.Args[1:])
			return
		}
		fmt.Fprintf(os.Stderr, "ptln: unknown command %q\n\n", arg)
		fmt.Fprintln(os.Stderr, "commands: start, new, join, party, chat, llms, thread, project, trigger, work, plan, crank, wt, daemon, peer, org, skill, settings, state, tray, sessions, login, logout, whoami, man, upgrade, version, help")
		fmt.Fprintln(os.Stderr, "run `ptln help` for usage")
		os.Exit(2)
	}
	// Bare `ptln` → the session manager (the front door): browse + run + switch your
	// AI CLI sessions. Not an interactive terminal (piped / CI) → print help rather
	// than launching the full-screen TUI into a non-tty.
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		helpMain()
		return
	}
	llmsMain(nil)
}
