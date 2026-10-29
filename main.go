// partyline — let's launch a shared shell.
// Default: your $SHELL, shared. Run whatever you want in it.
package main

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"

	"partyline.sh/partyline/internal/clispec"
	"partyline.sh/partyline/internal/obs"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	defer obs.Recover() // panics crash with a stack trace on stderr, as before
	// Anonymous, opt-out usage ping (throttled ≤1/day; see telemetry.go). Skipped for the hidden
	// stdio subcommands — they're spawned per-launch (share the install's id anyway) and must not
	// write to their protocol streams.
	maybeTelemetryPing()
	if len(os.Args) > 1 {
		arg := os.Args[1]
		// Uniform `ptln <cmd> --help` for every command, rendered from the registry in
		// internal/clispec. Handled HERE rather than inside each command's parser, because that is
		// exactly what went wrong: --help was never a case in work's argument loop, so it fell
		// through to "the rest is the task" and `ptln work --help` STARTED A WORKER.
		// PassThrough commands are exempt — `ptln new claude --help` is asking claude.
		if clispec.MaybeHelp(arg, os.Args[1:]) {
			return
		}
		switch arg {
		case "help", "-h", "--help":
			helpMain()
			return
		case "remote":
			remoteMain(os.Args[2:])
			return
		case "relay":
			relayMain(os.Args[2:])
			return
		case "new":
			// Start a FRESH AI session in the session manager — top-level (the front door IS
			// the manager). Same as `ptln llms new`. e.g. `ptln new claude --thread <id>`.
			llmsNew(os.Args[2:])
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
		case "man":
			// Show the embedded man page (formatted via mandoc/nroff, or --raw for the source).
			manMain(os.Args[2:])
			return
		case "memory-capture-hook":
			// Hidden: the Stop hook that records what a session learned. Never blocks the stop.
			memoryCaptureHookMain(os.Args[2:])
			return
		case "chat":
			// The room where the person and their running sessions talk. See chat_cmd.go.
			chatMain(os.Args[2:])
			return
		case "loop":
			// Cross-model review and, later, loops around the session you are talking to.
			// Hidden while it is validated. See loop_cmd.go and docs/plans/partyline-loops.md.
			loopMain(os.Args[2:])
			return
		case "_contact-sheet":
			contactSheetMain(os.Args[2:])
			return
		case "_contractor": // hidden; runs one contractor inside its window (contractor.go)
			if len(os.Args) > 2 {
				contractorMain(os.Args[2])
			}
			return
		case "_watcher": // hidden; the background watcher (watcher.go)
			watcherMain()
			return
		case "chat-hook":
			// Hidden: the Stop hook that hands chat messages to a session and posts its reply.
			chatHookMain()
			return
		case "keepgoing-hook":
			// Hidden: the Claude Stop hook `--keep-going` installs. Reads the hook payload on
			// stdin and prints a continuation decision. Not for humans.
			keepgoingHookMain(os.Args[2:])
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
		case "memory-hook":
			// UserPromptSubmit hook: print what the team recorded since this session last
			// looked. Hidden — it is wired into sessions, never typed.
			memoryHookMain()
			return
		case "memory":
			// Shared project memory: what the team has learned, as files in the project's
			// memory repo. See memory_store.go.
			memoryMain(os.Args[2:])
			return
		case "tmux":
			// The tmux backend's own entry points: the hidden hooks, popups and chords the
			// generated tmux.conf runs.
			tmuxCmdMain(os.Args[2:])
			return
		case "version", "--version":
			versionMain() // prints version + a synchronous up-to-date / behind check
			return
		case "theme":
			themeMain(os.Args[2:])
			return
		case "state":
			stateMain() // one JSON snapshot of this machine (account + daemon + live sessions)
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
		fmt.Fprintln(os.Stderr, "commands:")
		fmt.Fprintln(os.Stderr, clispec.WrapNames("  ", 92))
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
	if err := requireTmux(); err != nil {
		fatal(err)
	}
	// Attach outside tmux; inside partyline's server, open the popup on this client.
	if interactiveTTY() && (os.Getenv("TMUX") == "" || insidePtlnTmux()) {
		run := runTmuxBare
		if insidePtlnTmux() {
			run = tmuxHome
		}
		if err := run(); err != nil {
			fatal(err)
		}
		return
	}
	llmsMain(nil)
}
