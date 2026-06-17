// partyline — let's launch a shared shell.
// Default: your $SHELL, shared. Run whatever you want in it.
package main

import (
	"fmt"
	"os"
	"strings"

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
		case "llms":
			// Local cross-tool index of your AI CLI sessions (claude/codex/gemini/llm):
			// `partyline llms` lists them, `partyline llms resume <id>` jumps back in.
			llmsMain(os.Args[2:])
			return
		case "mux":
			// Local LLM multiplexer (Epic M): host N AI CLI sessions in ONE terminal
			// and cycle between them (ctrl-\ n/p) instead of a tab per session.
			muxMain(os.Args[2:])
			return
		case "summon":
			// EXPERIMENTAL (proto/summon-agents): spawn an AI agent — resumed from
			// your llms inventory or fresh — into a watchable, E2EE partyline session.
			summonMain(os.Args[2:])
			return
		case "start":
			// Explicit verb for the default action: host a shared shell. This is the
			// live-host path — it POSTs /api/v1/sessions as the logged-in user, exactly
			// like the web launchpad. Bare `partyline` does the same; `start` just makes
			// it discoverable. Drop the verb so shellMain's flag parser sees only flags
			// (e.g. `partyline start --invite-only -- vim`).
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
		// Not a known command. A leading flag ("-x"/"--x") or "--" means "start the
		// shared shell with these args" (e.g. `partyline --invite-only`, `partyline
		// -- vim`); anything else is a typo'd command. Error instead of silently
		// starting a shell — a stray shell would bind the relay port and confuse.
		if arg != "--" && !strings.HasPrefix(arg, "-") {
			fmt.Fprintf(os.Stderr, "ptln: unknown command %q\n\n", arg)
			fmt.Fprintln(os.Stderr, "commands: start, login, logout, whoami, sessions, team, join, party, llms, upgrade, version, help")
			fmt.Fprintln(os.Stderr, "run `ptln help` for usage, or `ptln` to start a shared shell")
			os.Exit(2)
		}
	}
	shellMain()
}
