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
	// The theme is a property of the INSTALL, not of the launcher. It used to be loaded only on
	// the launcher's boot path, so every other entry point — the board, the ctrl-\ menu, the
	// per-session menus — rendered with the default palette however the user had set it. The
	// board even called themed() on every frame, against a table that had never been loaded.
	//
	// Reading one small file is cheap enough to do unconditionally, including for the stdio
	// subcommands: it touches no protocol stream and cannot fail loudly.
	loadTheme()
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
			// EXPERIMENTAL (proto/tmux-backend): host sessions in tmux instead of the
			// built-in multiplexer — tmux's server-side grid makes tab-switch corruption
			// structurally impossible. Prototype for the mux-replacement decision.
			tmuxCmdMain(os.Args[2:])
			return
		case "version", "--version":
			versionMain() // prints version + a synchronous up-to-date / behind check
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
	llmsMain(nil)
}
