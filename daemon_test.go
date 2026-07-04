package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"partyline.sh/partyline/internal/api"
)

// resolveLaunch is the security chokepoint: a reference (a label) only becomes a command by
// EXACT match against the local registry. These tests pin that — injection/unknown labels must
// fail to resolve, and a valid one yields a fixed argv in the registered dir.
func TestResolveLaunch(t *testing.T) {
	tmp := t.TempDir() // a real dir so the existence check passes for valid cases
	reg := daemonRegistry{Projects: []daemonProject{
		{Label: "project-a", Path: tmp, Preset: "spec"},
		{Label: "casual", Path: tmp, Preset: "chat"},
	}}
	link := "https://partyline.sh/p/abc#t=plt_pty_x"

	// unknown / injection labels never resolve (exact-match only)
	for _, bad := range []string{"nope", "../../etc", "project-a/../casual", "PROJECT-A", ""} {
		if _, _, err := resolveLaunch(reg, launchRef{ProjectLabel: bad, PartyLink: link}); err == nil {
			t.Errorf("expected error for label %q, got none", bad)
		}
	}

	// a non-link party reference is rejected
	if _, _, err := resolveLaunch(reg, launchRef{ProjectLabel: "project-a", PartyLink: "rm -rf /"}); err == nil {
		t.Error("expected error for non-http party link")
	}

	// valid spec project → grounded (--evidence), read-only tools, in the registered dir
	argv, dir, err := resolveLaunch(reg, launchRef{ProjectLabel: "project-a", PartyLink: link})
	if err != nil {
		t.Fatalf("valid resolve failed: %v", err)
	}
	if dir != tmp {
		t.Errorf("dir = %q, want %q", dir, tmp)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{"party", link, "--name project-a", "--evidence", "--allowedTools Read,Grep,Glob"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv missing %q (got: %s)", want, joined)
		}
	}

	// chat preset → no --evidence (not grounded)
	argv, _, err = resolveLaunch(reg, launchRef{ProjectLabel: "casual", PartyLink: link})
	if err != nil {
		t.Fatalf("chat resolve failed: %v", err)
	}
	if strings.Contains(strings.Join(argv, " "), "--evidence") {
		t.Error("chat preset should not be grounded")
	}
}

// TestCycleJoinable pins the S2 [P] availability cycle: a project advances
// off → joinable(ask) → joinable(auto) → off, persisting to the local registry, and a
// basename-label collision (two dirs, same basename) is refused rather than silently
// clobbering the existing label→path mapping. Isolated via $HOME so it never touches the
// real ~/.partyline registry.
func TestCycleJoinable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()

	// off → ask
	if st, _ := cycleJoinable(dir); st != "ask" {
		t.Fatalf("off→ask: got %q", st)
	}
	if j := loadJoinable(); j[dir].launchPolicy() != "ask" || j[dir].Label != filepath.Base(dir) {
		t.Fatalf("registry after ask: %+v", j[dir])
	}
	// ask → auto
	if st, _ := cycleJoinable(dir); st != "auto" {
		t.Fatalf("ask→auto: got %q", st)
	}
	if loadJoinable()[dir].launchPolicy() != "auto" {
		t.Fatalf("policy not auto: %+v", loadJoinable()[dir])
	}
	// auto → off (unregistered)
	if st, _ := cycleJoinable(dir); st != "off" {
		t.Fatalf("auto→off: got %q", st)
	}
	if _, ok := loadJoinable()[dir]; ok {
		t.Fatalf("still registered after off")
	}

	// basename collision: a different dir whose basename matches an existing label is refused.
	base := filepath.Base(dir)
	other := filepath.Join(t.TempDir(), base)
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if st, _ := cycleJoinable(dir); st != "ask" { // re-register dir
		t.Fatalf("re-register: %q", st)
	}
	st, flash := cycleJoinable(other)
	if st != "off" || flash == "" {
		t.Fatalf("expected collision refusal, got state=%q flash=%q", st, flash)
	}
	if _, ok := loadJoinable()[other]; ok {
		t.Fatalf("collision should not have registered %q", other)
	}
}

// TestServiceUnit pins the S4 always-on service logic without any side effects: install must
// gate on device enrolment (so it never shells out to launchctl/systemctl unenrolled), the
// unit path is OS-appropriate, and the generated unit references this binary + `daemon run`.
// $HOME-isolated — never touches the real ~/.partyline or the user's LaunchAgents.
func TestServiceUnit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// No device enrolled → install refuses up front (before any OS call).
	if _, err := installService(); err == nil || !strings.Contains(err.Error(), "enrolled") {
		t.Fatalf("install without enrolment should fail with an enrol hint, got %v", err)
	}
	if serviceInstalled() {
		t.Fatalf("serviceInstalled should be false with a fresh HOME")
	}

	switch runtime.GOOS {
	case "darwin", "linux":
		if serviceUnitPath() == "" {
			t.Fatalf("serviceUnitPath empty on %s", runtime.GOOS)
		}
		unit := launchdPlist()
		if runtime.GOOS == "linux" {
			unit = systemdUnit()
		}
		for _, want := range []string{selfExe(), "daemon", "run"} {
			if !strings.Contains(unit, want) {
				t.Fatalf("unit missing %q:\n%s", want, unit)
			}
		}
	}
}

// runRefFromEvent is the mapping seam between the stream RunEvent and the resolver (O.2). This
// pins that a RunEvent's fields reach resolveRun intact — a valid event resolves to a fixed
// crank argv in the registered dir, and preset selection (build ⇒ Bash) survives the hop.
func TestRunRefFromEventResolves(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	reg := daemonRegistry{Projects: []daemonProject{{Label: "proj", Path: tmp, Preset: "spec"}}}
	ev := api.RunEvent{RunID: "r1", ProjectLabel: "proj", ThreadID: "plt-thr-1", Tasks: []string{"do x"}, Preset: "build"}

	argv, dir, err := resolveRun(reg, runRefFromEvent(ev))
	if err != nil {
		t.Fatalf("valid run event should resolve: %v", err)
	}
	if dir != tmp {
		t.Errorf("dir = %q, want registry path %q (never event-supplied)", dir, tmp)
	}
	if len(argv) < 5 || argv[0] != "crank" || argv[1] != "--file" || argv[3] != "--thread" || argv[4] != "plt-thr-1" {
		t.Fatalf("unexpected argv: %v", argv)
	}
	hasBash := false
	for _, a := range argv {
		if a == "--allow-bash" {
			hasBash = true
		}
	}
	if !hasBash {
		t.Errorf("preset build should grant Bash, got %v", argv)
	}
}

// TestExecuteRunRefusesUnknownLabel pins that a run whose label isn't in the LOCAL registry is
// refused (the reference-not-command chokepoint) — executeRun returns an error and no crank
// worklist/log is written. With a fresh HOME the registry is empty, so any label is unknown.
func TestExecuteRunRefusesUnknownLabel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	err := executeRun(daemonDevice{}, api.RunEvent{RunID: "r1", ProjectLabel: "nope", ThreadID: "plt-1", Tasks: []string{"x"}})
	if err == nil {
		t.Fatal("executeRun must refuse an unknown label")
	}
	if !strings.Contains(err.Error(), "resolve") {
		t.Errorf("expected a resolve refusal, got %v", err)
	}
	// No launch/log dir should have been created (nothing spawned).
	if _, statErr := os.Stat(filepath.Join(home, ".partyline", "daemon", "launches")); statErr == nil {
		t.Error("unknown-label run must not spawn (no launches dir expected)")
	}
}

// TestExecuteRunRejectsBadRunID pins that --run is a VALIDATED UUID appended after resolveRun:
// a run whose label + thread + tasks resolve cleanly is STILL refused (no spawn) when the run id
// isn't a plain UUID, so nothing that could smuggle a flag/path into crank's argv gets through.
func TestExecuteRunRejectsBadRunID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	proj := t.TempDir() // a real dir so resolveRun's existence check passes
	if err := saveDaemonRegistry(daemonRegistry{Projects: []daemonProject{
		{Label: "proj", Path: proj, Preset: "spec"},
	}}); err != nil {
		t.Fatal(err)
	}
	// Injection-shaped / non-UUID run ids must all be refused at the run-id gate.
	for _, bad := range []string{"not-a-uuid", "r1", "--allow-bash", "../etc", ""} {
		err := executeRun(daemonDevice{}, api.RunEvent{
			RunID: bad, ProjectLabel: "proj", ThreadID: "plt-thr-1", Tasks: []string{"do x"}, Preset: "spec",
		})
		if err == nil || !strings.Contains(err.Error(), "run id") {
			t.Errorf("run id %q: expected a run-id refusal, got %v", bad, err)
		}
	}
	// Nothing should have spawned (the reject is before startDetached).
	if _, statErr := os.Stat(filepath.Join(home, ".partyline", "daemon", "launches")); statErr == nil {
		t.Error("a bad run id must not spawn (no launches dir expected)")
	}
}
