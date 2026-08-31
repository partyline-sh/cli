package main

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"partyline.sh/partyline/internal/brand"
	"partyline.sh/partyline/internal/ptymux"
)

// A step walks pending → running → done, and only one is ever running at a time.
func TestBootStepLifecycle(t *testing.T) {
	m := newBootModel()
	if got := len(m.Steps); got != 0 {
		t.Fatalf("fresh model has %d steps, want 0", got)
	}
	m.Start("finding your sessions", 10*time.Millisecond)
	if m.Steps[0].State != bootRunning {
		t.Fatalf("started step state = %v, want running", m.Steps[0].State)
	}
	if m.Steps[0].Elapsed != 0 {
		t.Fatalf("a running step must not claim an elapsed time yet, got %v", m.Steps[0].Elapsed)
	}
	m.Detail("41 found")
	if m.Steps[0].Detail != "41 found" {
		t.Fatalf("Detail = %q", m.Steps[0].Detail)
	}

	// Starting the next step finishes the previous one.
	m.Start("reading history", 60*time.Millisecond)
	if m.Steps[0].State != bootDone {
		t.Fatalf("previous step state = %v, want done", m.Steps[0].State)
	}
	if m.Steps[0].Elapsed != 50*time.Millisecond {
		t.Fatalf("step 0 elapsed = %v, want 50ms", m.Steps[0].Elapsed)
	}
	if m.Steps[1].State != bootRunning {
		t.Fatalf("step 1 state = %v, want running", m.Steps[1].State)
	}
	// The finished step is no longer the one Detail annotates.
	m.Detail("meta")
	if m.Steps[0].Detail != "41 found" {
		t.Fatalf("Detail leaked onto a finished step: %q", m.Steps[0].Detail)
	}

	m.Complete(80 * time.Millisecond)
	for i, s := range m.Steps {
		if s.State != bootDone {
			t.Fatalf("step %d state = %v after Complete, want done", i, s.State)
		}
	}
	if m.Complete(90 * time.Millisecond); m.Steps[1].Elapsed != 20*time.Millisecond {
		t.Fatalf("Complete is not idempotent: step 1 elapsed = %v, want 20ms", m.Steps[1].Elapsed)
	}
}

// A finished step shows how long it took, so a slow launch names its own slow part.
// The splash shows the wordmark and a bar, and NOT the step list. The steps are still tracked —
// the bar is built from them — but naming each one made the wait feel longer and the labels meant
// nothing to the person reading them.
func TestBootFrameShowsNoStepText(t *testing.T) {
	m := newBootModel()
	m.SetTotal(4)
	m.Start("finding your sessions", 0)
	m.Detail("41 found")
	m.Start("reading history", 1200*time.Millisecond)
	m.Elapsed = 1200 * time.Millisecond

	frame := renderBootFrame(*m, 80, 24)
	if frame == "" {
		t.Fatal("expected a frame past the threshold")
	}
	for _, leaked := range []string{"finding your sessions", "41 found", "reading history", "1.2s"} {
		if strings.Contains(frame, leaked) {
			t.Errorf("frame still narrates the launch (%q):\n%q", leaked, frame)
		}
	}
	if !strings.Contains(frame, "P \x1b") && !strings.Contains(frame, "PARTYLINE") {
		t.Errorf("frame is missing the wordmark:\n%q", frame)
	}
}

// The bar fills as steps complete, and never reads full while work remains — a bar sitting at 100%
// with the screen still up is what makes a launcher feel stuck.
func TestBootBarFillsWithProgress(t *testing.T) {
	m := newBootModel()
	m.SetTotal(4)
	m.Elapsed = time.Second

	empty := renderBootBar(*m, 20)
	if strings.Count(empty, "━") != 0 {
		t.Errorf("nothing done yet, bar should be empty: %q", empty)
	}

	m.Start("one", 0)
	m.Start("two", 0) // starting the second completes the first
	half := renderBootBar(*m, 20)
	filled := strings.Count(half, "━")
	if filled == 0 || filled >= 20 {
		t.Errorf("one of four done should be partly filled, got %d of 20: %q", filled, half)
	}

	m.Start("three", 0)
	m.Start("four", 0)
	m.Complete(0) // all four done
	full := renderBootBar(*m, 20)
	if strings.Count(full, "━") != 20 {
		t.Errorf("all steps done should fill the bar: %q", full)
	}
}

// Before the launch knows how many steps it has, the bar sweeps rather than filling from a
// denominator nobody has computed yet.
func TestBootBarSweepsWhenTotalUnknown(t *testing.T) {
	m := newBootModel()
	m.Elapsed = time.Second
	m.Start("something", 0)

	a := renderBootBar(*m, 24)
	m.Phase = 6
	b := renderBootBar(*m, 24)
	if a == b {
		t.Error("an indeterminate bar should move between phases")
	}
	if strings.Count(a, "━") == 0 {
		t.Errorf("sweep should paint a moving run: %q", a)
	}
}

// SetTotal only grows: restores are added after the fixed steps, and a bar that jumped backwards
// would read as a stall.
func TestBootTotalOnlyGrows(t *testing.T) {
	m := newBootModel()
	m.SetTotal(4)
	m.SetTotal(9)
	m.SetTotal(2)
	if m.Total != 9 {
		t.Errorf("Total should keep the largest value, got %d", m.Total)
	}
}

func TestBootElapsedFormats(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{0, ""},
		{-1, ""},
		{18 * time.Millisecond, "18ms"},
		{999 * time.Millisecond, "999ms"},
		{1500 * time.Millisecond, "1.5s"},
		{42 * time.Second, "42s"},
	} {
		if got := bootElapsed(tc.d); got != tc.want {
			t.Errorf("bootElapsed(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

// Nothing renders at all below the threshold — a fast launch must not flash a splash.
func TestBootRendersNothingBelowThreshold(t *testing.T) {
	m := newBootModel()
	m.Start("finding your sessions", 0)
	m.Detail("41 found")
	for _, e := range []time.Duration{0, 20 * time.Millisecond, bootThreshold - time.Millisecond} {
		m.Elapsed = e
		if got := renderBootFrame(*m, 80, 24); got != "" {
			t.Fatalf("rendered a frame at %v (threshold %v): %q", e, bootThreshold, got)
		}
	}
	m.Elapsed = bootThreshold
	if renderBootFrame(*m, 80, 24) == "" {
		t.Fatalf("rendered nothing at the threshold %v", bootThreshold)
	}
}

// The frame never exceeds the terminal it was given — not in rows, not in columns.
func TestBootFrameFitsWithinBounds(t *testing.T) {
	sizes := [][2]int{{80, 24}, {40, 8}, {24, 6}, {120, 40}, {30, 7}, {200, 60}}
	for _, sz := range sizes {
		cols, rows := sz[0], sz[1]
		m := newBootModel()
		at := time.Duration(0)
		for i, label := range []string{
			"loading your theme", "finding your sessions", "reading history",
			"preparing the launcher", "reopening cyberpunk-game (1 of 3)",
			"reopening a-rather-long-session-name-here (2 of 3)", "reopening partyline (3 of 3)",
		} {
			at += time.Duration(i+1) * 40 * time.Millisecond
			m.Start(label, at)
		}
		m.Detail("41 found")
		m.Elapsed = at

		frame := renderBootFrame(*m, cols, rows)
		if frame == "" {
			t.Fatalf("%dx%d: rendered nothing", cols, rows)
		}
		assertFrameFits(t, frame, cols, rows)
	}
}

// A frame is a sequence of ESC[row;colH moves, each followed by the text placed there. Check
// every placement lands inside the box — where it starts AND where its last glyph ends.
var cursorMove = regexp.MustCompile(`\x1b\[(\d+);(\d+)H`)

func assertFrameFits(t *testing.T, frame string, cols, rows int) {
	t.Helper()
	locs := cursorMove.FindAllStringSubmatchIndex(frame, -1)
	if len(locs) == 0 {
		t.Fatalf("%dx%d: frame places nothing: %q", cols, rows, frame)
	}
	for i, loc := range locs {
		row, _ := strconv.Atoi(frame[loc[2]:loc[3]])
		col, _ := strconv.Atoi(frame[loc[4]:loc[5]])
		if row < 1 || row > rows {
			t.Fatalf("%dx%d: frame writes at row %d", cols, rows, row)
		}
		if col < 1 || col > cols {
			t.Fatalf("%dx%d: frame writes at col %d", cols, rows, col)
		}
		endOfLine := len(frame)
		if i+1 < len(locs) {
			endOfLine = locs[i+1][0]
		}
		text := frame[loc[1]:endOfLine]
		if w := brand.VisWidth(text); col+w-1 > cols {
			t.Fatalf("%dx%d: line at col %d is %d wide, overflowing the right edge by %d: %q",
				cols, rows, col, w, col+w-1-cols, text)
		}
	}
}

// The switchboard door reports theme → sessions → metadata → launcher, in that order.
// A recorder stands in for the live splash, so no terminal is involved.
func TestSwitchboardBootReportsItsStepsInOrder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rec := newBootRecorder()
	llmsBoot(rec)

	want := []string{
		"loading your theme",
		"finding your sessions",
		"reading history",
		"preparing the launcher",
	}
	got := rec.Labels()
	if len(got) != len(want) {
		t.Fatalf("boot reported %d steps %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("step %d = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
	// The session step says what it found, in the user's terms.
	if d := rec.steps[1].Detail; !strings.HasSuffix(d, " found") {
		t.Fatalf("sessions step detail = %q, want a \"N found\" count", d)
	}
}

// The --resume door reports one step per session restored, in the order the mux reopens them.
// bootReportRestores is the wiring runLLMSApp installs for the initial specs a resume hands it;
// driving ptymux.SpawnProgress here is exactly what ptymux.New does, minus the pty.
func TestResumeReportsOneStepPerSessionRestored(t *testing.T) {
	specs := []ptymux.Spec{
		{Label: "cyberpunk-game", Key: "a"},
		{Label: "claude · partyline", Key: "b"},
		{Label: "", Key: "c"}, // an unlabelled spec must still get a step, not a blank one
	}
	rec := newBootRecorder()
	unhook := bootReportRestores(rec, specs)
	if ptymux.SpawnProgress == nil {
		t.Fatal("bootReportRestores did not hook ptymux.SpawnProgress")
	}
	for i, sp := range specs {
		ptymux.SpawnProgress(sp, i, len(specs))
	}
	unhook()
	if ptymux.SpawnProgress != nil {
		t.Fatal("the hook outlived the launch it was installed for")
	}

	want := []string{
		"reopening cyberpunk-game (1 of 3)",
		"reopening claude · partyline (2 of 3)",
		"reopening a session (3 of 3)",
	}
	got := rec.Labels()
	if len(got) != len(specs) {
		t.Fatalf("restored %d sessions but reported %d steps: %v", len(specs), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("step %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// One session reads as "reopening x", not "reopening x (1 of 1)".
func TestResumeSingleSessionDropsTheCount(t *testing.T) {
	rec := newBootRecorder()
	specs := []ptymux.Spec{{Label: "cyberpunk-game"}}
	unhook := bootReportRestores(rec, specs)
	ptymux.SpawnProgress(specs[0], 0, 1)
	unhook()
	if got := rec.Labels(); len(got) != 1 || got[0] != "reopening cyberpunk-game" {
		t.Fatalf("labels = %v", got)
	}
}

// No initial specs (a plain `ptln`) means no hook at all — the switchboard boot must not
// leave a stale reporter behind for the next launch in the same process.
func TestBootReportRestoresNoSpecsInstallsNothing(t *testing.T) {
	ptymux.SpawnProgress = nil
	unhook := bootReportRestores(newBootRecorder(), nil)
	if ptymux.SpawnProgress != nil {
		t.Fatal("hooked SpawnProgress with no specs to restore")
	}
	unhook()
}

// The splash is inert when stdout is not a terminal: the headless paths (`ptln llms ls`, a
// piped `ptln`) must print nothing new.
func TestBootSplashPaintsNothingWithoutATerminal(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "boot")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b := startBootSplash(f)
	b.Step("finding your sessions")
	b.Detail("41 found")
	b.Done()
	st, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() != 0 {
		t.Fatalf("wrote %d bytes to a non-terminal stdout, want none", st.Size())
	}
}
