package main

import (
	tea "charm.land/bubbletea/v2"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureFake records commands and supplies a fixture that has drifted left.
func fixtureFake(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("PARTYLINE_TMUX_SOCKET", "ptln-test-fixture")
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	log := filepath.Join(dir, "commands")
	t.Setenv("FIXTURE_LOG", log)
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$FIXTURE_LOG"
if [ "$1" = -V ]; then echo "tmux 3.7c"; exit; fi
shift 2
if [ "$1" = -f ]; then shift 2; fi
case "$1" in
display-popup)
 if [ -n "$LOOPS_POPUP_RUN" ]; then
  for arg in "$@"; do
   case "$arg" in PARTYLINE_LOOPS_ACTION=*) printf '%s' "$LOOPS_POPUP_RUN" > "${arg#PARTYLINE_LOOPS_ACTION=}";; esac
  done
 fi;;
list-windows)
 if [ "$FIXTURE_ONLY" = 1 ]; then
  case "$*" in *window_id*) printf '@9\t__loops__\n';; *) printf '__loops__\n';; esac
  exit
 fi
 if [ "$FIXTURE_EMPTY" = 1 ]; then exit; fi
 case "$*" in
 *window_index*) printf '@9\t0\t__loops__\n@2\t1\tsession\n'; [ -n "$FIXTURE_UNTAGGED_LAST" ] && printf '@4\t2\t\n';;
 *) printf '@9\t__loops__\n@2\tsession\n'; [ -n "$FIXTURE_LEGACY" ] && printf '@5\t__launcher__\n';;
 esac;;
list-panes)
 case "$*" in
 *window_layout*) printf '@9\t\t0\t0\t%s\n@2\t\t0\t0\t%s\n' "$FIXTURE_SPEC" "$SESSION_SPEC";;
 *window_id*) printf '%%9\t@9\t__loops__\n';;
 *) printf '%%2\n';;
 esac;;
has-session) [ "$FIXTURE_EMPTY" != 1 ];;
new-session|new-window) [ -n "$FIXTURE_FAIL_NEW" ] && { echo "create window failed" >&2; exit 1; }; printf '@9\n';;
display-message)
 case "$*" in
 *pane_id*window_id*) printf '%s\n' "${FIXTURE_ACTIVE:-%9	@9}";;
 *"-t @9"*window_id*|*"-t %9"*window_id*) printf '@9\n';;
 *window_id*) printf '@2\n';;
 *"-t @9"*@ptln_key*|*"-t %9"*@ptln_key*) printf '__loops__\n';;
 *@ptln_key*) printf 'session\n';;
 esac;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return log
}

// fixtureEncoded uses the same persisted representation as tmux panes.
func fixtureEncoded(sp Spec) string {
	b, _ := json.Marshal(sp)
	return base64.StdEncoding.EncodeToString(b)
}

func TestFixture(t *testing.T) {
	t.Run("anchors", TestFixtureAnchors)
	t.Run("save-resume", TestFixtureSaveAndResume)
	t.Run("boot", TestFixtureBootSplash)
}

func TestFixtureAnchors(t *testing.T) {
	log := fixtureFake(t)
	if _, err := tmuxMerge([]Spec{{Key: "new", Argv: []string{"sleep", "30"}}}); err != nil {
		t.Fatal(err)
	}
	if err := menuPick(tmuxMenuItem{target: 'l'}, ""); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	if strings.Contains(string(b), "new-window -d -b") || strings.Contains(string(b), "move-window") {
		t.Fatal(string(b))
	}
	if !strings.Contains(string(b), "new-window -t ptln:") { // the colon: tmux 3.8 reads a bare session as window 0
		t.Fatal(string(b))
	}
}

func TestFixtureSaveAndResume(t *testing.T) {
	log := fixtureFake(t)
	t.Setenv("FIXTURE_SPEC", fixtureEncoded(Spec{Key: legacyLoopsKey, Argv: []string{"ptln", "loop", "tab"}}))
	t.Setenv("SESSION_SPEC", fixtureEncoded(Spec{Key: "__chat__", Argv: []string{"ptln", "chat"}}))
	specs := tmuxWorkspaceSpecs()
	if len(specs) != 1 || specs[0].Key != "__chat__" {
		t.Fatalf("saved specs = %+v", specs)
	}
	t.Setenv("FIXTURE_EMPTY", "1")
	if _, err := tmuxShellWindow(); err != nil {
		t.Fatal(err)
	}
	specs = append(specs, Spec{Key: "__launcher__", Argv: []string{"old-launcher"}})
	if _, err := tmuxMerge(specs); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	if strings.Contains(string(b), "loop tab --fixture") || strings.Contains(string(b), "old-launcher") {
		t.Fatalf("restore commands:\n%s", b)
	}
}

// fixtureBoot records screen lifecycle boundaries without attaching to tmux.
type fixtureBoot struct{ events *[]string }

func (b fixtureBoot) Step(string)   { *b.events = append(*b.events, "paint") }
func (b fixtureBoot) Detail(string) {}
func (b fixtureBoot) Total(int)     {}
func (b fixtureBoot) Done()         { *b.events = append(*b.events, "leave-alt") }

func TestFixtureBootSplash(t *testing.T) {
	// The screen lifecycle is under test, not tmux: prepare and attach are stubbed, so the version
	// check is too. The release runner has no tmux, and this failed the v0.109.0 gate there.
	oldProbe := probeTmuxVersion
	t.Cleanup(func() { probeTmuxVersion = oldProbe })
	probeTmuxVersion = func() string { return "tmux 3.7" }
	var events []string
	oldBoot, oldPrep, oldAttach, oldWake := newBootDisplay, tmuxBarePrepare, tmuxBareAttach, tmuxBareWake
	t.Cleanup(func() {
		newBootDisplay, tmuxBarePrepare, tmuxBareAttach, tmuxBareWake = oldBoot, oldPrep, oldAttach, oldWake
	})
	newBootDisplay = func() bootReporter { return fixtureBoot{&events} }
	tmuxBareWake = func() { events = append(events, "wake") }
	tmuxBarePrepare = func() error { events = append(events, "ensure"); return nil }
	tmuxBareAttach = func() error { events = append(events, "attach"); return nil }
	if err := runTmuxBare(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(events, ","); got != "wake,paint,ensure,leave-alt,attach" {
		t.Fatal(got)
	}
}

func TestFixtureSplashTeardown(t *testing.T) {
	// The screen lifecycle is under test, not tmux: prepare and attach are stubbed, so the version
	// check is too. The release runner has no tmux, and this failed the v0.109.0 gate there.
	oldProbe := probeTmuxVersion
	t.Cleanup(func() { probeTmuxVersion = oldProbe })
	probeTmuxVersion = func() string { return "tmux 3.7" }
	out, err := os.CreateTemp(t.TempDir(), "screen")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	b := startBootSplash(out)
	oldBoot, oldPrep, oldAttach, oldWake := newBootDisplay, tmuxBarePrepare, tmuxBareAttach, tmuxBareWake
	t.Cleanup(func() {
		newBootDisplay, tmuxBarePrepare, tmuxBareAttach, tmuxBareWake = oldBoot, oldPrep, oldAttach, oldWake
	})
	newBootDisplay = func() bootReporter { return b }
	tmuxBareWake = func() {}
	tmuxBarePrepare = func() error {
		b.m.Elapsed = bootThreshold * 2
		frame := renderBootFrame(*b.m, 100, 30)
		if frame == "" {
			t.Fatal("no splash painted")
		}
		b.painted = true
		_, _ = out.WriteString("\x1b[?1049h" + frame)
		return nil
	}
	tmuxBareAttach = func() error {
		screen, _ := os.ReadFile(out.Name())
		if !strings.HasSuffix(string(screen), "\x1b[?1049l\x1b[?25h") {
			t.Fatal("attached before leaving alternate screen")
		}
		return nil
	}
	if err := runTmuxBare(); err != nil {
		t.Fatal(err)
	}
	screen, _ := os.ReadFile(out.Name())
	if !strings.HasPrefix(string(screen), "\x1b[?1049h") || !strings.HasSuffix(string(screen), "\x1b[?1049l\x1b[?25h") {
		t.Fatalf("screen lifecycle = %q", screen)
	}
}

// Both tagged fixtures left by older binaries are retired.
func TestFixtureRetiresTheOldLauncher(t *testing.T) {
	log := fixtureFake(t)
	t.Setenv("FIXTURE_EMPTY", "")
	t.Setenv("FIXTURE_LEGACY", "1")
	retireFixtureWindows()
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "kill-window -t @5") || !strings.Contains(string(b), "kill-window -t @9") {
		t.Errorf("the old launcher and Loops fixtures were not both closed:\n%s", b)
	}
	if strings.Contains(string(b), "kill-window -t @2") {
		t.Error("session closed")
	}
	if !isFixtureKey(legacyLauncherKey) || isFixtureKey("session") {
		t.Error("isFixtureKey")
	}
}

// The legacy flag no longer changes quit behaviour.
func TestFixtureTabDetachesFromItsArgv(t *testing.T) {
	m := newTabModel(loopTabActions(true, true))
	_, cmd := m.Update(launcherKey("q"))
	if cmd == nil {
		t.Fatal("q did not quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("legacy flag does not quit")
	}
}

// A session that fails to open is reported, not hidden behind the Loops tab; only "nothing to
// open" falls back to it.
func TestFixtureFallbackOnlyForNothingToOpen(t *testing.T) {
	if !errors.Is(errNothingToOpen, errNothingToOpen) || errors.Is(fmt.Errorf("open %q: %v", "x", "boom"), errNothingToOpen) {
		t.Fatal("errNothingToOpen")
	}
	fixtureFake(t)
	if _, err := tmuxMerge([]Spec{{Key: legacyLoopsKey, Argv: []string{"ptln", "loop", "tab"}}}); !errors.Is(err, errNothingToOpen) {
		t.Errorf("only the fixture to open: %v", err)
	}
}

// runTmuxApp with the fixture present reports a session that failed to open (round 2's P1).
func TestFixtureRunTmuxAppReportsAFailedOpen(t *testing.T) {
	fixtureFake(t)
	t.Setenv("FIXTURE_FAIL_NEW", "1")
	err := runTmuxApp([]Spec{{Label: "claude · x", Key: "s1", Argv: []string{"sleep", "30"}}})
	if err == nil || errors.Is(err, errNothingToOpen) {
		t.Fatalf("a failed open was hidden: %v", err)
	}
}

func TestBareStartsShell(t *testing.T) {
	log := fixtureFake(t)
	t.Setenv("FIXTURE_EMPTY", "1")
	if err := tmuxBarePrepare(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "new-session -d -P -F #{window_id} -s ptln -c") || strings.Contains(string(b), "loop tab") {
		t.Fatal(string(b))
	}
}

func TestBareStartsWorkspace(t *testing.T) {
	log := fixtureFake(t)
	t.Setenv("FIXTURE_EMPTY", "1")
	saveWorkspace([]Spec{{Label: "last", Key: "saved", Argv: []string{"sleep", "30"}, Dir: t.TempDir()}})
	if err := tmuxBarePrepare(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "-n last") || !strings.Contains(string(b), "sleep 30") {
		t.Fatal(string(b))
	}
}

func TestLoopsPopupAfterExit(t *testing.T) {
	log := fixtureFake(t)
	t.Setenv("LOOPS_POPUP_RUN", "selected-run")
	if err := runLoopsPopup(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	calls := string(b)
	board := strings.Index(calls, "☎ loops")
	gate := strings.Index(calls, "loop gate --run selected-run")
	if board < 0 || gate < board {
		t.Fatal(calls)
	}
	if !strings.Contains(calls, "-w 144 -h 45") {
		t.Fatal(calls)
	}
	for _, size := range [][2]int{{200, 60}, {120, 38}, {80, 24}} {
		w, h := launcherPopupSize(size[0], size[1])
		cmd := strings.Join(loopsPopupCommand("ptln", size[0], size[1]), " ")
		if !strings.Contains(cmd, fmt.Sprintf("-w %d -h %d", w, h)) {
			t.Fatal(cmd)
		}
	}
}

func TestBareExistingKeepsFocus(t *testing.T) {
	log := fixtureFake(t)
	if err := tmuxBarePrepare(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	for _, unwanted := range []string{"select-window", "last-window", "move-window", "new-window", "new-session"} {
		if strings.Contains(string(b), unwanted) {
			t.Fatal(string(b))
		}
	}
}

func TestEmptyResumeStartsShell(t *testing.T) {
	log := fixtureFake(t)
	t.Setenv("FIXTURE_EMPTY", "1")
	if err := runTmuxApp([]Spec{{Key: legacyLoopsKey, Argv: []string{"ptln", "loop", "tab"}}}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "new-session -d -P -F #{window_id} -s ptln -c") || !strings.Contains(string(b), "attach-session") {
		t.Fatal(string(b))
	}
}

func TestBareReplacesLastObsoleteFixtureWithShell(t *testing.T) {
	log := fixtureFake(t)
	t.Setenv("FIXTURE_ONLY", "1")
	if err := tmuxBarePrepare(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	calls := string(b)
	created, killed := strings.Index(calls, "new-window"), strings.Index(calls, "kill-window -t @9")
	if created < 0 || killed < created {
		t.Fatal(calls)
	}
}
