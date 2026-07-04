package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestParseTasks(t *testing.T) {
	p := filepath.Join(t.TempDir(), "backlog.txt")
	os.WriteFile(p, []byte("# a comment\n\nfirst task\n  second task  \n# skip\nthird\n"), 0o644)
	tasks, err := parseTasks(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"first task", "second task", "third"}
	if len(tasks) != 3 || tasks[0] != want[0] || tasks[1] != want[1] || tasks[2] != want[2] {
		t.Fatalf("got %v want %v", tasks, want)
	}
}

func TestCrankShouldHalt(t *testing.T) {
	// item cap
	if halt, _ := crankShouldHalt(3, 0, 0, crankOpts{max: 3}); !halt {
		t.Fatal("should halt at the item cap")
	}
	if halt, _ := crankShouldHalt(2, 0, 0, crankOpts{max: 3}); halt {
		t.Fatal("should NOT halt before the cap")
	}
	// consecutive failures
	if halt, why := crankShouldHalt(5, 2, 0, crankOpts{haltOnFail: 2}); !halt || why == "" {
		t.Fatalf("should halt on 2 consecutive fails: halt=%v why=%q", halt, why)
	}
	if halt, _ := crankShouldHalt(5, 1, 0, crankOpts{haltOnFail: 2}); halt {
		t.Fatal("one failure should not halt")
	}
	// max=0 means unlimited
	if halt, _ := crankShouldHalt(100, 0, 0, crankOpts{max: 0, haltOnFail: 2}); halt {
		t.Fatal("max=0 must not cap")
	}
}

// TestCrankShouldHaltTokenCeiling pins the O.5 stop condition in isolation: halt once the
// accumulated worklist tokens reach --max-tokens, never below it, and never when the ceiling is off.
func TestCrankShouldHaltTokenCeiling(t *testing.T) {
	if halt, why := crankShouldHalt(1, 0, 120, crankOpts{maxTokens: 100}); !halt || why == "" {
		t.Fatalf("should halt once tokens cross the ceiling: halt=%v why=%q", halt, why)
	}
	if halt, _ := crankShouldHalt(1, 0, 80, crankOpts{maxTokens: 100}); halt {
		t.Fatal("should NOT halt below the ceiling")
	}
	// maxTokens=0 (default) = off: even a huge total must not halt on tokens.
	if halt, _ := crankShouldHalt(50, 0, 1_000_000, crankOpts{maxTokens: 0}); halt {
		t.Fatal("maxTokens=0 must never halt on tokens")
	}
}

// TestRunCrankWithHaltsOnTokenBudget drives the loop with a fake per-task usage sequence: with a
// ceiling, it runs tasks until the accumulated total crosses --max-tokens then stops BEFORE the
// next task; with --max-tokens 0 it never halts on tokens. This is the O.5 definition-of-done.
func TestRunCrankWithHaltsOnTokenBudget(t *testing.T) {
	tasks := []string{"t1", "t2", "t3", "t4"}
	// Each task's worker reports 60 tokens.
	fakeExec := func() (taskExec, *[]int) {
		var ran []int
		return func(i int, task string) crankResult {
			ran = append(ran, i)
			return crankResult{task: task, branch: fmt.Sprintf("b%d", i), ok: true, tokens: 60}
		}, &ran
	}

	// Ceiling 100: task0 (0→60) and task1 (60→120) run, then usedTokens>=100 halts before task2.
	ex, ran := fakeExec()
	runCrankWith(tasks, crankOpts{maxTokens: 100}, ex, runReporter{})
	if len(*ran) != 2 || (*ran)[0] != 0 || (*ran)[1] != 1 {
		t.Fatalf("expected tasks 0,1 to run then halt on the token budget; ran=%v", *ran)
	}

	// Ceiling 0 (off): all four run regardless of tokens.
	ex, ran = fakeExec()
	runCrankWith(tasks, crankOpts{maxTokens: 0}, ex, runReporter{})
	if len(*ran) != 4 {
		t.Fatalf("maxTokens=0 must not halt on tokens; ran=%v", *ran)
	}
}

// TestRunCrankWithReportsTaskEvents pins the O.3 self-reporting contract: with a run reporter
// live, crank emits `queued` for the whole worklist up front, then `running` before each task and
// a terminal `done`/`failed` (carrying branch + note) after — in order. A fake exec stands in for
// the real worktree+worker so the loop's telemetry is what's under test, not `claude`.
func TestRunCrankWithReportsTaskEvents(t *testing.T) {
	tasks := []string{"task one", "task two"}
	type ev struct {
		idx                          int
		task, status, branch, detail string
	}
	var got []ev
	report := runReporter{post: func(idx int, task, status, branch, detail string) {
		got = append(got, ev{idx, task, status, branch, detail})
	}}
	// exec: task 0 succeeds (branch b0), task 1 fails (branch b1, note "boom").
	exec := func(i int, task string) crankResult {
		if i == 0 {
			return crankResult{task: task, branch: "b0", ok: true, note: "committed"}
		}
		return crankResult{task: task, branch: "b1", ok: false, note: "boom"}
	}
	runCrankWith(tasks, crankOpts{}, exec, report)

	want := []ev{
		{0, "task one", "queued", "", ""},
		{1, "task two", "queued", "", ""},
		{0, "task one", "running", "", ""},
		{0, "task one", "done", "b0", "committed"},
		{1, "task two", "running", "", ""},
		{1, "task two", "failed", "b1", "boom"},
	}
	if len(got) != len(want) {
		t.Fatalf("emitted %d events, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestRunReporterNoRunIDIsNoop pins that self-reporting is pure telemetry: no run id → a no-op
// reporter, so a crank invoked without --run (plain `ptln crank`) never tries to report.
func TestRunReporterNoRunIDIsNoop(t *testing.T) {
	if newRunReporter("").post != nil {
		t.Fatal("no run id must yield a no-op reporter (post == nil)")
	}
	// A run id but no device token in the env is still a no-op (nothing to auth with).
	t.Setenv("PARTYLINE_DAEMON_TOKEN", "")
	if newRunReporter("11111111-1111-1111-1111-111111111111").post != nil {
		t.Fatal("run id without a device token must be a no-op reporter")
	}
}

func TestFirstWords(t *testing.T) {
	if got := firstWords("add a dark mode toggle to the navbar", 4); got != "add a dark mode" {
		t.Fatalf("got %q", got)
	}
	if got := firstWords("short", 4); got != "short" {
		t.Fatalf("got %q", got)
	}
}
