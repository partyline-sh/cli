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
		case "join":
			joinMain(os.Args[2:])
			return
		case "party":
			// Mode 2: bring an agent into a Party (humans + agents channel). Always-on
			// runner that wakes an episodic agent (claude) when it's addressed.
			partyMain(os.Args[2:])
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
		case "daemon":
			// Epic R remote-launch (MVP, in progress): register projects the web may launch
			// agents into, on this machine. See docs/DAEMON-MVP.md. Reference-not-command.
			daemonMain(os.Args[2:])
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
		fmt.Fprintln(os.Stderr, "commands: start, login, logout, whoami, sessions, party, llms, daemon, team, join, upgrade, version, help")
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
