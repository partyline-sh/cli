package main

import (
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func batchBrief(title, goal string) string {
	return strings.Replace(strings.Replace(goodBrief, "# CSV export", "# "+title, 1), "Export tickets as CSV.", goal, 1)
}

// approvedBatch is a three-step batch in a scratch repository, reviewed clean and approved.
func approvedBatch(t *testing.T) (string, *loopBatch, *loopRun) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	repo, _ := loopRepo(t)
	briefs := []string{batchBrief("One", "Do one."), batchBrief("Two", "Do two."), batchBrief("Three", "Do three.")}
	b, r, err := createBatch(repo, []string{"pane:%7"}, []string{"true"}, "claude", briefs, nil)
	if err != nil {
		t.Fatal(err)
	}
	applyBatchReview(r, b, []loopReview{{Engine: "claude"}})
	r, _ = loadLoopRun(r.ID)
	if r.State != loopAwaiting || !strings.Contains(latestBrief(r), "2. Two: Do two.") {
		t.Fatalf("after a clean review: %s\n%s", r.State, latestBrief(r))
	}
	spawnedRounds = nil
	if err := loopApproveRun(r.ID, r.Revision); err != nil {
		t.Fatal(err)
	}
	r, _ = loadLoopRun(r.ID)
	b, _ = loadBatch(b.ID)
	return repo, b, r
}

// One approval runs every step in order, each from the brief the person approved.
func TestBatchRunsStepsInOrderAfterOneApproval(t *testing.T) {
	repo, b, r := approvedBatch(t)
	if len(b.Hashes) != 3 || r.State != loopBaseline || latestBrief(r) != b.Briefs[0] {
		t.Fatalf("after approval: hashes %d, state %s, brief %q", len(b.Hashes), r.State, latestBrief(r))
	}
	for step := 2; step <= 3; step++ {
		_ = r.set(loopPRReady, "", "pr_ready", nil)
		next, err := batchNextStep(r)
		if err != nil {
			t.Fatalf("step %d: %v", step, err)
		}
		r, _ = loadLoopRun(next)
		if r.Step != step || r.State != loopBaseline || latestBrief(r) != b.Briefs[step-1] || r.Repo != repo {
			t.Errorf("step %d: %+v brief %q", step, r, latestBrief(r))
		}
	}
	if len(spawnedRounds) != 3 {
		t.Errorf("baselines started: %v", spawnedRounds)
	}
	_ = r.set(loopPRReady, "", "pr_ready", nil)
	if next, err := batchNextStep(r); err != nil || next != "" {
		t.Errorf("after the last step: %q %v", next, err)
	}
}

// A step whose brief is not the approved text never starts; nor does one after a failed step or
// with the previous step's work uncommitted.
func TestBatchNextRefuses(t *testing.T) {
	repo, b, r := approvedBatch(t)
	_ = r.set(loopFailed, "", "failed", nil)
	if _, err := batchNextStep(r); err == nil {
		t.Error("started the next step after a failed one")
	}
	_ = r.set(loopPRReady, "", "pr_ready", nil)
	_ = os.WriteFile(repo+"/new.go", []byte("package main\n"), 0o644)
	if _, err := batchNextStep(r); err == nil || !strings.Contains(err.Error(), "commit") {
		t.Errorf("uncommitted work: %v", err)
	}
	_ = exec.Command("git", "-C", repo, "add", "-A").Run()
	_ = exec.Command("git", "-C", repo, "commit", "-qm", "step 1").Run()
	b.Briefs[1] += "\nAlso rewrite everything.\n"
	_ = b.save()
	if _, err := batchNextStep(r); err == nil || !strings.Contains(err.Error(), "not the one the user approved") {
		t.Errorf("changed brief: %v", err)
	}
	if b, _ := loadBatch(b.ID); len(b.Runs) != 1 {
		t.Errorf("a step was started: %v", b.Runs)
	}
}

// P1s from the batch review go back to the session; once its revisions are used, the batch goes to
// the person with the open P1s said.
func TestBatchReviewP1(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo, _ := loopRepo(t)
	b, r, _ := createBatch(repo, []string{"pane:%7"}, []string{"true"}, "claude", []string{batchBrief("One", "a"), batchBrief("Two", "b")}, nil)
	p1 := []loopReview{{Engine: "claude", Findings: []loopFinding{{Priority: "P1", File: ".partyline-batch/step-2.md", Line: 1, Quote: "# Two", Claim: "step 2 needs step 3"}}}}
	applyBatchReview(r, b, p1)
	if r, _ = loadLoopRun(r.ID); r.State != loopRevising {
		t.Fatalf("P1 review: %s", r.State)
	}
	// A disputed P1 raised again does not block: it goes to the person with the batch.
	b.Disputed = []loopTracked{{ID: "B1-1", loopFinding: p1[0].Findings[0], Dispute: "step 3 is not needed"}}
	r.Revision = 2
	_ = r.set(loopBatchRev, "", "test", nil)
	applyBatchReview(r, b, p1)
	if r, _ = loadLoopRun(r.ID); r.State != loopAwaiting || !strings.Contains(latestBrief(r), "step 3 is not needed") {
		t.Errorf("disputed P1 raised again: %s\n%s", r.State, latestBrief(r))
	}
	// Out of revisions with an undisputed P1 open: the batch goes to the person with it listed,
	// and the approval screen says approving accepts it.
	b.Disputed = nil
	r.Revision = loopMaxRevisions
	_ = r.set(loopBatchRev, "", "test", nil)
	applyBatchReview(r, b, p1)
	if r, _ = loadLoopRun(r.ID); r.State != loopAwaiting || !strings.Contains(latestBrief(r), "1 open P1 after 3 revisions") || !strings.Contains(latestBrief(r), "step 2 needs step 3") {
		t.Fatalf("out of revisions: %s\n%s", r.State, latestBrief(r))
	}
	m, _ := fakeGate(t, []*loopRun{r}, nil)
	if out := plain(m); !strings.Contains(out, "Approving accepts them") {
		t.Errorf("the approval screen does not say approving accepts the open P1:\n%s", out)
	}
}

// A P1 disputed during a step is deferred: the step keeps building and the summary lists it.
func TestBatchDisputeIsDeferred(t *testing.T) {
	_, b, r := approvedBatch(t)
	_ = r.set(loopBuilding, "", "test", nil)
	writeLoopJSON(r.ID, "findings-0.json", []loopTracked{{ID: "F1-1", loopFinding: loopFinding{Priority: "P1", File: "a.go", Line: 1, Claim: "wrong"}}})
	if _, err := loopRaiseDispute(r.ID, "F1-1", "it is right"); err != nil {
		t.Fatal(err)
	}
	r, _ = loadLoopRun(r.ID)
	b, _ = loadBatch(b.ID)
	if r.State != loopBuilding || len(b.Deferred) != 1 || !strings.Contains(batchSummary(b), "Disputed during the batch (1)") {
		t.Errorf("after the dispute: %s, deferred %d\n%s", r.State, len(b.Deferred), batchSummary(b))
	}
}

// A revision inside the approved scope keeps the step building; one beyond it waits for the person.
func TestBatchStepRevision(t *testing.T) {
	_, _, r := approvedBatch(t)
	_ = r.set(loopBuilding, "", "test", nil)
	if why, err := batchReviseStep(r, batchBrief("One", "Do one, differently.")); why != "" || err != nil {
		t.Fatalf("in scope: %q %v", why, err)
	}
	if r, _ = loadLoopRun(r.ID); r.State != loopBuilding || !strings.Contains(latestBrief(r), "differently") {
		t.Errorf("in scope: %s %q", r.State, latestBrief(r))
	}
	batchScopeCheck = func(*loopRun, string, string) string { return "- adds a new feature" }
	defer func() { batchScopeCheck = func(*loopRun, string, string) string { return "" } }()
	if _, err := batchReviseStep(r, batchBrief("One", "Do one and five more things.")); err != nil {
		t.Fatalf("out of scope: %v", err)
	}
	if r, _ = loadLoopRun(r.ID); r.State != loopAwaiting || !strings.Contains(strings.Join(loopTake(r.ID), "\n"), "adds a new feature") {
		t.Errorf("out of scope: %s", r.State)
	}
	// Approving that revision approves the step's brief, not the batch again.
	if err := loopApproveRun(r.ID, r.Revision); err != nil {
		t.Fatal(err)
	}
	if r, _ = loadLoopRun(r.ID); !strings.Contains(latestBrief(r), "five more") {
		t.Errorf("approved revision replaced: %q", latestBrief(r))
	}
}

// The popup shows a batch as one approval: every step's goal, the review, and the code to approve.
func TestGateShowsABatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo, _ := loopRepo(t)
	b, r, _ := createBatch(repo, []string{"pane:%7"}, []string{"true"}, "claude", []string{batchBrief("One", "Do one."), batchBrief("Two", "Do two.")}, nil)
	applyBatchReview(r, b, []loopReview{{Engine: "claude"}})
	r, _ = loadLoopRun(r.ID)
	m, c := fakeGate(t, []*loopRun{r}, nil)
	out := plain(m)
	for _, want := range []string{"APPROVE A BATCH", "1. One: Do one.", "2. Two: Do two.", "approves every step"} {
		if !strings.Contains(out, want) {
			t.Errorf("batch popup lacks %q:\n%s", want, out)
		}
	}
	// Each step's goal is its own line, at any width.
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 30})
	if !regexp.MustCompile(`(?m)^\s*(goal\s+)?2\. Two: Do two\.`).MatchString(plain(nm.(gateModel))) {
		t.Errorf("step 2 does not start its own line at 60 columns:\n%s", plain(nm.(gateModel)))
	}
	if gateKeys(m, "a"); len(c.approved) != 0 {
		t.Error("a batch was approved without the code")
	}
	if gateKeys(m, "a", "4", "2"); len(c.approved) != 1 {
		t.Errorf("approve with the code: %v", c.approved)
	}
}

// Every scope review compares with the brief the person approved, not the last revision; after
// maxFreeRevisions the next one goes to the person; an unverified P1 fails the review.
func TestBatchScopeBaseline(t *testing.T) {
	_, b, r := approvedBatch(t)
	_ = r.set(loopBuilding, "", "test", nil)
	var against []string
	batchScopeCheck = func(_ *loopRun, approved, _ string) string { against = append(against, approved); return "" }
	defer func() { batchScopeCheck = func(*loopRun, string, string) string { return "" } }()
	for i := 1; i <= maxFreeRevisions; i++ {
		if why, err := batchReviseStep(r, batchBrief("One", "Do one, take "+strconv.Itoa(i)+".")); why != "" || err != nil {
			t.Fatalf("revision %d: %q %v", i, why, err)
		}
		r, _ = loadLoopRun(r.ID)
	}
	for i, a := range against {
		if a != b.Briefs[0] {
			t.Errorf("scope review %d compared against %q, not the approved brief", i+1, a)
		}
	}
	if why, _ := batchReviseStep(r, batchBrief("One", "Do one, take 4.")); why == "" {
		t.Error("a fourth unattended revision was accepted")
	}
	if r, _ = loadLoopRun(r.ID); r.State != loopAwaiting {
		t.Errorf("after the cap: %s", r.State)
	}

	saved := reviewBriefs
	defer func() { reviewBriefs = saved }()
	reviewBriefs = func(*loopRun, map[string]string, string, string) ([]loopReview, error) {
		return []loopReview{{Engine: "claude", Dropped: []loopFinding{{Priority: "P1", Claim: "adds scope"}}}}, nil
	}
	if why := realBatchScopeCheck(r, "a", "b"); !strings.Contains(why, "unverified") {
		t.Errorf("an unverified P1 passed the scope review: %q", why)
	}
}

// A batch review that finishes after the batch was stopped leaves it stopped.
func TestBatchReviewAfterStop(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo, _ := loopRepo(t)
	_, r, _ := createBatch(repo, []string{"pane:%7"}, []string{"true"}, "claude", []string{batchBrief("One", "a"), batchBrief("Two", "b")}, nil)
	saved := reviewBriefs
	defer func() { reviewBriefs = saved }()
	reviewBriefs = func(*loopRun, map[string]string, string, string) ([]loopReview, error) {
		cur, _ := loadLoopRun(r.ID)
		_ = cur.set(loopStopped, "stopped by the user", "stopped", nil)
		return []loopReview{{Engine: "claude"}}, nil
	}
	loopBatchReviewMain([]string{r.ID})
	if r, _ = loadLoopRun(r.ID); r.State != loopStopped {
		t.Errorf("a stopped batch was resurrected as %s", r.State)
	}
}

// Between steps the batch still holds the worktree; stopping it there ends it.
func TestBatchBetweenSteps(t *testing.T) {
	repo, b, r := approvedBatch(t)
	_ = r.set(loopPRReady, "", "pr_ready", nil)
	if a := activeLoopFor(repo); a == nil || a.ID != r.ID {
		t.Fatalf("between steps the worktree is free: %v", a)
	}
	if !strings.Contains(loopNextStep(r), "partyline commits it and starts the next step") {
		t.Errorf("prompt line: %q", loopNextStep(r))
	}
	_ = withBatch(b.ID, func(b *loopBatch) error { b.Stopped = true; return nil })
	if _, err := batchNextStep(r); err == nil {
		t.Error("a stopped batch started its next step")
	}
	if activeLoopFor(repo) != nil {
		t.Error("a stopped batch still holds the worktree")
	}
}

// Deferred findings come to the person after the last step, one at a time; the fixes go to a builder.
func TestBatchDeferredReview(t *testing.T) {
	_, b, r := approvedBatch(t)
	_ = withBatch(b.ID, func(b *loopBatch) error {
		b.Deferred = []loopTracked{
			{ID: "S1-F1-1", Decision: "deferred", loopFinding: loopFinding{Priority: "P1", File: "a.go", Line: 1, Claim: "one"}},
			{ID: "S2-F1-1", Decision: "deferred", loopFinding: loopFinding{Priority: "P1", File: "b.go", Line: 1, Claim: "two"}},
		}
		return nil
	})
	for i := 0; i < 3; i++ {
		_ = r.set(loopPRReady, "", "pr_ready", nil)
		next, err := batchNextStep(r)
		if err != nil || next == "" {
			t.Fatalf("after step %d: %q %v", i+1, next, err)
		}
		r, _ = loadLoopRun(next)
	}
	if r.State != loopAwaitYou || r.Step != 4 {
		t.Fatalf("deferred review: %+v", r)
	}
	if err := loopDecideDispute(r.ID, "S1-F1-1", "fix"); err != nil {
		t.Fatal(err)
	}
	if r, _ = loadLoopRun(r.ID); r.State != loopAwaitYou {
		t.Fatalf("after the first ruling: %s", r.State)
	}
	if err := loopDecideDispute(r.ID, "S2-F1-1", "dismiss"); err != nil {
		t.Fatal(err)
	}
	r, _ = loadLoopRun(r.ID)
	task := startedBuilders[len(startedBuilders)-1]
	if r.State != loopBuilding || r.Builder == "" || !strings.Contains(task, "S1-F1-1") || strings.Contains(task, "S2-F1-1 [") {
		t.Errorf("after the rulings: %s, builder %q\n%s", r.State, r.Builder, task)
	}
}

// Before approval the session revises the batch, with disputes; after approval it cannot, so the
// approved briefs and their hashes stay as they were.
func TestBatchRevise(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo, _ := loopRepo(t)
	b, r, _ := createBatch(repo, []string{"pane:%7"}, []string{"true"}, "claude", []string{batchBrief("One", "a"), batchBrief("Two", "b")}, nil)
	p1 := []loopReview{{Engine: "claude", Findings: []loopFinding{
		{Priority: "P1", File: ".partyline-batch/step-2.md", Line: 1, Quote: "# Two", Claim: "needs three"},
		{Priority: "P1", File: ".partyline-batch/step-1.md", Line: 1, Quote: "# One", Claim: "vague"},
	}}}
	applyBatchReview(r, b, p1)
	revised := []string{batchBrief("One", "a, precisely"), batchBrief("Two", "b")}
	if err := batchRevise(r.ID, revised, map[string]string{"B1-1": "three is not needed"}); err != nil {
		t.Fatal(err)
	}
	b, _ = loadBatch(b.ID)
	r, _ = loadLoopRun(r.ID)
	if r.State != loopBatchRev || len(b.Disputed) != 1 || b.Disputed[0].Claim != "needs three" || b.Briefs[0] != revised[0] {
		t.Fatalf("after revise: %s, disputed %+v", r.State, b.Disputed)
	}
	applyBatchReview(r, b, []loopReview{{Engine: "claude"}})
	_ = loopApproveRun(r.ID, r.Revision)
	r, _ = loadLoopRun(r.ID)
	_ = r.set(loopRevising, "", "rejected", nil) // a later step revision the person rejected
	if err := batchRevise(r.ID, []string{batchBrief("X", "x"), batchBrief("Y", "y")}, nil); err == nil {
		t.Error("an approved batch's briefs were replaced")
	}
	if b, _ = loadBatch(b.ID); b.Briefs[0] != revised[0] || b.Hashes[0] != briefHash(revised[0]) {
		t.Errorf("approved briefs changed: %q", b.Briefs[0])
	}
}

// A finding the person ruled "fix" stays open while reviewers still find it; dismissed and
// deferred ones are not raised again.
func TestSettledFindings(t *testing.T) {
	f := loopFinding{Priority: "P1", File: "a.go", Line: 3, Quote: "return nil // TODO", Claim: "The write error is dropped silently."}
	for decision, suppressed := range map[string]bool{"fix": false, "dismiss": true, "deferred": true} {
		ds := []loopTracked{{ID: "F1-1", loopFinding: f, Decision: decision}}
		if got := sameFinding(settled(ds), f); got != suppressed {
			t.Errorf("%s: suppressed=%v, want %v", decision, got, suppressed)
		}
	}
}

// The popup stops a loop only with the code; the batch approval screen shows the review's result
// and the disputes approving dismisses.
func TestGateStopAndBatchReviewShown(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo, _ := loopRepo(t)
	b, r, _ := createBatch(repo, []string{"pane:%7"}, []string{"true"}, "claude", []string{batchBrief("One", "a"), batchBrief("Two", "b")}, nil)
	b.Disputed = []loopTracked{{ID: "B1-1", loopFinding: loopFinding{Priority: "P1", File: "x", Line: 1, Claim: "c"}, Dispute: "not so"}}
	applyBatchReview(r, b, []loopReview{{Engine: "claude", Verdict: "approve"}})
	r, _ = loadLoopRun(r.ID)
	m, c := fakeGate(t, []*loopRun{r}, nil)
	out := plain(m)
	for _, want := range []string{"No open P1", "1 finding(s) the session disputes"} {
		if !strings.Contains(out, want) {
			t.Errorf("batch screen lacks %q:\n%s", want, out)
		}
	}
	if gateKeys(m, "s"); len(c.stopped) != 0 {
		t.Error("stopped without the code")
	}
	if gateKeys(m, "s", "4", "2"); len(c.stopped) != 1 {
		t.Errorf("stop with the code: %v", c.stopped)
	}
}

// A batch review whose briefs were replaced while it ran is dropped; status takes the batch id.
func TestBatchReviewBoundToItsBriefs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo, _ := loopRepo(t)
	b, r, _ := createBatch(repo, []string{"pane:%7"}, []string{"true"}, "claude", []string{batchBrief("One", "a"), batchBrief("Two", "b")}, nil)
	saved := reviewBriefs
	defer func() { reviewBriefs = saved }()
	reviewBriefs = func(*loopRun, map[string]string, string, string) ([]loopReview, error) {
		_ = withBatch(b.ID, func(b *loopBatch) error { b.Briefs[1] = batchBrief("Two", "changed"); return nil })
		return []loopReview{{Engine: "claude"}}, nil
	}
	loopBatchReviewMain([]string{r.ID})
	if r, _ = loadLoopRun(r.ID); r.State != loopBatchRev {
		t.Errorf("a review of other briefs was applied: %s", r.State)
	}
	if got, err := batchByID(b.ID); err != nil || got.ID != b.ID {
		t.Errorf("status by batch id: %v", err)
	}
	if got, err := batchByID(r.ID); err != nil || got.ID != b.ID {
		t.Errorf("status by loop id: %v", err)
	}
}

// Reviewers are told to check findings ruled fix, and to leave dismissed and deferred ones alone.
func TestDecidedPrompt(t *testing.T) {
	p := decidedSection([]loopTracked{
		{ID: "a", loopFinding: loopFinding{File: "a.go", Line: 1, Claim: "ruled fix"}, Decision: "fix"},
		{ID: "b", loopFinding: loopFinding{File: "b.go", Line: 1, Claim: "dismissed"}, Decision: "dismiss"},
	})
	fixAt, quietAt := strings.Index(p, "ruled fix"), strings.Index(p, "dismissed")
	if !strings.Contains(p, "raise it again as a P1 if it is still not fixed") || fixAt < strings.Index(p, "must be fixed") || quietAt > strings.Index(p, "must be fixed") {
		t.Errorf("prompt:\n%s", p)
	}
}

// Two different defects quoting the same line are two findings; a reworded claim about the same
// defect is the same one.
func TestSameFindingNeedsTheSameClaim(t *testing.T) {
	settledOne := []loopTracked{{loopFinding: loopFinding{File: "a.go", Quote: "return nil // TODO", Claim: "The error from the write is dropped, so a failed save reports success."}, Decision: "dismiss"}}
	other := loopFinding{File: "a.go", Quote: "return nil // TODO", Claim: "This path skips the lock, so two writers can interleave."}
	reworded := loopFinding{File: "./a.go", Quote: "return nil  // TODO", Claim: "A failed save reports success because the write error is dropped."}
	if sameFinding(settledOne, other) {
		t.Error("a different defect on the same line was suppressed")
	}
	if !sameFinding(settledOne, reworded) {
		t.Error("a reworded claim about the same defect was not recognised")
	}
}

// A running batch is listed in the popup, where it can only be stopped.
func TestGateListsARunningBatch(t *testing.T) {
	_, _, r := approvedBatch(t)
	_ = r.set(loopBuilding, "building", "test", nil)
	if runs := gateRuns(); len(runs) != 1 || runs[0].ID != r.ID {
		t.Fatalf("gate runs: %v", runs)
	}
	m, c := fakeGate(t, []*loopRun{r}, nil)
	if out := plain(m); !strings.Contains(out, "RUNNING BATCH") || !strings.Contains(out, "stop the batch") {
		t.Errorf("running batch screen:\n%s", out)
	}
	m = gateKeys(m, "a", "4", "2")
	if len(c.approved) != 0 {
		t.Error("a running batch was approved")
	}
	if gateKeys(m, "s", "4", "2"); len(c.stopped) != 1 {
		t.Errorf("stop: %v", c.stopped)
	}
}

// After a finding is deferred or ruled on, the next round may review the same code: it is not
// "changed nothing". A round whose run was stopped meanwhile does not undo the stop.
func TestRoundAfterRulingAndAfterStop(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo, _ := loopRepo(t)
	r := testLoopRun(t, repo)
	r.Checks = []string{"false"} // fails, and did not fail at baseline: the round goes back to building
	tree := runLoopChecks(repo, r.Checks, loopFile(r.ID, "probe.log")).Tree
	r.LastTree, r.Recheck, r.Round = tree, true, 2
	_ = r.set(loopChecking, "", "ready", nil)
	loopRunRound(r, 2)
	if got, _ := loadLoopRun(r.ID); got.State != loopBuilding || got.Recheck {
		t.Errorf("unchanged tree after a ruling: %s recheck=%v (%s)", got.State, got.Recheck, got.Note)
	}

	r, _ = loadLoopRun(r.ID)
	r.Round = 3
	_ = r.set(loopChecking, "", "ready", nil)
	stale, _ := loadLoopRun(r.ID)
	_ = r.set(loopStopped, "stopped by the user", "stopped", nil)
	loopRunRound(stale, 3)
	if got, _ := loadLoopRun(r.ID); got.State != loopStopped {
		t.Errorf("a round finishing after the stop moved the run to %s", got.State)
	}
}

// Stop on a step that batch next has just moved past follows the batch to its current step; stop
// between steps keeps the passed step pr_ready and stops the batch.
func TestStopFollowsTheBatch(t *testing.T) {
	_, b, r := approvedBatch(t)
	_ = r.set(loopPRReady, "passed", "pr_ready", nil)
	if err := loopStopRun(r.ID); err != nil {
		t.Fatal(err)
	}
	r, _ = loadLoopRun(r.ID)
	if b, _ = loadBatch(b.ID); r.State != loopPRReady || !b.Stopped {
		t.Errorf("between steps: %s stopped=%v", r.State, b.Stopped)
	}
	evs, _ := loopEvents(r.ID)
	if evs[len(evs)-1].Type != "batch_stopped" {
		t.Errorf("last event %s", evs[len(evs)-1].Type)
	}

	_, b, r = approvedBatch(t)
	_ = r.set(loopPRReady, "", "pr_ready", nil)
	next, err := batchNextStep(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := loopStopRun(r.ID); err != nil { // the id of the step that already passed
		t.Fatal(err)
	}
	if n, _ := loadLoopRun(next); n.State != loopStopped {
		t.Errorf("the current step is %s after stopping by the previous step's id", n.State)
	}
}

// The next step starts only on the batch's branch, on top of the step that passed.
func TestBatchNextStaysOnItsBranch(t *testing.T) {
	repo, b, r := approvedBatch(t)
	if b.Branch != "main" {
		t.Fatalf("branch recorded: %q", b.Branch)
	}
	_ = r.set(loopPRReady, "", "pr_ready", nil)
	_ = exec.Command("git", "-C", repo, "checkout", "-qb", "elsewhere").Run()
	if _, err := batchNextStep(r); err == nil || !strings.Contains(err.Error(), "branch main") {
		t.Errorf("on another branch: %v", err)
	}
}

// A failed step ends the batch and puts the findings deferred so far to the person, for rulings only.
func TestFailedStepPutsDeferredToThePerson(t *testing.T) {
	_, b, r := approvedBatch(t)
	_ = withBatch(b.ID, func(b *loopBatch) error {
		b.Deferred = []loopTracked{{ID: "S1-F1-1", Decision: "deferred", loopFinding: loopFinding{Priority: "P1", File: "a.go", Line: 1, Claim: "one"}}}
		return nil
	})
	r.Round = loopMaxRounds
	_ = r.set(loopBuilding, "", "test", nil)
	if _, err := loopStartRound(r.ID, "report"); err == nil {
		t.Fatal("a seventh round started")
	}
	b, _ = loadBatch(b.ID)
	if !b.Failed || len(b.Runs) != 1 || b.Deferral == "" {
		t.Fatalf("after the failed step: failed=%v runs=%v deferral=%q", b.Failed, b.Runs, b.Deferral)
	}
	if !strings.Contains(batchSummary(b), "2. Two — not started") {
		t.Errorf("summary maps steps to runs wrongly:\n%s", batchSummary(b))
	}
	d, _ := loadLoopRun(b.Deferral)
	if d.State != loopAwaitYou {
		t.Fatalf("deferred review: %s", d.State)
	}
	if err := loopDecideDispute(d.ID, "S1-F1-1", "fix"); err != nil {
		t.Fatal(err)
	}
	if d, _ = loadLoopRun(d.ID); d.State != loopPRReady || !strings.Contains(d.Note, "1 to fix") {
		t.Errorf("rulings on an ended batch: %s %q", d.State, d.Note)
	}
}

// Approving a brief revision mid-run carries on building from the same base, without re-running
// the baseline.
func TestApprovedRevisionKeepsTheBaseline(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo, _ := loopRepo(t)
	r := testLoopRun(t, repo)
	_ = r.set(loopAwaiting, "", "started", nil)
	if err := loopApproveRun(r.ID, 1); err != nil {
		t.Fatal(err)
	}
	r, _ = loadLoopRun(r.ID)
	base := r.Base
	writeLoopJSON(r.ID, "baseline.json", loopCheckRound{})
	r.Round = 0 // revised before the first ready: the baseline still stands
	_ = r.set(loopBuilding, "", "test", nil)
	if err := loopReviseBrief(r.ID, goodBrief+"\n"); err != nil {
		t.Fatal(err)
	}
	r, _ = loadLoopRun(r.ID)
	spawnedRounds = nil
	if err := loopApproveRun(r.ID, r.Revision); err != nil {
		t.Fatal(err)
	}
	if r, _ = loadLoopRun(r.ID); r.State != loopBuilding || r.Base != base || len(spawnedRounds) != 0 {
		t.Errorf("after approving the revision: %s base %s (was %s), rounds %v", r.State, r.Base, base, spawnedRounds)
	}
}

// The last step holds the worktree until batch next closes the batch.
func TestLastStepHeldUntilClosed(t *testing.T) {
	repo, _, r := approvedBatch(t)
	for i := 0; i < 2; i++ {
		_ = r.set(loopPRReady, "", "pr_ready", nil)
		next, _ := batchNextStep(r)
		r, _ = loadLoopRun(next)
	}
	_ = r.set(loopPRReady, "", "pr_ready", nil)
	if a := activeLoopFor(repo); a == nil || a.ID != r.ID {
		t.Fatalf("last step released the worktree before closing: %v", a)
	}
	if _, err := batchNextStep(r); err != nil {
		t.Fatal(err)
	}
	if a := activeLoopFor(repo); a != nil {
		t.Errorf("closed batch still holds the worktree: %s", a.ID)
	}
}
