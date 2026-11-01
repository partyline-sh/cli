package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"partyline.sh/partyline/internal/clispec"
	eng "partyline.sh/partyline/internal/engine"
	"partyline.sh/partyline/internal/gitwt"
)

// launcherEngineBin resolves a tool name (as shown in the launcher) to its executable via
// the internal/engine registry. `agy` is accepted as an alias for antigravity. This is the
// one place new-session launching and the tool list agree on what to exec. "" = unknown.
func launcherEngineBin(tool string) string {
	name := strings.ToLower(tool)
	if name == "agy" {
		name = "antigravity"
	}
	spec, ok := eng.Lookup(name)
	if !ok {
		return ""
	}
	return spec.Bin
}

// isWireableEngine reports whether an executable (a child's argv[0]) is an AI engine we can wire
// Common Ground into by (re)launching it — vs. a plain shell, which can only record-as-you.
func isWireableEngine(bin string) bool {
	switch bin {
	case "claude", "codex", "gemini", "agy":
		return true
	}
	return false
}

// mcpManagedClaudeConfig reports whether a claude --mcp-config value is one partyline wrote —
// it names the context-threads server (current or legacy) or a catalog server. Anything else
// is the user's own flag and survives strip-and-rebuild untouched.
func mcpManagedClaudeConfig(val string) bool {
	return strings.Contains(val, `"partyline-context-threads"`) || strings.Contains(val, `"common-ground"`)
}

// stripSessionWiring removes every partyline-managed wiring flag from a launch argv — the
// claude --mcp-config/--append-system-prompt we wrote and codex -c mcp_servers.* overrides for
// managed names. Keeps --resume/--continue (the conversation) and any user-supplied flags.
func stripSessionWiring(argv []string) []string {
	out := make([]string, 0, len(argv))
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if a == "--append-system-prompt" {
			i++ // ours (the thread primer) — drop flag AND value
			continue
		}
		if a == "--mcp-config" && i+1 < len(argv) && mcpManagedClaudeConfig(argv[i+1]) {
			i++
			continue
		}
		if a == "-c" && i+1 < len(argv) && mcpManagedCodexOverride(argv[i+1]) {
			i++
			continue
		}
		out = append(out, a)
	}
	return out
}

// carryConversation makes a relaunched claude pick up the conversation it was in (best-effort
// --continue). codex/gemini have no per-launch equivalent — their argv passes through.
func carryConversation(bin string, argv []string) []string {
	if bin != "claude" {
		return argv
	}
	for _, a := range argv {
		if a == "--resume" || a == "--continue" {
			return argv
		}
	}
	return append(append([]string(nil), argv...), "--continue")
}

// prepareLaunchSpec is the last step before ANY session spec is launched — a resumed one, a reopened
// one, or one restored from the saved workspace.
//
// It does two things. It strips the wiring an older partyline wrote into the saved argv: sessions
// created while context threads existed still carry a --mcp-config for a server that is gone and a
// primer telling the agent to call tools that no longer exist, and `--resume` replays that argv
// verbatim. And it adds the memory hooks (withMemoryHooks), which used to ride only a fresh
// `ptln new` in a legacy project — so a resumed session, which is most sessions, got none.
//
// Idempotent: a spec that has been through here comes out unchanged.
func prepareLaunchSpec(spec Spec) Spec {
	if len(spec.Argv) == 0 {
		return spec
	}
	// Context threads were removed; a saved spec may still name one.
	spec.Thread, spec.ThreadLabel = "", ""
	bin := filepath.Base(spec.Argv[0])
	if isWireableEngine(bin) {
		spec.Argv, spec.Engine = wireSessionArgv(bin, spec.Argv, "", spec.MCPs)
	}
	spec.Argv = withMemoryHooks(bin, spec.Argv)
	return spec
}

// wireSessionArgv rebuilds a child's launch argv from desired state: the Common Ground thread
// (recall/remember + primer) plus the selected catalog MCP servers. Always strip-then-rebuild —
// never incremental — so toggles are idempotent and the argv can't accumulate stale wiring.
// The thread/engine ride the SPEC (→ the child's own env via childEnv), never a global —
// that's the fix for cross-session contamination. Returns the wired argv + engine name.
func wireSessionArgv(bin string, argv []string, _ string, _ []string) ([]string, string) {
	engine := bin
	if bin == "agy" {
		engine = "antigravity"
	}
	// Partyline no longer injects anything into a launch: the facts reach the engine through its
	// own memory file and, where it is wired, the per-turn hook. Stripping is KEPT because
	// sessions created before the context threads were removed still carry the --mcp-config and
	// -c overrides partyline wrote, and resuming one of those must not start a server that is
	// no longer there.
	return stripSessionWiring(argv), engine
}

// llmsNew starts a FRESH session of an engine in the mux — the new-session counterpart to
// resuming. `ptln llms new <engine> [dir] [--worktree <name>] [--keep-going N]`.
func llmsNew(args []string) {
	// `new` passes arguments after the engine to the engine, so the dispatcher leaves --help alone.
	// Before the engine there is nothing to pass it to, and "unknown tool --help" was the answer.
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		if spec, ok := clispec.Lookup("new"); ok {
			clispec.PrintHelp(os.Stdout, spec)
		}
		return
	}
	if err := requireTmux(); err != nil {
		fatal(err)
	}
	var pos []string
	thread, worktree, goal := "", "", ""
	noThread, withState := false, false
	keepGoing := 0
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--thread":
			// Context threads were removed. Accepted so an old alias or script does not break;
			// the value is dropped, and said so, rather than silently honoured.
			if i++; i < len(args) {
				fmt.Fprintln(os.Stderr, "ptln: --thread is ignored — context threads were removed; this repo's facts in memory/ reach the session instead")
			}
			continue
		case "--worktree", "--wt":
			if i++; i < len(args) {
				worktree = strings.TrimSpace(args[i])
			}
			continue
		case "--no-thread":
			noThread = true
			continue
		case "--with-state":
			withState = true
			continue
		case "--keep-going":
			if i++; i < len(args) {
				keepGoing, _ = strconv.Atoi(strings.TrimSpace(args[i]))
			}
			continue
		case "--goal":
			if i++; i < len(args) {
				goal = args[i]
			}
			continue
		}
		pos = append(pos, args[i])
	}
	if len(pos) == 0 {
		fatal(fmt.Errorf("usage: ptln new <claude|codex|gemini|opencode|goose|antigravity> [dir] [--worktree <name> [--with-state]] [--keep-going N] [--goal <text>]"))
	}
	dir, _ := os.Getwd()
	if len(pos) > 1 && strings.TrimSpace(pos[1]) != "" {
		if abs, err := filepath.Abs(pos[1]); err == nil {
			dir = abs
		}
	}
	spec, err := newSessionSpec(pos[0], dir, thread, worktree, goal, withState, noThread, keepGoing)
	if err != nil {
		fatal(err)
	}
	if err := runLLMSApp([]Spec{spec}); err != nil {
		fatal(err)
	}
}

// bypassFlagsFor is the engine's own skip-permissions mode, from the same table the share picker
// uses — one definition of what "bypass" means per engine, so a new engine added there gets the
// ctrl-n behaviour without a second edit.
func bypassFlagsFor(tool string) []string {
	for _, m := range permissionModes(tool) {
		if m.danger {
			return m.flags
		}
	}
	return nil
}

// quickNewEngine is what 'N' launches: PARTYLINE_ENGINE when it names a known engine, else
// claude. Env-driven so a codex-first person gets their engine on the same key.
func quickNewEngine() string {
	e := strings.TrimSpace(os.Getenv("PARTYLINE_ENGINE"))
	switch e {
	case "claude", "codex", "gemini", "antigravity":
		return e
	}
	return "claude"
}

// newSessionSpec builds the launch Spec for a fresh AI session — the ONE path shared by
// `ptln new` (CLI) and the ctrl-\ n New/Run menu, so both start sessions identically:
// resolve the engine, optionally create+seed a worktree (with optional with-state fork),
// inherit the repo's bound thread (E3.5), wire the thread MCP + primer, and arm keep-going.
// Side effects (worktree, keep-going state) are intentional. Returns an error instead of
// exiting so the menu can surface it inline. Progress notes go to stderr (harmless in the mux).
func newSessionSpec(tool, dir, thread, worktree, goal string, withState, noThread bool, keepGoing int, permFlags ...string) (Spec, error) {
	bin := launcherEngineBin(tool)
	if bin == "" {
		return Spec{}, fmt.Errorf("unknown tool %q — try: claude, codex, gemini, opencode, goose, antigravity", tool)
	}
	if _, err := exec.LookPath(bin); err != nil {
		return Spec{}, fmt.Errorf("%s not found on PATH — is it installed?", bin)
	}
	label := bin
	if worktree != "" {
		repo, err := gitwt.RepoRoot(dir)
		if err != nil {
			return Spec{}, fmt.Errorf("--worktree needs a git repository: %w", err)
		}
		wtPath, branch, err := gitwt.Create(repo, worktree)
		if err != nil {
			return Spec{}, err
		}
		_ = gitwt.SeedInclude(repo, wtPath)
		if withState {
			if err := gitwt.MaterializeWIP(dir, wtPath); err != nil {
				return Spec{}, fmt.Errorf("--with-state: %w", err)
			}
		}
		fmt.Fprintf(os.Stderr, "⎇ worktree %s (branch %s)\n", wtPath, branch)
		dir = wtPath
		label = bin + " ⎇" + branch
	}
	argv := []string{bin}
	// E4.0 keep-going: a Stop hook auto-continues up to N turns / until the done sentinel. Hard-capped.
	if keepGoing > 0 {
		if bin != "claude" {
			fmt.Fprintf(os.Stderr, "note: --keep-going is claude-only for now — ignored for %s.\n", bin)
		} else if key, err := armKeepGoing(keepGoing, goal); err == nil {
			argv = append(argv, "--settings", keepGoingSettings(key))
			fmt.Fprintf(os.Stderr, "⏩ keep-going armed: up to %d continuations\n", keepGoing)
		}
	}
	// The memory hooks ride every claude session in a git repo. Keep-going's --settings already
	// includes them, and withMemoryHooks leaves an argv that has one alone.
	argv = withMemoryHooks(bin, argv)
	// Permission flags go LAST: they are the operator's explicit word on this launch and must
	// win over anything the wiring appended.
	argv = append(argv, permFlags...)
	return Spec{
		Label: label,
		Key:   fmt.Sprintf("new-%s-%d", bin, time.Now().UnixNano()),
		Argv:  argv,
		Dir:   dir,
	}, nil
}

// firstLauncherRun reports whether this is the launcher's very first open on this machine —
// and stamps the marker so it's true exactly once (E6.3: the welcome footer). Best-effort:
// any filesystem trouble reads as "not first" so onboarding can never break the launcher.
func firstLauncherRun() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	p := filepath.Join(home, ".partyline", "onboarded")
	if _, err := os.Stat(p); err == nil {
		return false
	}
	if os.MkdirAll(filepath.Dir(p), 0o700) != nil {
		return false
	}
	return os.WriteFile(p, []byte("1\n"), 0o600) == nil
}

// runLLMSApp opens the launcher or sessions on the tmux backend.
func runLLMSApp(initial []Spec) error {
	if err := requireTmux(); err != nil {
		return err
	}
	if os.Getenv("TMUX") != "" && !insidePtlnTmux() {
		return fmt.Errorf("already inside another tmux session — detach first (prefix d), or run ptln in a terminal outside it")
	}
	if len(initial) == 0 {
		launcherSearch, welcomeWantSearch = welcomeWantSearch, false
		if insidePtlnTmux() {
			return tmuxHome()
		}
		return runTmuxBare()
	}
	return runTmuxApp(initial)
}

// mcpManagedCodexOverride reports whether a codex `-c` override is one partyline wrote. Kept
// after the context threads were removed so that resuming a session created before the teardown
// strips the override instead of pointing codex at an MCP server that no longer exists.
func mcpManagedCodexOverride(val string) bool {
	if !strings.HasPrefix(val, "mcp_servers.") {
		return false
	}
	name := strings.TrimPrefix(val, "mcp_servers.")
	if i := strings.IndexByte(name, '.'); i >= 0 {
		name = name[:i]
	}
	return name == "partyline-context-threads" || name == "common-ground"
}
