package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/term"
)

// shellSpec opens a plain shell as a mux window, so terminals and AI sessions live in the
// same launcher (no Key → never dedupes; each is its own window). Defaults to the cwd.
func shellSpec() *Spec {
	cwd, _ := os.Getwd()
	return shellSpecIn(cwd)
}

// shellSpecIn is shellSpec starting in a chosen directory (the ctrl-\ n "blank terminal" door).
func shellSpecIn(dir string) *Spec {
	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/sh"
	}
	label := "terminal"
	if dir != "" {
		label = "terminal · " + filepath.Base(dir)
	}
	return &Spec{Label: label, Argv: []string{sh}, Dir: dir}
}

// aiOpenInMux opens the given sessions (id or unique prefix) straight into the multiplexer
// — `ptln llms <id> [<id>...]`. Bare `ptln llms` opens the browser instead. This is the CLI
// shortcut; the interactive equivalent is selecting rows with space and pressing ⏎.
func aiOpenInMux(ids []string) {
	if err := requireTmux(); err != nil {
		fatal(err)
	}
	sessions := collectSessions()
	meta := loadLLMMeta()
	var specs []Spec
	for _, q := range ids {
		s, ok := matchSession(sessions, q)
		if !ok {
			fatal(fmt.Errorf("ptln llms: no session matches %q (run `ptln llms` for ids)", q))
		}
		if s.resumeArgv == nil {
			fatal(fmt.Errorf("ptln llms: %s sessions aren't resumable (no recorded path)", s.Tool))
		}
		specs = append(specs, prepareLaunchSpec(Spec{Label: muxLabelFor(s, meta), Key: s.ID, Model: sessionModel(s), Argv: s.resumeArgv, Dir: s.resumeDir}))
	}
	if err := runLLMSApp(specs); err != nil {
		fatal(fmt.Errorf("ptln llms: %w", err))
	}
}

// ---- workspace save/restore (ptln llms --resume) ----

func workspacePath() string { return filepath.Join(stateDir(), "llms-workspace.json") }

// saveWorkspace records the sessions open in the mux (with their resume argv, which carries
// the chosen permission flags) so --resume can bring the same set back. Wired as the mux's
// BeforeQuit hook, fired once at quit before teardown.
//
// An empty set never overwrites a saved one: quitting the bare launcher (or after closing
// everything) must not wipe the workspace you'd want `--resume` to bring back. Otherwise
// opening `ptln llms`, poking around, and quitting would silently destroy your session set.
func saveWorkspace(specs []Spec) {
	if len(specs) == 0 {
		return
	}
	specs = workspaceSpecs(specs)
	b, err := json.MarshalIndent(specs, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(workspacePath()), 0o700)
	_ = os.WriteFile(workspacePath(), b, 0o600)
}

// workspaceSpecs is what actually gets written: the live tabs, PLUS any fork the engine spawned
// from one of them.
//
// The live tabs pass through untouched — a tab always reloads the conversation it was created for.
// Adoption only ever ADDS, so that guarantee is never traded away for this one. Both are needed:
// without the pin a name stops meaning one conversation, and without adoption a fork is stranded
// exactly as it was in the incident this fixes.
func workspaceSpecs(live []Spec) []Spec {
	forks := adoptForks(live)
	if len(forks) == 0 {
		return live
	}
	return append(append(make([]Spec, 0, len(live)+len(forks)), live...), forks...)
}

// workspaceSig identifies a workspace by its session keys, so a repaint or a label tweak
// doesn't rewrite the file but opening/closing/replacing a session does.
func workspaceSig(specs []Spec) string {
	keys := make([]string, 0, len(specs))
	for _, s := range specs {
		keys = append(keys, s.Key)
	}
	sort.Strings(keys)
	return strings.Join(keys, "\x00")
}

func loadWorkspace() []Spec {
	b, err := os.ReadFile(workspacePath())
	if err != nil {
		return nil
	}
	var specs []Spec
	_ = json.Unmarshal(b, &specs)
	// A saved spec replays its argv verbatim, including whatever an older partyline wrote into it,
	// so every one goes through the same preparation as a fresh launch.
	for i := range specs {
		specs[i] = prepareLaunchSpec(specs[i])
	}
	return specs
}

// resumeWorkspace reopens the sessions that were live when you last quit the launcher,
// each with the same permission flags (saved in the resume argv).
func resumeWorkspace() {
	if err := requireTmux(); err != nil {
		fatal(err)
	}
	specs := loadWorkspace()
	if len(specs) == 0 {
		fmt.Println("ptln: no saved workspace yet — open some sessions and quit, then `ptln llms --resume` brings them back.")
		return
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		fatal(fmt.Errorf("--resume needs a terminal"))
	}
	// A fork the engine's daemon still owns cannot be resumed — the engine refuses to resume a live
	// session. Dropping it quietly is precisely the failure this whole path exists to end: the
	// conversation stays running, invisible, and the human concludes their work is gone. So name it,
	// say where it is, and leave the rest of the set to open normally.
	specs = dropHeldForks(specs)
	if err := runLLMSApp(specs); err != nil {
		fatal(fmt.Errorf("ptln llms --resume: %w", err))
	}
}

// dropHeldForks removes workspace specs whose session a fork-owning engine daemon still holds
// — the engine refuses to resume a live session. Dropping one quietly is precisely the failure
// the resume path exists to end (the conversation stays running, invisible, and the human
// concludes their work is gone), so each dropped fork is named with where to reattach. Shared
// by every workspace-resume entry point (built-in mux and the tmux backend).
func dropHeldForks(specs []Spec) []Spec {
	held := heldForks(specs)
	if len(held) == 0 {
		return specs
	}
	keep := make([]Spec, 0, len(specs))
	blocked := make(map[string]bool, len(held))
	for _, f := range held {
		blocked[f.SessionID] = true
	}
	for _, sp := range specs {
		if !blocked[sp.Key] {
			keep = append(keep, sp)
		}
	}
	for _, f := range held {
		fmt.Printf("ptln: %s is still running under the engine's own daemon, so it can't be reopened here.\n", short8(f.SessionID))
		fmt.Printf("      It is a fork of a session in this workspace and your conversation may be in it.\n")
		fmt.Printf("      Attach with:  claude agents      (pick %s, cwd %s)\n", short8(f.SessionID), f.Cwd)
	}
	return keep
}

// matchSession resolves an id or unique prefix against the session inventory (mirrors
// aiResume's matching).
func matchSession(sessions []aiSession, q string) (aiSession, bool) {
	for _, s := range sessions {
		if s.ID == q {
			return s, true
		}
	}
	var hit aiSession
	n := 0
	for _, s := range sessions {
		if strings.HasPrefix(s.ID, q) {
			hit, n = s, n+1
		}
	}
	return hit, n == 1
}

func muxLabel(s aiSession) string {
	if s.Cwd != "" {
		return s.Tool + " · " + filepath.Base(s.Cwd)
	}
	id := s.ID
	if len(id) > 8 {
		id = id[:8]
	}
	return s.Tool + " · " + id
}

// muxLabelFor is a session's label in the mux status bar + picker: the user's rename if
// they set one in the launcher (so the picker matches what the browser shows), else the
// tool · project default.
func muxLabelFor(s aiSession, meta map[string]sessMeta) string {
	if nm := strings.TrimSpace(meta[s.ID].Name); nm != "" {
		return nm
	}
	return muxLabel(s)
}
