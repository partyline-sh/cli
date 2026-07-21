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

	// valid spec project → read-only tools, in the registered dir. Grounding is NO LONGER decided
	// here: the daemon can't see the party mode, so it never forces --evidence off the preset
	// (that wrongly grounded describe/chat). Grounding is now a party-MODE decision the server
	// sends (party_agent.grounded); the launch argv must carry no --evidence.
	argv, dir, err := resolveLaunch(reg, launchRef{ProjectLabel: "project-a", PartyLink: link})
	if err != nil {
		t.Fatalf("valid resolve failed: %v", err)
	}
	if dir != tmp {
		t.Errorf("dir = %q, want %q", dir, tmp)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{"party", link, "--name project-a", "--allowedTools Read,Grep,Glob"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv missing %q (got: %s)", want, joined)
		}
	}
	if strings.Contains(joined, "--evidence") {
		t.Errorf("spec preset must NOT force --evidence at launch — grounding is mode-driven (got: %s)", joined)
	}

	// chat preset → also no --evidence (grounding isn't launch-decided)
	argv, _, err = resolveLaunch(reg, launchRef{ProjectLabel: "casual", PartyLink: link})
	if err != nil {
		t.Fatalf("chat resolve failed: %v", err)
	}
	if strings.Contains(strings.Join(argv, " "), "--evidence") {
		t.Error("chat preset should not be grounded")
	}
}

// TestResolveLaunchEnginePreference pins the engine pecking order: a VALID server-sent engine
// (the accepted event's `engine`) beats the local registry's per-project engine, which beats
// the claude default — and an unknown/injection-y server value never reaches the argv (it
// falls back to the local engine). The read-only posture rides for EVERY engine that can
// enforce one (R6): claude --allowedTools, codex --sandbox read-only, gemini --approval-mode
// plan; antigravity has no flag (headless deny-by-default is its enforcement).
func TestResolveLaunchEnginePreference(t *testing.T) {
	tmp := t.TempDir()
	reg := daemonRegistry{Projects: []daemonProject{
		{Label: "local-codex", Path: tmp, Engine: "codex"},
		{Label: "plain", Path: tmp},
	}}
	link := "https://partyline.sh/p/abc#t=plt_pty_x"

	cases := []struct {
		name, label, server string
		wantEngine          string // expected --engine value; "" = no --engine flag (claude)
	}{
		{"server overrides a different local engine", "local-codex", "gemini", "gemini"},
		{"server claude overrides local codex", "local-codex", "claude", ""},
		{"empty server keeps local", "local-codex", "", "codex"},
		{"empty server keeps claude default", "plain", "", ""},
		{"server engine over the claude default", "plain", "antigravity", "antigravity"},
		{"unknown server engine falls back to local", "local-codex", "evil; rm -rf /", "codex"},
		{"unknown server engine falls back to default", "plain", "../claude", ""},
	}
	for _, tc := range cases {
		argv, _, err := resolveLaunch(reg, launchRef{ProjectLabel: tc.label, PartyLink: link, Engine: tc.server})
		if err != nil {
			t.Fatalf("%s: resolve failed: %v", tc.name, err)
		}
		got := ""
		for i, a := range argv {
			if a == "--engine" && i+1 < len(argv) {
				got = argv[i+1]
			}
		}
		if got != tc.wantEngine {
			t.Errorf("%s: --engine = %q, want %q (argv: %v)", tc.name, got, tc.wantEngine, argv)
		}
		// R6: every engine's read-only tail (or antigravity's documented absence of one).
		joined := strings.Join(argv, " ")
		wantTail := map[string]string{
			"":            "--allowedTools Read,Grep,Glob",
			"codex":       "--sandbox read-only",
			"gemini":      "--approval-mode plan",
			"antigravity": "",
		}[tc.wantEngine]
		if wantTail == "" && tc.wantEngine == "antigravity" {
			for _, flag := range []string{"--allowedTools", "--sandbox", "--approval-mode", "--dangerously"} {
				if strings.Contains(joined, flag) {
					t.Errorf("%s: antigravity must get NO posture/bypass flags (argv: %v)", tc.name, argv)
				}
			}
		} else if !strings.Contains(joined, wantTail) {
			t.Errorf("%s: missing read-only tail %q (argv: %v)", tc.name, wantTail, argv)
		}
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

// TestAugmentRunArgvMaxTokens pins the #81-slice-3b budget flow event→argv: a valid run id
// yields --run <uuid>, and --max-tokens N is appended IFF the event carries a positive budget
// (0/absent ⇒ unbounded ⇒ omitted). This is what makes the slice-2 pause fire on the daemon path.
func TestAugmentRunArgvMaxTokens(t *testing.T) {
	const uuid = "11111111-2222-3333-4444-555555555555"
	base := []string{"crank", "--file", "/w/x.txt", "--thread", "plt-1"}

	// A positive budget → --max-tokens appears with the right value, after --run.
	got, err := augmentRunArgv(append([]string(nil), base...), api.RunEvent{RunID: uuid, MaxTokens: 250000})
	if err != nil {
		t.Fatalf("valid event should augment: %v", err)
	}
	if !argvHasFlagVal(got, "--run", uuid) {
		t.Errorf("expected --run %s in %v", uuid, got)
	}
	if !argvHasFlagVal(got, "--max-tokens", "250000") {
		t.Errorf("expected --max-tokens 250000 in %v", got)
	}
	// #81 slice 3c: --resume is always present so an approved (re-queued) run skips done tasks.
	if !argvHas(got, "--resume") {
		t.Errorf("expected --resume in %v", got)
	}

	// Zero / absent budget → NO --max-tokens (unbounded — the pre-slice-3b behaviour).
	for _, mt := range []int{0, -1} {
		got, err := augmentRunArgv(append([]string(nil), base...), api.RunEvent{RunID: uuid, MaxTokens: mt})
		if err != nil {
			t.Fatalf("MaxTokens=%d should still augment (just no ceiling): %v", mt, err)
		}
		for _, a := range got {
			if a == "--max-tokens" {
				t.Errorf("MaxTokens=%d must NOT emit --max-tokens, got %v", mt, got)
			}
		}
	}

	// A non-UUID run id is refused (nothing to append onto) — the run-id chokepoint.
	if _, err := augmentRunArgv(append([]string(nil), base...), api.RunEvent{RunID: "--allow-bash", MaxTokens: 100}); err == nil {
		t.Error("a non-UUID run id must be refused")
	}
}

// argvHas reports whether argv contains the given token anywhere.
func argvHas(argv []string, tok string) bool {
	for _, a := range argv {
		if a == tok {
			return true
		}
	}
	return false
}

// argvHasFlagVal reports whether argv contains `flag` immediately followed by `val`.
func argvHasFlagVal(argv []string, flag, val string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == val {
			return true
		}
	}
	return false
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

// allTasksDone is the evidence gate for reconciling an unwatched run (sweepOrphanRuns): a run whose
// exit watcher died with a restarted daemon is `done` ONLY when every reported task finished clean.
func TestAllTasksDone(t *testing.T) {
	mk := func(statuses ...string) []api.RunTaskStatus {
		out := make([]api.RunTaskStatus, len(statuses))
		for i, s := range statuses {
			out[i] = api.RunTaskStatus{Idx: i, Status: s}
		}
		return out
	}
	if allTasksDone(nil) || allTasksDone(mk()) {
		t.Fatal("no reported tasks must NOT read as completion (a crank that died silently must park)")
	}
	if !allTasksDone(mk("done")) || !allTasksDone(mk("done", "done")) {
		t.Fatal("all-done must read as completion")
	}
	for _, bad := range []string{"queued", "running", "failed", "blocked"} {
		if allTasksDone(mk("done", bad)) {
			t.Fatalf("a %s task must NOT read as completion", bad)
		}
	}
}
