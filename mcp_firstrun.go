package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/term"
)

// First-run MCP connect (#557) — the "install → MCP installed" guarantee.
//
// THE PROBLEM. partyline's whole value in a session is recall/remember being THERE. Until now a new
// user had to find out that `ptln thread connect <engine>` exists, and run it once per engine they
// use. Most people never got there, so the product's central feature was invisible to them: they
// installed a thing that appeared to do nothing.
//
// THE PRODUCT CALL (the issue names it, and this takes its recommendation): a LOUD PROMPT, never a
// silent write. Registering an MCP server writes into ~/.claude, ~/.gemini and friends — files the
// user owns and did not ask us to touch. Doing that unannounced on first launch would be the exact
// behaviour that makes people distrust a CLI, and the one-line saving is not worth it.
//
// So: detect what is installed, say plainly what would be written where, ask once, remember the
// answer. Declining is remembered too — nagging is how a prompt becomes an annoyance to be silenced
// rather than a question to be answered.

// engineConnect names an engine, how to detect it, and where registering writes.
type engineConnect struct {
	key  string // as `ptln thread connect <key>` takes it
	bin  string // the executable to look for on PATH
	name string // how a human says it
	dest string // the file this would touch — shown in the prompt, because that is the thing being consented to
}

var connectableEngines = []engineConnect{
	{key: "claude", bin: "claude", name: "Claude Code", dest: "~/.claude.json"},
	{key: "codex", bin: "codex", name: "Codex", dest: "~/.codex/config.toml"},
	{key: "gemini", bin: "gemini", name: "Gemini CLI", dest: "~/.gemini/settings.json"},
	// antigravity has no direct mcp-add; `ptln thread connect` registers via claude then imports.
	{key: "antigravity", bin: "agy", name: "Antigravity", dest: "~/.claude.json + agy import"},
}

// mcpOfferState remembers what we have already asked about, per engine, so a second engine
// installed next month gets its own offer without re-asking about the ones already settled.
type mcpOfferState struct {
	// engine key → "connected" | "declined". Absence means "never asked".
	Answered map[string]string `json:"answered"`
}

func mcpOfferPath() string { return filepath.Join(stateDir(), "mcp-offer.json") }

func loadMCPOffer() mcpOfferState {
	var st mcpOfferState
	if b, err := os.ReadFile(mcpOfferPath()); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	if st.Answered == nil {
		st.Answered = map[string]string{}
	}
	return st
}

func saveMCPOffer(st mcpOfferState) {
	if b, err := json.MarshalIndent(st, "", "  "); err == nil {
		_ = os.WriteFile(mcpOfferPath(), append(b, '\n'), 0o600)
	}
}

// installedEngines is the subset of connectableEngines actually on this machine. Offering to wire
// an engine someone does not have is noise that teaches them to skip the prompt.
func installedEngines() []engineConnect {
	var out []engineConnect
	for _, e := range connectableEngines {
		if _, err := exec.LookPath(e.bin); err == nil {
			out = append(out, e)
		}
	}
	return out
}

// pendingEngines are installed engines we have never asked about.
func pendingEngines(st mcpOfferState) []engineConnect {
	var out []engineConnect
	for _, e := range installedEngines() {
		if st.Answered[e.key] == "" {
			out = append(out, e)
		}
	}
	return out
}

// maybeOfferMCPConnect runs at the front door. Silent and instant in every case except the one it
// exists for: an interactive terminal, a logged-in user, and at least one installed engine nobody
// has been asked about yet.
func maybeOfferMCPConnect() {
	// Never in CI, a pipe, or a spawned stdio server. A prompt nobody can answer is a hang.
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stderr.Fd())) {
		return
	}
	// Only at the FRONT DOOR. Someone who typed `ptln update` came to do one specific thing, and
	// interrupting it with an unrelated setup question is how a one-time prompt starts feeling like
	// nagging even when it only fires once. The session manager (bare `ptln`) and `ptln login` are
	// where a setup question belongs — you are already there to start working.
	if !frontDoorInvocation() {
		return
	}
	if os.Getenv("PARTYLINE_NO_CONNECT_PROMPT") != "" {
		return
	}
	st := loadMCPOffer()
	pending := pendingEngines(st)
	if len(pending) == 0 {
		return
	}

	dim := func(s string) string { return "\x1b[38;5;245m" + s + "\x1b[0m" }
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "☎ partyline can wire shared context (recall / remember) into your AI sessions.")
	fmt.Fprintln(os.Stderr, dim("  Found: "+engineList(pending)))
	// Name the files. This is the thing being consented to, and "we'd like to add an MCP server" is
	// not informed consent if the person cannot tell what it edits.
	for _, e := range pending {
		fmt.Fprintln(os.Stderr, dim(fmt.Sprintf("    %-14s writes %s", e.name, e.dest)))
	}
	fmt.Fprint(os.Stderr, "  Wire them up now? [Y/n] ")

	answer := strings.ToLower(strings.TrimSpace(readLine()))
	if answer != "" && !strings.HasPrefix(answer, "y") {
		// Remembered, so this is asked once and not once per launch. `ptln thread connect <engine>`
		// is still there when they change their mind.
		for _, e := range pending {
			st.Answered[e.key] = "declined"
		}
		saveMCPOffer(st)
		fmt.Fprintln(os.Stderr, dim("  Skipped. Change your mind: ptln thread connect <engine>"))
		return
	}

	for _, e := range pending {
		if err := connectEngineQuiet(e.key); err != nil {
			// RECORD THE FAILURE. Leaving it unanswered "so the next run can retry" sounds generous
			// and is how this became a prompt on EVERY ptln invocation: an engine whose connect can
			// never succeed is asked about forever, and the person is stuck answering a question
			// that does nothing. Reported once, remembered, and left to an explicit retry.
			fmt.Fprintf(os.Stderr, "  ✗ %s: %v\n", e.name, err)
			fmt.Fprintf(os.Stderr, "    %s\n", dimS("not asking again — retry with: ptln thread connect "+e.key))
			st.Answered[e.key] = outcomeFor(err)
			continue
		}
		st.Answered[e.key] = outcomeFor(nil)
		fmt.Fprintf(os.Stderr, "  ✓ %s\n", e.name)
	}
	saveMCPOffer(st)
	fmt.Fprintln(os.Stderr, dim("  Done. Open a session in a project and your agent can recall/remember."))
	fmt.Fprintln(os.Stderr)
}

func engineList(es []engineConnect) string {
	names := make([]string, 0, len(es))
	for _, e := range es {
		names = append(names, e.name)
	}
	return strings.Join(names, ", ")
}

func readLine() string {
	sc := bufio.NewScanner(os.Stdin)
	if sc.Scan() {
		return sc.Text()
	}
	return ""
}

// frontDoorInvocation reports whether this is a moment where a setup question belongs: opening the
// session manager, or having just logged in. Every other subcommand was typed to do something
// specific, and a prompt there is an interruption rather than an offer.
func frontDoorInvocation() bool {
	if len(os.Args) < 2 {
		return true // bare `ptln` — the session manager, the front door itself
	}
	switch os.Args[1] {
	case "login", "llms", "welcome":
		return true
	}
	// Bare flags go to the session manager too (`ptln --resume`).
	return strings.HasPrefix(os.Args[1], "-") && os.Args[1] != "-h" && os.Args[1] != "--help"
}

func dimS(s string) string { return "\x1b[38;5;245m" + s + "\x1b[0m" }

// outcomeFor is the recording rule, extracted so it can be tested as a RULE rather than as a
// helper nobody proves is called. Every terminal state must produce a non-empty answer: an empty
// one means "never asked", which is what turned a one-time offer into a prompt on every
// invocation.
func outcomeFor(err error) string {
	if err != nil {
		return "failed"
	}
	return "connected"
}
