package main

import (
	"os"
	"path/filepath"
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
	if !strings.Contains(proofReport("/tmp/repo", []criterionProof{p}), "PASSES on the base branch") {
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
	if !strings.Contains(proofReport("/tmp/repo", []criterionProof{p}), "protects nothing") {
		t.Fatal("the report must explain that an already-red guard protects nothing")
	}
}

func TestACorrectPairIsAccepted(t *testing.T) {
	acc := prove(t, "false", "acceptance") // fails today → correct
	grd := prove(t, "true", "guard")       // passes today → correct
	if !acc.ok() || !grd.ok() {
		t.Fatalf("a correct pair was rejected: acceptance=%+v guard=%+v", acc, grd)
	}
	if r := proofReport("/tmp/repo", []criterionProof{acc, grd}); r != "" {
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
	if !strings.Contains(proofReport("/tmp/repo", []criterionProof{p}), "could not run it") {
		t.Fatal("the report must say the command could not be run")
	}
}

// The refusal has to teach, because the planner that wrote the bad command is the one reading it.
func TestTheRefusalNamesTheTrapsAndForbidsShape(t *testing.T) {
	r := proofReport("/tmp/repo", []criterionProof{prove(t, "true", "acceptance")})
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

// THE PROBES MUST RUN IN THE PLAN'S REPO, NOT THE SESSION'S DIRECTORY.
//
// The first version used os.Getwd(), and it was wrong within an hour of shipping — found by
// dogfooding it rather than by a test. The MCP server inherits the SESSION's working directory, and
// a session is frequently rooted somewhere other than the repository its work targets. Probing a
// guard of `go build ./...` in a directory with no go.mod returns exit 1, so a perfectly good plan
// was refused on the grounds that its guard was "already red". The refusal was correct; the
// directory was not — which is the worst shape of bug this file can have, since it makes the tool
// confidently wrong about someone else's work.
func TestTheProbesRunInThePlansRepoNotTheSessionDirectory(t *testing.T) {
	withHomeDir(t)

	// A registry entry pointing at a real git repo, as the daemon would have written it.
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	reg := daemonRegistry{Projects: []daemonProject{{Label: "the-project", Path: repo}}}
	if err := saveDaemonRegistry(reg); err != nil {
		t.Fatal(err)
	}

	if got := repoDirForProof("the-project"); got != repo {
		t.Fatalf("probes would run in %q, not the project's own repo %q — a guard would be judged against the wrong tree", got, repo)
	}
}

// WHERE the probes run decides whether they mean anything, so resolution is deliberately asymmetric.
//
// This test used to assert that an unknown label falls back to the working directory. That fallback
// was silently catastrophic: the commands ran in whatever repo the session happened to sit in, where
// an acceptance command fails because the code is ABSENT rather than because the work is undone —
// and failing is exactly what the gate wants to see before the work. Two plans were certified that
// way, their checks never once executed against the code they targeted. Proving nothing and saying
// so beats proving something somewhere else.
func TestProofDirFallsBackOnlyWhenNoProjectWasNamed(t *testing.T) {
	withHomeDir(t)
	wd, _ := os.Getwd()

	// No label: a plain CLI session inside a repo IS its own answer. Unchanged.
	if dir, how := resolveProofDir(""); dir != wd || how != proofDirCwd {
		t.Errorf("with no project named, probes should run in the working directory; got %q (%s)", dir, how)
	}

	// A label nobody registered: resolve to NOTHING rather than somewhere unrelated.
	if dir, how := resolveProofDir("never-registered"); dir != "" || how != proofDirUnresolved {
		t.Errorf("an unknown project must not resolve to a directory, got %q (%s)", dir, how)
	}

	// A registered project whose directory is gone — stale registry, common after a move.
	if err := saveDaemonRegistry(daemonRegistry{Projects: []daemonProject{{Label: "moved", Path: filepath.Join(t.TempDir(), "gone")}}}); err != nil {
		t.Fatal(err)
	}
	if dir, how := resolveProofDir("moved"); dir != "" || how != proofDirUnresolved {
		t.Errorf("a stale registry entry must not resolve to a directory, got %q (%s)", dir, how)
	}
}

// The refusal has to say WHERE it ran, or a wrong-directory verdict is indistinguishable from a real
// one — which is how three correct guards in a row were reported as already-red.
func TestProofReportNamesTheDirectoryItRanIn(t *testing.T) {
	p := criterionProof{Command: "cd web && true", Want: "pass today", Got: 1}
	if r := proofReport("/somewhere/else", []criterionProof{p}); !strings.Contains(r, "/somewhere/else") {
		t.Errorf("the refusal does not say where the commands ran:\n%s", r)
	}
}

// planning_open must CARRY the project label into the draft. It accepted only `idea` and `title`, so
// every plan opened (rather than imported) had an empty RepoLabel and got proved in the session's
// working directory — a different repository. This asserts the wiring exists at all three points:
// the tool advertises `repo`, the handler reads it, and it lands on the draft.
func TestPlanningOpenCarriesTheProjectLabel(t *testing.T) {
	src, err := os.ReadFile("cg_mcp.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	schema := body[strings.Index(body, `"planning_open"`):]
	if i := strings.Index(schema, `"planning_note"`); i > 0 {
		schema = schema[:i]
	}
	if !strings.Contains(schema, `"repo":`) {
		t.Error("planning_open does not advertise `repo`, so a caller has no way to say which project the plan is about")
	}
	handler := body[strings.Index(body, `case "planning_open":`):]
	if i := strings.Index(handler, `case "planning_note":`); i > 0 {
		handler = handler[:i]
	}
	if !strings.Contains(handler, `json:"repo"`) {
		t.Error("planning_open's arguments struct does not read `repo` — a caller could send it and it would be silently dropped")
	}
	if !strings.Contains(handler, "d.RepoLabel = ") {
		t.Error("planning_open never stores the project label on the draft, so finalize cannot find the repo to prove in")
	}
}
