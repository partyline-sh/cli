package main

import (
	"testing"

	"partyline.sh/partyline/internal/gate"
	"partyline.sh/partyline/internal/surface"
)

// G.0 rests on one claim: the legacy verifyResult fields are a PROJECTION of the typed report, not
// a second opinion. crank.go still branches on ran/ok — quarantine, the merge train, the repair
// loop — while the control plane will branch on the report's verdict. The moment those two
// disagree, a task is quarantined locally and reported as passing (or the reverse), and nothing
// would catch it.
//
// So: drive the same lane combinations through both and assert they agree.
func TestReportAgreesWithTheLegacyProjection(t *testing.T) {
	pass := verifyResult{ran: true, ok: true, code: surface.CodeOK}
	skip := verifyResult{}
	warn := verifyResult{ran: true, ok: true, code: surface.CodeCheckBaselineRed, warn: "red on base too"}
	failHard := verifyResult{ran: true, ok: false, code: surface.CodeCheckFailed, reasons: "exit 1"}
	failTransient := verifyResult{ran: true, ok: false, code: surface.CodeProviderRateLimited, reasons: "throttled"}

	cases := []struct {
		name                string
		checks, rev, vis    verifyResult
		legacyRan, legacyOK bool
	}{
		{"nothing configured", skip, skip, skip, false, false},
		{"checks only, clean", pass, skip, skip, true, true},
		{"all three clean", pass, pass, pass, true, true},
		{"checks red on base — advisory", warn, pass, skip, true, true},
		{"build failed", failHard, skip, skip, true, false},
		{"reviewer rejected", pass, failHard, skip, true, false},
		{"visual rejected", pass, pass, failHard, true, false},
		{"reviewer throttled", pass, failTransient, skip, true, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := buildReport(tc.checks, tc.rev, tc.vis)
			if got := r.Ran(); got != tc.legacyRan {
				t.Errorf("report.Ran() = %v, legacy ran = %v", got, tc.legacyRan)
			}
			if got := r.OK(); got != tc.legacyOK {
				t.Errorf("report.OK() = %v, legacy ok = %v (verdict %q)", got, tc.legacyOK, r.Verdict)
			}
		})
	}
}

// A lane that never ran must be recorded, not dropped. "We stopped early because the build failed"
// and "this repo has no reviewer" are different facts, and only the second one should ever be read
// as an argument for configuring a reviewer.
func TestSkippedLanesAreRecorded(t *testing.T) {
	r := buildReport(verifyResult{ran: true, ok: true, code: surface.CodeOK}, verifyResult{}, verifyResult{})
	if len(r.Checks) != 3 {
		t.Fatalf("got %d lanes, want 3 (a skipped lane is still a lane)", len(r.Checks))
	}
	for _, c := range r.Checks[1:] {
		if c.Status != gate.StatusSkip || c.Code != surface.CodeSkipped {
			t.Errorf("lane %q: status=%q code=%q, want skip/skipped", c.Name, c.Status, c.Code)
		}
	}
}

// The advisory rule, asserted at the boundary where it is actually decided. A check that is red on
// the BASE branch too is pre-existing debt this diff did not introduce — quarantining for it
// rejects perfect work every time, which is the run-dbf167b5 lesson that 37 pre-existing lint
// errors failed a clean diff.
func TestAWarnLaneNeverQuarantines(t *testing.T) {
	warn := verifyResult{ran: true, ok: true, code: surface.CodeCheckBaselineRed, warn: "lint fails on base too"}
	r := buildReport(warn, verifyResult{ran: true, ok: true, code: surface.CodeOK}, verifyResult{})
	if !r.OK() {
		t.Fatalf("verdict %q — an advisory must not block", r.Verdict)
	}
	for _, c := range r.Checks {
		if c.Status == gate.StatusWarn && c.Blocking {
			t.Errorf("lane %q is a warning but still marked blocking", c.Name)
		}
	}
	if got := r.Warnings(); got == "" {
		t.Error("the advisory vanished — it must still be surfaced on the task")
	}
}

// The code is set at each return site rather than inferred from prose. This pins the one place
// that judgment is still made from text: telling "the reviewer said no" apart from "the reviewer
// did not answer in a form we could read", which are a code defect and a harness defect.
func TestReviewerCodeDistinguishesRejectionFromNonAnswer(t *testing.T) {
	if got := reviewerCode(true, ""); got != surface.CodeOK {
		t.Errorf("pass → %q, want %q", got, surface.CodeOK)
	}
	if got := reviewerCode(false, "reviewer: FAIL — misses the second case"); got != surface.CodeReviewerRejected {
		t.Errorf("rejection → %q, want %q", got, surface.CodeReviewerRejected)
	}
	if got := reviewerCode(false, "reviewer: no parseable VERDICT in the reply (fail-closed)"); got != surface.CodeReviewerUnparseable {
		t.Errorf("non-answer → %q, want %q", got, surface.CodeReviewerUnparseable)
	}
}

func TestChecksCodeMarksBaselineRed(t *testing.T) {
	if got := checksCode(nil); got != surface.CodeOK {
		t.Errorf("clean → %q, want %q", got, surface.CodeOK)
	}
	if got := checksCode([]string{"lint fails on base"}); got != surface.CodeCheckBaselineRed {
		t.Errorf("baseline red → %q, want %q", got, surface.CodeCheckBaselineRed)
	}
}
