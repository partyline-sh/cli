package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"partyline.sh/partyline/internal/ptymux"
)

// aiOpenInMux opens the given sessions (id or unique prefix) straight into the multiplexer
// — `ptln llms <id> [<id>...]`. Bare `ptln llms` opens the browser instead. This is the CLI
// shortcut; the interactive equivalent is selecting rows with space and pressing ⏎.
func aiOpenInMux(ids []string) {
	sessions := collectSessions()
	meta := loadLLMMeta()
	var specs []ptymux.Spec
	for _, q := range ids {
		s, ok := matchSession(sessions, q)
		if !ok {
			fatal(fmt.Errorf("ptln llms: no session matches %q (run `ptln llms` for ids)", q))
		}
		if s.resumeArgv == nil {
			fatal(fmt.Errorf("ptln llms: %s sessions aren't resumable (no recorded path)", s.Tool))
		}
		specs = append(specs, ptymux.Spec{Label: muxLabelFor(s, meta), Key: s.ID, Model: sessionModel(s), Argv: s.resumeArgv, Dir: s.resumeDir})
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
func saveWorkspace(specs []ptymux.Spec) {
	b, err := json.MarshalIndent(specs, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(workspacePath()), 0o700)
	_ = os.WriteFile(workspacePath(), b, 0o600)
}

func loadWorkspace() []ptymux.Spec {
	b, err := os.ReadFile(workspacePath())
	if err != nil {
		return nil
	}
	var specs []ptymux.Spec
	_ = json.Unmarshal(b, &specs)
	return specs
}

// resumeWorkspace reopens the sessions that were live when you last quit the launcher,
// each with the same permission flags (saved in the resume argv).
func resumeWorkspace() {
	specs := loadWorkspace()
	if len(specs) == 0 {
		fmt.Println("ptln: no saved workspace yet — open some sessions and quit, then `ptln llms --resume` brings them back.")
		return
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		fatal(fmt.Errorf("--resume needs a terminal"))
	}
	if err := runLLMSApp(specs); err != nil {
		fatal(fmt.Errorf("ptln llms --resume: %w", err))
	}
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
