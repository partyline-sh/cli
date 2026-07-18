package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"partyline.sh/partyline/internal/api"
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
	var got []api.RunTaskUpdate
	report := runReporter{post: func(tr api.RunTaskUpdate) { got = append(got, tr) }}
	// exec: task 0 succeeds (branch b0, with a summary + tokens), task 1 fails (branch b1, "boom").
	exec := func(i int, task string) crankResult {
		if i == 0 {
			return crankResult{task: task, branch: "b0", ok: true, note: "committed", summary: "edited foo.go", tokens: 1200}
		}
		return crankResult{task: task, branch: "b1", ok: false, note: "boom"}
	}
	runCrankWith(tasks, crankOpts{}, exec, report)

	want := []struct {
		idx                          int
		task, status, branch, detail string
	}{
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
		g := got[i]
		if g.Idx != want[i].idx || g.Task != want[i].task || g.Status != want[i].status || g.Branch != want[i].branch || g.Detail != want[i].detail {
			t.Errorf("event %d = %+v, want core %+v", i, g, want[i])
		}
	}
	// #263 (run legibility): the terminal `done` event carries the worker's own summary + tokens.
	if done := got[3]; done.Summary != "edited foo.go" || done.Tokens != 1200 {
		t.Errorf("done event missing legibility detail: summary=%q tokens=%d", done.Summary, done.Tokens)
	}
	// Lifecycle-only events (queued/running) carry no summary/tokens/duration.
	if q := got[0]; q.Summary != "" || q.Tokens != 0 || q.DurationMs != 0 {
		t.Errorf("queued event should carry no result detail: %+v", q)
	}
}

// TestRunCrankWithRateLimitBlocks pins the Slice 2 pause contract: a task throttled mid-run
// (rateLimitResetAt set) is reported `blocked` — NOT `done` (which a resume would SKIP, abandoning
// the partial work) and NOT `failed` — carrying its resume handle, and the loop STOPS so the fleet
// doesn't hammer the throttled provider. maybePauseForRateLimit (run-level pause) isn't exercised
// here: crankOpts{} has no run id, so it returns without touching the process.
func TestRunCrankWithRateLimitBlocks(t *testing.T) {
	tasks := []string{"t0", "t1", "t2"}
	reset := time.Now().Add(2 * time.Hour)
	var ran []int
	exec := func(i int, task string) crankResult {
		ran = append(ran, i)
		if i == 0 {
			return crankResult{task: task, branch: "b0", ok: false, rateLimitResetAt: reset, resumeHandle: "sess-abc"}
		}
		return crankResult{task: task, branch: fmt.Sprintf("b%d", i), ok: true, note: "committed"}
	}
	var got []api.RunTaskUpdate
	report := runReporter{post: func(tr api.RunTaskUpdate) { got = append(got, tr) }}

	runCrankWith(tasks, crankOpts{}, exec, report)

	// The rate limit stops the loop after task 0 — t1/t2 never run.
	if len(ran) != 1 || ran[0] != 0 {
		t.Fatalf("rate limit must stop the loop after task 0; ran=%v", ran)
	}
	// Task 0's terminal event is `blocked` (resumable), carrying the handle for resume-in-place.
	var term *api.RunTaskUpdate
	for i := range got {
		if got[i].Idx == 0 && (got[i].Status == "blocked" || got[i].Status == "done" || got[i].Status == "failed") {
			term = &got[i]
		}
	}
	if term == nil {
		t.Fatal("no terminal event for the throttled task")
	}
	if term.Status != "blocked" {
		t.Errorf("throttled task status = %q, want blocked (done→skipped on resume, failed→misleading)", term.Status)
	}
	if term.ResumeHandle != "sess-abc" {
		t.Errorf("throttled task must persist its resume handle; got %q", term.ResumeHandle)
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

// TestRunCrankWithResumeSkipsDone pins the #81 slice 3a resume contract: given a set of
// already-`done` indices, runCrankWith runs EXACTLY the remaining tasks and never re-runs a
// skipped one — while preserving each task's ORIGINAL backlog index in the emitted events (so
// run_tasks stays aligned for 3b + telemetry). Skipped tasks are also not re-`queued`.
func TestRunCrankWithResumeSkipsDone(t *testing.T) {
	tasks := []string{"t0", "t1", "t2", "t3"}
	skip := map[int]bool{0: true, 2: true} // t0 and t2 already done in the store

	var ran []int
	exec := func(i int, task string) crankResult {
		ran = append(ran, i)
		return crankResult{task: task, branch: fmt.Sprintf("b%d", i), ok: true, note: "committed"}
	}
	type ev struct {
		idx                          int
		task, status, branch, detail string
	}
	var got []ev
	report := runReporter{post: func(tr api.RunTaskUpdate) {
		got = append(got, ev{tr.Idx, tr.Task, tr.Status, tr.Branch, tr.Detail})
	}}

	runCrankWith(tasks, crankOpts{resume: true, resumeSkip: skip}, exec, report)

	// Only the non-skipped tasks run, at their ORIGINAL indices.
	if len(ran) != 2 || ran[0] != 1 || ran[1] != 3 {
		t.Fatalf("expected only tasks 1,3 to run at original indices; ran=%v", ran)
	}

	// Events: queued for 1 and 3 only (no re-queue of skipped 0,2), then running+done per task,
	// all carrying the original index.
	want := []ev{
		{1, "t1", "queued", "", ""},
		{3, "t3", "queued", "", ""},
		{1, "t1", "running", "", ""},
		{1, "t1", "done", "b1", "committed"},
		{3, "t3", "running", "", ""},
		{3, "t3", "done", "b3", "committed"},
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

func TestFirstWords(t *testing.T) {
	if got := firstWords("add a dark mode toggle to the navbar", 4); got != "add a dark mode" {
		t.Fatalf("got %q", got)
	}
	if got := firstWords("short", 4); got != "short" {
		t.Fatalf("got %q", got)
	}
}
