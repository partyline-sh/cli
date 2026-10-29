package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const goodBrief = `# CSV export
## Goal
Export tickets as CSV.
## Decisions
CSV, not XLSX: the users open it in Numbers.
## Constraints
The JSON API stays as it is.
## Out of scope
Scheduling.
## Acceptance
- run: test -f export.csv
- file: export.go
## Verification
The run line proves the file is produced.
## Unknowns
None.
`

func TestBriefNeedsEverySectionAndStructuredAcceptance(t *testing.T) {
	if err := validateBrief(goodBrief); err != nil {
		t.Fatalf("good brief refused: %v", err)
	}
	if err := validateBrief(strings.Replace(goodBrief, "## Out of scope\nScheduling.\n", "", 1)); err == nil || !strings.Contains(err.Error(), "Out of scope") {
		t.Errorf("missing section not named: %v", err)
	}
	if err := validateBrief(strings.Replace(goodBrief, "- run: test -f export.csv\n- file: export.go", "it works", 1)); err == nil {
		t.Error("acceptance with no run:/file: lines was accepted")
	}
	acc := briefAcceptance(goodBrief)
	if len(acc) != 2 || acc[0] != (loopAcceptance{"run", "test -f export.csv"}) || acc[1] != (loopAcceptance{"file", "export.go"}) {
		t.Errorf("acceptance = %+v", acc)
	}
}

func TestReadyReportNeedsEvidenceAndTheThreeAnswers(t *testing.T) {
	if err := validateReport("## Evidence\ngo test ./... passed\n"); err == nil || !strings.Contains(err.Error(), "Least confident") {
		t.Errorf("incomplete report accepted: %v", err)
	}
	full := "## Evidence\nx\n## Hardest decision\nx\n## Rejected alternatives\nx\n## Least confident\nx\n## What an excellent reviewer would criticise\nx\n"
	if err := validateReport(full); err != nil {
		t.Errorf("complete report refused: %v", err)
	}
}

// A check that already failed before the change is the repository's problem, not the run's.
func TestOnlyNewFailuresCount(t *testing.T) {
	base := []loopCheck{{Cmd: "lint", Exit: 1}, {Cmd: "test", Exit: 0}}
	now := []loopCheck{{Cmd: "lint", Exit: 1}, {Cmd: "test", Exit: 2}, {Cmd: "build", Exit: 0}}
	got := newFailures(now, base)
	if len(got) != 1 || got[0].Cmd != "test" {
		t.Errorf("new failures = %+v", got)
	}
}

func testLoopRun(t *testing.T, repo string) *loopRun {
	t.Helper()
	r := &loopRun{ID: newLoopID(), Repo: repo, Session: []string{"pane:%7"}, Checks: []string{"true"}, Review: "claude", Created: time.Now(), Revision: 1}
	if err := r.set(loopBuilding, "building", "started", nil); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(loopFile(r.ID, "brief-1.md"), []byte(goodBrief), 0o600)
	return r
}

// A message is delivered once, framed as claims, to the session the run belongs to only.
func TestInboxDeliversOnceToItsSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	r := testLoopRun(t, t.TempDir())
	if loopStopReason([]string{"pane:%7"}) != "" {
		t.Error("delivered with nothing queued")
	}
	_ = loopPost(r.ID, "P1 in export.go")
	if loopStopReason([]string{"pane:%99"}) != "" {
		t.Error("delivered to another session")
	}
	got := loopStopReason([]string{"pane:%7"})
	if !strings.Contains(got, "P1 in export.go") || !strings.Contains(got, "claims to verify") {
		t.Errorf("delivery = %q", got)
	}
	if loopStopReason([]string{"pane:%7"}) != "" {
		t.Error("delivered twice")
	}
	if ctx := loopPromptContext([]string{"pane:%7"}); !strings.Contains(ctx, "state: building") || !strings.Contains(ctx, "ptln loop ready "+r.ID) {
		t.Errorf("prompt context = %q", ctx)
	}
}

func TestEventLogRefusesADamagedLine(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	r := testLoopRun(t, t.TempDir())
	if evs, err := loopEvents(r.ID); err != nil || len(evs) != 1 || evs[0].Type != "started" {
		t.Fatalf("events = %+v, %v", evs, err)
	}
	f, _ := os.OpenFile(filepath.Join(loopDir(r.ID), "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString("{not json\n")
	f.Close()
	if _, err := loopEvents(r.ID); err == nil {
		t.Error("a damaged event log was read without error")
	}
}

// Checks run in the repository itself; a check during which the work changed is stale.
func TestChecksRunInPlaceAndDetectChangedWork(t *testing.T) {
	repo, _ := loopRepo(t)
	log := filepath.Join(t.TempDir(), "checks.log")
	res := runLoopChecks(repo, []string{"test -f main.go", "exit 3"}, log)
	if res.Stale || len(res.Checks) != 2 || res.Checks[0].Exit != 0 || res.Checks[1].Exit != 3 {
		t.Errorf("checks = %+v", res)
	}
	if b, _ := os.ReadFile(log); !strings.Contains(string(b), "$ exit 3") {
		t.Errorf("log: %s", b)
	}
	if res := runLoopChecks(repo, []string{"echo changed >> main.go"}, log); !res.Stale {
		t.Error("a check that edited the work was not marked stale")
	}
}

// A round whose work is identical to the previous round's stops the run.
func TestARoundThatChangesNothingStopsTheRun(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo, write := loopRepo(t)
	r := testLoopRun(t, repo)
	r.Checks = []string{"exit 1"}
	_ = r.set(loopBaseline, "", "approved", nil)
	loopBaselineRound(r) // exit 1 already fails: it must not count
	write("export.go", "package main\n")
	// A round applies its outcome to the run on disk, so each step is saved, and read back after.
	r, _ = loadLoopRun(r.ID)
	r.Round = 1
	_ = r.set(loopChecking, "", "ready", nil)
	loopRunRound(r, 1)
	r, _ = loadLoopRun(r.ID)
	// The acceptance run line fails (no export.csv), so round 1 goes back to building.
	if r.State != loopBuilding || !strings.Contains(r.Note, "failing") {
		t.Fatalf("after round 1: %s %q", r.State, r.Note)
	}
	msgs := strings.Join(loopTake(r.ID), "\n")
	if !strings.Contains(msgs, "test -f export.csv") || strings.Contains(msgs, "$ exit 1") {
		t.Errorf("round 1 messages:\n%s", msgs)
	}
	r.Round = 2
	_ = r.set(loopChecking, "", "ready", nil)
	loopRunRound(r, 2)
	r, _ = loadLoopRun(r.ID)
	if r.State != loopFailed || !strings.Contains(r.Note, "changed nothing") {
		t.Errorf("after an unchanged round 2: %s %q", r.State, r.Note)
	}
}

// A run that reached pr_ready is over: a new loop can start in the repository, and the session's
// prompts no longer carry the old run's state.
func TestPRReadyEndsTheRun(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	r := testLoopRun(t, repo)
	_ = r.set(loopPRReady, "no open P1", "pr_ready", nil)
	if activeLoopFor(repo) != nil || loopPromptContext([]string{"pane:%7"}) != "" {
		t.Error("a pr_ready run is still active")
	}
}

// An acceptance line identical to a check that passed this round is not run again; one that is not
// is run, and its failure counts.
func TestAcceptanceSkipsAPassedCheck(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo, _ := loopRepo(t)
	r := testLoopRun(t, repo)
	brief := strings.Replace(goodBrief, "## Acceptance\n", "## Acceptance\n- run: exit 7\n", 1)
	if got := acceptanceFailures(r, brief, loopFile(r.ID, "a.log"), map[string]bool{"exit 7": true}); hasCmd(got, "exit 7") {
		t.Errorf("a line that passed as a check was run again: %+v", got)
	}
	if got := acceptanceFailures(r, brief, loopFile(r.ID, "b.log"), nil); !hasCmd(got, "exit 7") {
		t.Errorf("a failing acceptance line was not reported: %+v", got)
	}
}

func hasCmd(cs []loopCheck, cmd string) bool {
	for _, c := range cs {
		if c.Cmd == cmd {
			return true
		}
	}
	return false
}
