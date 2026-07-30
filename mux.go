package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/term"

	"partyline.sh/partyline/internal/ptymux"
)

// shellSpec opens a plain shell as a mux window, so terminals and AI sessions live in the
// same launcher (no Key → never dedupes; each is its own window). Defaults to the cwd.
func shellSpec() *ptymux.Spec {
	cwd, _ := os.Getwd()
	return shellSpecIn(cwd)
}

// shellSpecIn is shellSpec starting in a chosen directory (the ctrl-\ n "blank terminal" door).
func shellSpecIn(dir string) *ptymux.Spec {
	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/sh"
	}
	label := "terminal"
	if dir != "" {
		label = "terminal · " + filepath.Base(dir)
	}
	return &ptymux.Spec{Label: label, Argv: []string{sh}, Dir: dir}
}

// shareClosure hosts a session over the E2EE relay (view-only) so someone can join and
// watch it live. Runs `partyline start -- <resume argv>` as a subprocess via the mux's
// suspend, so your OTHER open sessions stay put and you drop back into the launcher when
// the share ends. (Exec-handoff would orphan those other children — this doesn't.)
func shareClosure(s aiSession) func() {
	return func() {
		argv := append([]string{"start", "--"}, s.resumeArgv...)
		cmd := exec.Command(selfExe(), argv...)
		cmd.Dir = s.resumeDir
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		fmt.Println("\n☎ sharing this session — send the join link below; guests watch view-only. Exit the session (Ctrl-D / quit) to stop and drop everyone.")
		_ = cmd.Run()
	}
}

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
		specs = append(specs, inheritRepoBindSpec(ptymux.Spec{Label: muxLabelFor(s, meta), Key: s.ID, Model: sessionModel(s), Argv: s.resumeArgv, Dir: s.resumeDir}))
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
func saveWorkspace(specs []ptymux.Spec) {
	if len(specs) == 0 {
		return
	}
	b, err := json.MarshalIndent(specs, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(workspacePath()), 0o700)
	_ = os.WriteFile(workspacePath(), b, 0o600)
}

// workspaceWatchPoll is how often the live set is compared to what's on disk. Seconds, not
// milliseconds: this exists to survive a kill, and losing the last few seconds of tab changes
// is nothing next to losing the whole set.
const workspaceWatchPoll = 3 * time.Second

// startWorkspaceWatch keeps llms-workspace.json current while the mux runs.
//
// BeforeQuit alone is not enough, and the gap it leaves is how a session gets "lost": swap the
// binary, crash, or SIGKILL and the snapshot still describes the last CLEAN quit. Any session
// opened since then is absent, so `--resume` restores a tab under its old name with an OLDER
// session id — same label, different conversation, no memory of what you were doing. The user
// reads that as "my work is gone" when the transcript is fine and only the pointer is stale.
//
// Deliberately a poll over the live set rather than hooks on open/close: every way a child can
// appear or vanish (launcher, ctrl-\ n, a pane closing, an engine exiting on its own) shows up
// here for free, and a snapshot that misses one path is the bug all over again.
func startWorkspaceWatch(mx *ptymux.Mux) {
	if mx == nil {
		return
	}
	go func() {
		last := ""
		for {
			specs := mx.LiveSpecs()
			if sig := workspaceSig(specs); sig != last {
				saveWorkspace(specs) // no-ops on an empty set, so `last` stays put and we retry
				if len(specs) > 0 {
					last = sig
				}
			}
			time.Sleep(workspaceWatchPoll)
		}
	}()
}

// workspaceSig identifies a workspace by its session keys, so a repaint or a label tweak
// doesn't rewrite the file but opening/closing/replacing a session does.
func workspaceSig(specs []ptymux.Spec) string {
	keys := make([]string, 0, len(specs))
	for _, s := range specs {
		keys = append(keys, s.Key)
	}
	sort.Strings(keys)
	return strings.Join(keys, "\x00")
}

func loadWorkspace() []ptymux.Spec {
	b, err := os.ReadFile(workspacePath())
	if err != nil {
		return nil
	}
	var specs []ptymux.Spec
	_ = json.Unmarshal(b, &specs)
	// Reopened sessions must honor the repo bind like fresh ones (a saved spec from before the
	// bind existed — or from an older CLI — carries no thread; inherit it now, at restore).
	for i := range specs {
		specs[i] = inheritRepoBindSpec(specs[i])
	}
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
