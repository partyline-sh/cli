package gate

import (
	"strings"
	"testing"

	"partyline.sh/partyline/internal/surface"
)

func lane(name, kind, status, code string, blocking bool) CheckResult {
	return CheckResult{Name: name, Kind: kind, Status: status, Code: code, Blocking: blocking}
}

// The verdict rules are the whole contract: everything downstream — quarantine, the merge train,
// the board column, the pause reason — reads this one value.
func TestFinalizeVerdicts(t *testing.T) {
	cases := []struct {
		name  string
		lanes []CheckResult
		want  string
		note  string
	}{
		{
			name: "nothing enabled",
			lanes: []CheckResult{
				lane("build", KindCheck, StatusSkip, surface.CodeSkipped, true),
			},
			want: surface.VerdictSkipped,
			note: "a repo with no gate has proved NOTHING — reporting that as a pass makes green meaningless",
		},
		{
			name: "everything passed",
			lanes: []CheckResult{
				lane("build", KindCheck, StatusPass, surface.CodeOK, true),
				lane("reviewer", KindReviewer, StatusPass, surface.CodeOK, true),
			},
			want: surface.VerdictPass,
		},
		{
			name: "a blocking lane failed",
			lanes: []CheckResult{
				lane("build", KindCheck, StatusFail, surface.CodeCheckFailed, true),
			},
			want: surface.VerdictFail,
			note: "the quarantine",
		},
		{
			name: "an ADVISORY lane failed",
			lanes: []CheckResult{
				lane("build", KindCheck, StatusPass, surface.CodeOK, true),
				lane("lint", KindCheck, StatusFail, surface.CodeCheckFailed, false),
			},
			want: surface.VerdictPass,
			note: "advisory means recorded, not blocking — this is what lets lint run without gating",
		},
		{
			name: "the provider throttled us",
			lanes: []CheckResult{
				lane("reviewer", KindReviewer, StatusFail, surface.CodeProviderRateLimited, true),
			},
			want: surface.VerdictBlocked,
			note: "never a pass, and never a FAIL either — the diff was never judged",
		},
		{
			name: "a reviewer we could not parse",
			lanes: []CheckResult{
				lane("reviewer", KindReviewer, StatusFail, surface.CodeReviewerUnparseable, true),
			},
			want: surface.VerdictFail,
			note: "fail-closed: an unreadable answer is not a pass",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := New()
			for _, l := range tc.lanes {
				r.Add(l)
			}
			if got := r.Finalize(); got != tc.want {
				t.Errorf("verdict = %q, want %q — %s", got, tc.want, tc.note)
			}
		})
	}
}

// Both can be true at once, and the order matters. A build that genuinely failed cannot be fixed
// by retrying, so parking the branch as `blocked` to wait for a quota would leave definitely-broken
// work waiting on something that changes nothing. Definite bad news outranks "we do not know".
func TestAHardFailureOutranksAThrottledLane(t *testing.T) {
	r := New()
	r.Add(lane("build", KindCheck, StatusFail, surface.CodeCheckFailed, true))
	r.Add(lane("reviewer", KindReviewer, StatusFail, surface.CodeProviderRateLimited, true))
	if got := r.Finalize(); got != surface.VerdictFail {
		t.Errorf("verdict = %q, want %q — a failed build is not waiting on a quota", got, surface.VerdictFail)
	}
	if r.Code != surface.CodeCheckFailed {
		t.Errorf("code = %q, want the hard failure's code so the UI names the real problem", r.Code)
	}
}

// pass_with_findings is a distinct outcome, not a cosmetic label: it merges AND carries notes onto
// the pull request. Collapsing it into pass loses the findings; collapsing it into fail blocks work
// that a reviewer explicitly did not block.
func TestFindingsWithoutAFailureAreNotAFailure(t *testing.T) {
	r := New()
	r.Add(lane("reviewer", KindReviewer, StatusPass, surface.CodeOK, true))
	r.Findings = []Finding{{Title: "this could be clearer", File: "a.go", Line: 12}}
	if got := r.Finalize(); got != surface.VerdictPassWithFindings {
		t.Fatalf("verdict = %q, want %q", got, surface.VerdictPassWithFindings)
	}
	if !r.OK() {
		t.Error("pass_with_findings must be allowed to merge — the reviewer chose not to block")
	}
}

// Add() derives Class from the declared code rather than trusting the caller, so a lane cannot
// record a retry disposition that disagrees with the vocabulary.
func TestAddDerivesClassFromTheCode(t *testing.T) {
	r := New()
	r.Add(CheckResult{Name: "x", Code: surface.CodeProviderTimeout, Class: "none" /* a lie */})
	if got := r.Checks[0].Class; got != string(surface.ClassTransient) {
		t.Errorf("class = %q, want %q — the caller's value must not win", got, surface.ClassTransient)
	}
	r.Add(CheckResult{Name: "y", Code: "something.undeclared"})
	if got := r.Checks[1].Class; got != string(surface.ClassHard) {
		t.Errorf("undeclared code class = %q, want hard — never silently retry an unknown failure", got)
	}
}

// Ran() is what the merge train reads. A repo with no checks must not auto-land: landing without a
// human is the trade for HAVING real checks, and a repo that defined none has not made that trade.
func TestRanIsFalseWhenEveryLaneSkipped(t *testing.T) {
	r := New()
	r.Add(lane("build", KindCheck, StatusSkip, surface.CodeSkipped, true))
	r.Add(lane("reviewer", KindReviewer, StatusSkip, surface.CodeSkipped, true))
	r.Finalize()
	if r.Ran() {
		t.Error("Ran() must be false when nothing was enabled")
	}
	if r.OK() {
		t.Error("a skipped gate must not report OK — that would auto-land unverified work")
	}
}

func TestReasonsAndWarningsSeparate(t *testing.T) {
	r := New()
	r.Add(CheckResult{Name: "build", Status: StatusFail, Code: surface.CodeCheckFailed, Blocking: true, Detail: "exit 1"})
	r.Add(CheckResult{Name: "lint", Status: StatusWarn, Code: surface.CodeCheckBaselineRed, Detail: "red on base too"})
	if got := r.Reasons(); !strings.Contains(got, "build: exit 1") {
		t.Errorf("Reasons() = %q, want the blocking failure", got)
	}
	if got := r.Reasons(); strings.Contains(got, "red on base") {
		t.Errorf("Reasons() leaked an advisory warning: %q", got)
	}
	if got := r.Warnings(); !strings.Contains(got, "red on base too") {
		t.Errorf("Warnings() = %q, want the advisory", got)
	}
}

func TestTruncateBoundsEvidence(t *testing.T) {
	// Counted in runes, not bytes: the ellipsis is a 3-byte character, and asserting on len()
	// measured the marker rather than the content it bounds.
	if got := Truncate(strings.Repeat("x", 100), 10); len([]rune(got)) > 11 {
		t.Errorf("Truncate did not bound: %d runes", len([]rune(got)))
	}
	if got := Truncate("short", 10); got != "short" {
		t.Errorf("Truncate mangled a short string: %q", got)
	}
}
