package gate

import (
	"testing"

	"partyline.sh/partyline/internal/surface"
)

func lr(lane, verdict string, fs ...Finding) LaneResult {
	return LaneResult{Lane: lane, Verdict: verdict, Findings: fs}
}

func f(file string, line int, title string) Finding {
	return Finding{File: file, Line: line, Title: title}
}

// THE POINT OF THE SLICE. Two independent reviewers reading the same diff will describe the same
// defect differently — different wording, different word order, a few lines apart. If the merge
// misses that, every finding looks like it came from one reviewer and the agreement signal — the
// only thing two lanes buy that one cannot — is destroyed.
func TestTheSameDefectDescribedDifferentlyMergesToOne(t *testing.T) {
	merged := MergeFindings([]LaneResult{
		lr("primary", surface.VerdictPass, f("runs.ts", 42, "Missing null check on the run row")),
		lr("secondary", surface.VerdictPass, f("runs.ts", 44, "the run row is missing a null check")),
	})
	if len(merged) != 1 {
		t.Fatalf("got %d findings, want 1 — two reviewers described ONE defect:\n%+v", len(merged), merged)
	}
	agreed, judged := Agreement(merged[0], []LaneResult{lr("primary", surface.VerdictPass), lr("secondary", surface.VerdictPass)})
	if agreed != 2 || judged != 2 {
		t.Errorf("agreement = %d/%d, want 2/2 — this is the signal the whole slice exists to produce", agreed, judged)
	}
}

func TestGenuinelyDifferentFindingsStaySeparate(t *testing.T) {
	merged := MergeFindings([]LaneResult{
		lr("primary", surface.VerdictPass,
			f("runs.ts", 42, "missing null check"),
			f("runs.ts", 90, "unbounded query")),
		lr("secondary", surface.VerdictPass, f("other.ts", 42, "missing null check")),
	})
	if len(merged) != 3 {
		t.Fatalf("got %d, want 3 — different files and different defects must not collapse:\n%+v", len(merged), merged)
	}
	for _, m := range merged {
		if a, _ := Agreement(m, nil); a != 1 {
			t.Errorf("%q in %s claims agreement it does not have", m.Title, m.File)
		}
	}
}

// Far apart in the same file is a different defect, not the same one seen twice.
func TestDistantLinesDoNotMerge(t *testing.T) {
	merged := MergeFindings([]LaneResult{
		lr("a", surface.VerdictPass, f("x.go", 10, "missing null check")),
		lr("b", surface.VerdictPass, f("x.go", 400, "missing null check")),
	})
	if len(merged) != 2 {
		t.Fatalf("got %d, want 2 — 390 lines apart is not the same finding", len(merged))
	}
}

// The ordering IS the product. A human reads a review queue top-down; what two reviewers converged
// on has to come before one reviewer's stylistic preference, or the ranking buys nothing.
func TestAgreementSortsAboveEverythingElse(t *testing.T) {
	merged := MergeFindings([]LaneResult{
		lr("a", surface.VerdictPass,
			Finding{File: "z.go", Line: 1, Title: "style nit", Severity: "critical"},
			f("a.go", 10, "real bug")),
		lr("b", surface.VerdictPass, f("a.go", 11, "real bug")),
	})
	if len(merged) != 2 {
		t.Fatalf("expected 2 findings, got %+v", merged)
	}
	if merged[0].File != "a.go" {
		t.Errorf("first finding is %q; the one BOTH reviewers raised must come first even though the "+
			"other is marked critical by a single lane", merged[0].File)
	}
}

func TestSeverityBreaksTiesWithinTheSameAgreement(t *testing.T) {
	merged := MergeFindings([]LaneResult{
		lr("a", surface.VerdictPass,
			Finding{File: "b.go", Line: 5, Title: "minor thing", Severity: "low"},
			Finding{File: "a.go", Line: 5, Title: "bad thing", Severity: "high"}),
	})
	if merged[0].Severity != "high" {
		t.Errorf("severity did not order equal-agreement findings: %+v", merged)
	}
}

// A merged finding keeps the fuller explanation and the higher severity, because that is what a
// human needs — not whichever lane happened to be processed first.
func TestMergeKeepsTheBetterInformation(t *testing.T) {
	merged := MergeFindings([]LaneResult{
		lr("a", surface.VerdictPass, Finding{File: "x.go", Line: 3, Title: "bug", Body: "short", Severity: "low"}),
		lr("b", surface.VerdictPass, Finding{File: "x.go", Line: 3, Title: "bug", Body: "a much longer explanation of why", Severity: "high"}),
	})
	if len(merged) != 1 {
		t.Fatalf("expected one merged finding, got %d", len(merged))
	}
	if merged[0].Body != "a much longer explanation of why" {
		t.Errorf("body = %q, want the fuller one", merged[0].Body)
	}
	if merged[0].Severity != "high" {
		t.Errorf("severity = %q, want the higher one", merged[0].Severity)
	}
}

// ---- the quorum rule ----

// A SINGLE reviewer's rejection blocks, because there is nobody to agree with. Anything else would
// mean turning on a second lane weakens the one-reviewer gate you already had.
func TestALoneJudgeRejectionBlocks(t *testing.T) {
	if v, _ := Quorum([]LaneResult{lr("a", surface.VerdictFail)}); v != surface.VerdictFail {
		t.Errorf("verdict = %q, want fail — with one judge there is no such thing as a lone dissent", v)
	}
	// Same when the other lane could not run: one judge, one verdict.
	v, _ := Quorum([]LaneResult{
		lr("a", surface.VerdictFail),
		{Lane: "b", Verdict: surface.VerdictBlocked, Code: surface.CodeProviderRateLimited},
	})
	if v != surface.VerdictFail {
		t.Errorf("verdict = %q — a throttled second lane must not soften the first one's rejection", v)
	}
}

// THE DEFAULT, CHANGED ON PURPOSE. The first draft blocked on ANY rejection. That is sound
// reasoning with a possibly-wrong conclusion: under fail-any, adding a second lane strictly
// INCREASES the quarantine rate, so a noisier reviewer costs a human exactly the minutes this epic
// exists to save. Under the default a lone dissent among several judges does not block — it becomes
// a finding on the pull request, where the human deciding to merge will read it. Nothing is
// discarded; only the blocking threshold moves.
func TestALoneDissentAmongJudgesDoesNotBlockByDefault(t *testing.T) {
	lanes := []LaneResult{
		lr("a", surface.VerdictFail, f("x.go", 5, "questionable")),
		lr("b", surface.VerdictPass),
		lr("c", surface.VerdictPass),
	}
	v, _ := Quorum(lanes)
	if v != surface.VerdictPassWithFindings {
		t.Errorf("verdict = %q, want pass_with_findings — a lone objection is surfaced, not enforced", v)
	}
	// And it must NOT vanish: the whole justification is that the objection still reaches a human.
	if len(MergeFindings(lanes)) == 0 {
		t.Error("the dissenting reviewer's finding was discarded — then the trade would be indefensible")
	}
}

func TestTwoRejectionsBlock(t *testing.T) {
	v, _ := Quorum([]LaneResult{
		lr("a", surface.VerdictFail),
		lr("b", surface.VerdictFail),
		lr("c", surface.VerdictPass),
	})
	if v != surface.VerdictFail {
		t.Errorf("verdict = %q, want fail — two reviewers agreed the change is wrong", v)
	}
}

// A project that would rather stop than let anything through can still have that.
func TestFailAnyIsAvailableForProjectsThatWantIt(t *testing.T) {
	lanes := []LaneResult{lr("a", surface.VerdictFail), lr("b", surface.VerdictPass), lr("c", surface.VerdictPass)}
	if v, _ := QuorumWith(lanes, FailAny); v != surface.VerdictFail {
		t.Errorf("verdict = %q under FailAny, want fail", v)
	}
	if v, _ := QuorumWith(lanes, FailOnAgreement); v != surface.VerdictPassWithFindings {
		t.Errorf("verdict = %q under the default, want pass_with_findings", v)
	}
}

func TestFindingsWithoutRejectionMerge(t *testing.T) {
	v, _ := Quorum([]LaneResult{lr("a", surface.VerdictPass, f("x.go", 1, "consider renaming"))})
	if v != surface.VerdictPassWithFindings {
		t.Errorf("verdict = %q, want pass_with_findings", v)
	}
}

func TestCleanPass(t *testing.T) {
	if v, _ := Quorum([]LaneResult{lr("a", surface.VerdictPass), lr("b", surface.VerdictPass)}); v != surface.VerdictPass {
		t.Errorf("verdict = %q, want pass", v)
	}
}

// One lane that could not run must not stop a lane that did. This is what makes a second lane safe
// to add: a flaky or rate-limited reviewer degrades to one-reviewer behaviour instead of blocking
// every run.
func TestOneBlockedLaneDoesNotBlockTheGate(t *testing.T) {
	v, _ := Quorum([]LaneResult{
		{Lane: "a", Verdict: surface.VerdictBlocked, Code: surface.CodeProviderRateLimited},
		lr("b", surface.VerdictPass),
	})
	if v != surface.VerdictPass {
		t.Errorf("verdict = %q — a throttled second reviewer must not block work the first one cleared", v)
	}
}

// But if NOBODY judged it, there is no verdict. Fail-closed, and the reason names the actual
// problem: "timed out" and "unknown engine" need different fixes.
func TestAllLanesBlockedIsBlocked(t *testing.T) {
	v, code := Quorum([]LaneResult{
		{Lane: "a", Verdict: surface.VerdictBlocked, Code: surface.CodeEngineUnknown},
		{Lane: "b", Verdict: surface.VerdictBlocked, Code: surface.CodeProviderTimeout},
	})
	if v != surface.VerdictBlocked {
		t.Errorf("verdict = %q, want blocked — nobody judged the diff", v)
	}
	if code != surface.CodeEngineUnknown {
		t.Errorf("code = %q, want the first lane's actual reason", code)
	}
}

func TestNoLanesIsSkippedNotPass(t *testing.T) {
	v, _ := Quorum(nil)
	if v != surface.VerdictSkipped {
		t.Errorf("verdict = %q, want skipped — no reviewer configured has proved nothing", v)
	}
}

// THE DEFECT THE FIRST VERSION SHIPPED. A project configures two lanes; one is permanently
// rate-limited. Bare agreement badges "1/1 reviewers" — maximum confidence from a gate that
// silently lost half its reviewers. That is the same failure this epic bans everywhere else:
// "we did not check" must never read as "it passed".
func TestAgreementCannotOverstateItselfWhenALaneNeverRan(t *testing.T) {
	lanes := []LaneResult{
		{Lane: "primary", Verdict: surface.VerdictPass, Findings: []Finding{f("a.go", 1, "bug")}},
		{Lane: "secondary", Verdict: surface.VerdictBlocked, Code: surface.CodeProviderRateLimited},
	}
	merged := MergeFindings(lanes)
	agreed, judged := Agreement(merged[0], lanes)
	if agreed != 1 {
		t.Errorf("agreed = %d, want 1", agreed)
	}
	if judged != 1 {
		t.Errorf("judged = %d, want 1 — a lane that never ran must not inflate the denominator", judged)
	}
	if len(Degraded(lanes)) != 1 {
		t.Error("the gate ran on fewer reviewers than configured and said nothing about it")
	}
}

// The instrumentation has one job: make "was the default right?" answerable from stored data
// rather than from argument. LoneDissent marks exactly the set the default let through and
// fail-any would have blocked — the population to review in a fortnight.
func TestReviewStatsMarkTheSetTheDefaultLetThrough(t *testing.T) {
	lanes := []LaneResult{
		lr("a", surface.VerdictFail, f("x.go", 1, "maybe wrong")),
		lr("b", surface.VerdictPass),
	}
	st := SummarizeReview(lanes, FailOnAgreement, MergeFindings(lanes))
	if !st.LoneDissent {
		t.Error("a lone dissent among judges was not marked — this is the population that decides " +
			"whether the default is costing us real defects")
	}
	if st.Judged != 2 || st.Rejects != 1 {
		t.Errorf("stats = %+v", st)
	}

	// Two rejections is agreement, not a lone dissent — it blocked, so it is not in the question.
	st = SummarizeReview([]LaneResult{lr("a", surface.VerdictFail), lr("b", surface.VerdictFail)}, FailOnAgreement, nil)
	if st.LoneDissent {
		t.Error("two agreeing rejections were counted as a lone dissent")
	}

	// A blocked lane is not a judge, so a single judge rejecting is not a lone dissent either.
	st = SummarizeReview([]LaneResult{
		lr("a", surface.VerdictFail),
		{Lane: "b", Verdict: surface.VerdictBlocked},
	}, FailOnAgreement, nil)
	if st.LoneDissent {
		t.Error("a rejection with no second judge was counted as a lone dissent")
	}
	if st.Judged != 1 || st.Lanes != 2 {
		t.Errorf("stats = %+v — configured lanes and judging lanes are different facts", st)
	}
}
