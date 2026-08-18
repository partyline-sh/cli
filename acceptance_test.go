package main

import (
	"strings"
	"testing"
	"time"

	"partyline.sh/partyline/internal/api"
)

const pfTimeout = 20 * time.Second

// RED BEFORE GREEN. An acceptance check that already passes cannot prove the work happened — it
// would report success either way. That is not theoretical: "Baseline the numbered migrations"
// carried an executable check, passed it, and still reached a reviewer with its core deliverable
// missing, because the check was green at BOTH ends.

func TestATaskWhoseAcceptanceAlreadyPassesIsStopped(t *testing.T) {
	p := preflightAcceptance(t.TempDir(), []api.RunAcceptanceCheck{
		{Command: "true", Direction: "acceptance", Text: "the thing works"},
	}, pfTimeout)

	if p.blocked == "" {
		t.Fatal("a task whose acceptance check already passes was allowed to start — it can never prove itself")
	}
	// The message is read INSTEAD of a result, so it has to name the check and both ways out.
	for _, want := range []string{"ALREADY PASSES", "the thing works", "already done", "GUARD"} {
		if !strings.Contains(p.blocked, want) {
			t.Errorf("the refusal never mentions %q:\n%s", want, p.blocked)
		}
	}
}

func TestARedAcceptanceCheckIsExactlyWhatWeWant(t *testing.T) {
	p := preflightAcceptance(t.TempDir(), []api.RunAcceptanceCheck{
		{Command: "false", Direction: "acceptance", Text: "the thing works"},
	}, pfTimeout)
	if p.blocked != "" {
		t.Errorf("a correctly-failing acceptance check blocked the task: %s", p.blocked)
	}
	if p.ran != 1 {
		t.Errorf("ran = %d, want 1", p.ran)
	}
}

// A guard is the opposite contract: it must be green now and green later. Green now is silent.
func TestAPassingGuardIsSilent(t *testing.T) {
	p := preflightAcceptance(t.TempDir(), []api.RunAcceptanceCheck{
		{Command: "true", Direction: "guard", Text: "the suite still passes"},
	}, pfTimeout)
	if p.blocked != "" {
		t.Errorf("a passing guard blocked the task — guards are SUPPOSED to be green: %s", p.blocked)
	}
	if len(p.warns) != 0 {
		t.Errorf("a passing guard warned about nothing: %v", p.warns)
	}
}

// A guard that is ALREADY red is pre-existing breakage. Refusing the task would punish it for
// someone else's regression — but staying silent lets the worker read its own red as self-inflicted.
func TestAnAlreadyFailingGuardWarnsRatherThanBlocks(t *testing.T) {
	p := preflightAcceptance(t.TempDir(), []api.RunAcceptanceCheck{
		{Command: "false", Direction: "guard", Text: "the suite still passes"},
	}, pfTimeout)
	if p.blocked != "" {
		t.Errorf("pre-existing breakage blocked an unrelated task: %s", p.blocked)
	}
	if len(p.warns) != 1 || !strings.Contains(p.warns[0], "pre-existing") {
		t.Errorf("a pre-existing guard failure was not reported as pre-existing: %v", p.warns)
	}
}

// THE ASYMMETRY. Fail-closed on "it passed"; fail-open on "could not tell". Blocking a real task
// because our own check timed out is a worse outcome than missing one bad task.
func TestAnUnrunnableCheckNeverBlocks(t *testing.T) {
	p := preflightAcceptance(t.TempDir(), []api.RunAcceptanceCheck{
		{Command: "sleep 5", Direction: "acceptance", Text: "slow"},
	}, 300*time.Millisecond)
	if p.blocked != "" {
		t.Errorf("a check that could not finish blocked the task: %s", p.blocked)
	}
	if len(p.warns) != 1 || !strings.Contains(p.warns[0], "proves nothing") {
		t.Errorf("a timed-out check was not reported as inconclusive: %v", p.warns)
	}
}

// Mixed sets are the normal case: one acceptance plus some guards. One already-green acceptance is
// enough to stop the task, whatever else is present.
func TestOneGreenAcceptanceStopsATaskWithGuards(t *testing.T) {
	p := preflightAcceptance(t.TempDir(), []api.RunAcceptanceCheck{
		{Command: "true", Direction: "guard", Text: "still builds"},
		{Command: "true", Direction: "acceptance", Text: "the new endpoint answers"},
		{Command: "false", Direction: "acceptance", Text: "the other new thing"},
	}, pfTimeout)
	if p.blocked == "" {
		t.Fatal("an already-green acceptance check was masked by the others")
	}
	if !strings.Contains(p.blocked, "the new endpoint answers") {
		t.Errorf("the refusal does not name WHICH check was already green:\n%s", p.blocked)
	}
	if strings.Contains(p.blocked, "the other new thing") {
		t.Error("a correctly-red check was reported as a problem")
	}
}

// No checks at all must behave exactly as the loop did before this existed.
func TestNoChecksIsNotAFailure(t *testing.T) {
	p := preflightAcceptance(t.TempDir(), nil, pfTimeout)
	if p.blocked != "" || len(p.warns) != 0 || p.ran != 0 {
		t.Errorf("an empty check set was not a no-op: %+v", p)
	}
	// A criterion with prose but no command is a reviewer's job, not a runner's.
	p = preflightAcceptance(t.TempDir(), []api.RunAcceptanceCheck{{Command: "  ", Direction: "acceptance", Text: "looks nice"}}, pfTimeout)
	if p.blocked != "" || p.ran != 0 {
		t.Errorf("a prose-only criterion was treated as runnable: %+v", p)
	}
}
