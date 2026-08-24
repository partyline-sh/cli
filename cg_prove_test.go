package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"partyline.sh/partyline/internal/api"
)

// These are the four commands that were actually filed tonight. Each was written by a planner, read
// by a human, and shipped into a task — and not one of them could do what it claimed. The point of
// this test is that a criterion is only as good as running it.

func prove(t *testing.T, cmd, dir string) criterionProof {
	t.Helper()
	got := proveCriteria(t.TempDir(), []api.WorkItemCriterion{{Command: cmd, Direction: dir}}, 20*time.Second)
	if len(got) != 1 {
		t.Fatalf("expected one proof, got %d", len(got))
	}
	return got[0]
}

// `go test -run X` exits 0 when X matches NOTHING. Filed as "FAILS TODAY: the test does not exist",
// it passes before the work and would report success either way. This is what refused run 99bd49b7
// at the gate, after it had already been filed as ready.
func TestAnAcceptanceCheckThatCannotFailIsRejected(t *testing.T) {
	p := prove(t, "true", "acceptance")
	if p.ok() {
		t.Fatal("a command that PASSES today was accepted as an acceptance check — it cannot tell whether the work happened")
	}
	if !strings.Contains(proofReport([]criterionProof{p}), "PASSES on the base branch") {
		t.Fatal("the report must say the check already passes, not just that it failed")
	}
}

// The mirror image: `wc -l | grep -q '^0$'` never matches on macOS because BSD wc pads with spaces,
// so a guard over a perfectly green tree is red before anyone writes a line.
func TestAGuardThatIsAlreadyRedIsRejected(t *testing.T) {
	p := prove(t, "false", "guard")
	if p.ok() {
		t.Fatal("a command that FAILS today was accepted as a guard — it will fail the run for reasons unrelated to the task")
	}
	if !strings.Contains(proofReport([]criterionProof{p}), "protects nothing") {
		t.Fatal("the report must explain that an already-red guard protects nothing")
	}
}

func TestACorrectPairIsAccepted(t *testing.T) {
	acc := prove(t, "false", "acceptance") // fails today → correct
	grd := prove(t, "true", "guard")       // passes today → correct
	if !acc.ok() || !grd.ok() {
		t.Fatalf("a correct pair was rejected: acceptance=%+v guard=%+v", acc, grd)
	}
	if r := proofReport([]criterionProof{acc, grd}); r != "" {
		t.Fatalf("a correct pair produced a refusal:\n%s", r)
	}
}

// Prose criteria (adversarial / behavior review) carry no command and must be skipped, not treated
// as a failure — a human reviewing a diff is a real criterion that nothing can execute.
func TestProseCriteriaAreNotProbed(t *testing.T) {
	got := proveCriteria(t.TempDir(), []api.WorkItemCriterion{
		{Text: "a human confirms it looks right", Verify: "behavior review", Direction: "guard"},
	}, 5*time.Second)
	if len(got) != 0 {
		t.Fatalf("a criterion with no command was probed: %+v", got)
	}
}

// A command that hangs must not wedge planning. The refusal has to name the timeout rather than
// silently reporting a failure the planner cannot explain.
func TestAHangingCommandTimesOutAndSaysSo(t *testing.T) {
	p := prove(t, "sleep 30", "acceptance")
	if p.Err == nil {
		t.Fatal("a hanging command should report an error, not a verdict")
	}
	if !strings.Contains(proofReport([]criterionProof{p}), "could not run it") {
		t.Fatal("the report must say the command could not be run")
	}
}

// The refusal has to teach, because the planner that wrote the bad command is the one reading it.
func TestTheRefusalNamesTheTrapsAndForbidsShape(t *testing.T) {
	r := proofReport([]criterionProof{prove(t, "true", "acceptance")})
	for _, must := range []string{"ASSERT THE OUTCOME, NOT THE SHAPE", "go test -run X", "TestBootstrap", "BSD wc", "draft is KEPT"} {
		if !strings.Contains(r, must) {
			t.Fatalf("the refusal no longer mentions %q — it stops teaching the thing that caused this", must)
		}
	}
}

// THE UNIT WORKING IS NOT THE POINT — IT HAS TO BE WIRED.
//
// Removing the call from planning_finalize left every test above green, because they exercise
// proveCriteria and proofReport directly. A prover nothing calls is exactly the class of bug this
// whole change exists to stop: code that is correct, tested, and never reached.
//
// Asserted against the source because handleCall needs a live server, a thread and a control plane
// to invoke. Cruder than a behavioural test, and it catches the one mutation that matters.
func TestFinalizeActuallyProvesTheCriteria(t *testing.T) {
	src, err := os.ReadFile("cg_mcp.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	i := strings.Index(body, `case "planning_finalize":`)
	if i < 0 {
		t.Fatal("planning_finalize is gone from handleCall")
	}
	// Only the finalize arm counts: proving anywhere else does not stop a bad criterion being filed.
	arm := body[i:]
	if j := strings.Index(arm, `case "promote_work_item":`); j > 0 {
		arm = arm[:j]
	}
	if !strings.Contains(arm, "proveCriteria(") || !strings.Contains(arm, "proofReport(") {
		t.Fatal("planning_finalize no longer runs the criteria before filing — a command that cannot fail can be filed again")
	}
	// And it must REFUSE on a bad report, not merely mention it.
	if !strings.Contains(arm, "rep != \"\"") {
		t.Fatal("finalize computes a proof report but does not gate on it")
	}
}
