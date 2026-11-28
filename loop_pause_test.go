package main

import (
	"strings"
	"testing"
	"time"
)

// A batch review a reviewer could not complete pauses the batch, naming the reviewer, instead of
// sending it to the person on the others' reading; resuming runs the review again.
func TestBatchReviewPausesOnAMissingReviewer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo, _ := loopRepo(t)
	b, r, _ := createBatch(repo, []string{"pane:%7"}, []string{"true"}, "claude,codex", []string{batchBrief("One", "a"), batchBrief("Two", "b")}, nil)
	reset := time.Now().Add(time.Hour).UTC()
	applyBatchReview(r, b, []loopReview{
		{Engine: "claude", Error: "reviewer limit reached", LimitedUntil: reset},
		{Engine: "codex", Verdict: "approve"},
	})
	r, _ = loadLoopRun(r.ID)
	if r.State != loopPaused || r.PausedFrom != loopBatchRev || !r.PausedUntil.Equal(reset) {
		t.Fatalf("state %s from %q until %v", r.State, r.PausedFrom, r.PausedUntil)
	}
	if !strings.Contains(r.Note, "claude: reviewer limit reached") || !strings.Contains(r.Note, "resumes by itself") {
		t.Errorf("note: %q", r.Note)
	}
	if msgs := strings.Join(loopTake(r.ID), "\n"); !strings.Contains(msgs, "Nothing passed on the other reviewers' word alone") {
		t.Errorf("message: %s", msgs)
	}
	pausedResume() // the limit has not lifted
	if r, _ = loadLoopRun(r.ID); r.State != loopPaused {
		t.Fatalf("resumed before the reset: %s", r.State)
	}
	saved := spawnBatchReview
	defer func() { spawnBatchReview = saved }()
	spawned := 0
	spawnBatchReview = func(string) error { spawned++; return nil }
	_ = withRunLock(r.ID, func(cur *loopRun) error {
		cur.PausedUntil = time.Now().Add(-time.Minute)
		return cur.save(loopEvent{Type: "test"})
	})
	pausedResume()
	if r, _ = loadLoopRun(r.ID); r.State != loopBatchRev || spawned != 1 || r.PausedFrom != "" {
		t.Errorf("after the reset: %s, spawned %d, from %q", r.State, spawned, r.PausedFrom)
	}
}

// A failure with no known reset waits for `ptln loop resume`; the watcher leaves it alone.
func TestPausedRoundWaitsForResumeWithoutAReset(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo, _ := loopRepo(t)
	r := testLoopRun(t, repo)
	r.Round = 2
	_ = r.set(loopChecking, "", "ready", nil)
	missing, until := reviewersMissing([]loopReview{
		{Engine: "claude", Error: "timed out"},
		{Engine: "codex", Error: "reviewer limit reached", LimitedUntil: time.Now().Add(time.Hour)},
		{Engine: "gemini", Verdict: "approve"},
	})
	if len(missing) != 2 || !until.IsZero() {
		t.Fatalf("missing %v until %v: a failure without a reset must make it manual", missing, until)
	}
	note, msg := pauseNote(r.ID, "round 2's review", missing, until)
	r.PausedFrom, r.PausedUntil = loopChecking, until
	roundApplyPaused(r, note, missing, msg)
	r, _ = loadLoopRun(r.ID)
	if r.State != loopPaused || !strings.Contains(r.Note, "claude: timed out") || !strings.Contains(r.Note, "ptln loop resume "+r.ID) {
		t.Fatalf("%s %q", r.State, r.Note)
	}
	if next := loopNextStep(r); !strings.Contains(next, "Nothing for you to do") {
		t.Errorf("next step: %q", next)
	}
	pausedResume()
	if r, _ = loadLoopRun(r.ID); r.State != loopPaused {
		t.Fatalf("the watcher resumed a manual pause: %s", r.State)
	}
	saved := spawnRound
	defer func() { spawnRound = saved }()
	var rounds []int
	spawnRound = func(_ string, n int) error { rounds = append(rounds, n); return nil }
	if err := loopResumeRun(r.ID, "resumed by hand"); err != nil {
		t.Fatal(err)
	}
	if r, _ = loadLoopRun(r.ID); r.State != loopChecking || !r.Recheck || len(rounds) != 1 || rounds[0] != 2 {
		t.Errorf("after resume: %s recheck %t rounds %v", r.State, r.Recheck, rounds)
	}
	if err := loopResumeRun(r.ID, "again"); err == nil {
		t.Error("resumed a run that is not paused")
	}
}
