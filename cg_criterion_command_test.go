package main

import (
	"encoding/json"
	"strings"
	"testing"

	"partyline.sh/partyline/internal/api"
)

// THE BUG THIS PINS. api.WorkItemCriterion carried only {Text, Verify}. The specificity gate's
// `provable` check is
//
//	criteria.some(c => coerceDirection(c.direction, c.text) === "acceptance" && !!c.command?.trim())
//
// so a criterion that cannot carry a command can never satisfy it. planning_finalize applies that
// same gate, which made the documented path — planning_open → planning_note → planning_finalize —
// structurally dead-ended: every draft failed `provable` no matter what was written, and no task
// filed from the CLI could ever be Started.
//
// The server had accepted `command` and `direction` all along (coerceCriteria in
// web/src/lib/api/work-items.ts stores both). Only the Go type was never widened, so the fields
// were silently dropped at the client boundary — the two halves of one contract drifting apart.
func TestCriterionCarriesCommandAndDirection(t *testing.T) {
	// Exactly the shape an agent sends to planning_note / propose_work_item.
	raw := `[{"text":"schedule tests pass","verify":"executable check","direction":"acceptance","command":"cd web && npx vitest run src/lib/api/schedule.test.ts"}]`

	var got []api.WorkItemCriterion
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 criterion, got %d", len(got))
	}
	if got[0].Command == "" {
		t.Fatal("command was DROPPED at the client boundary — the provable gate can never be satisfied, so planning_finalize can never file a startable task")
	}
	if got[0].Direction != "acceptance" {
		t.Fatalf("direction was dropped or mangled: %q", got[0].Direction)
	}
	if !strings.Contains(got[0].Command, "vitest") {
		t.Fatalf("command survived but wrong: %q", got[0].Command)
	}
}

// And it has to survive the trip BACK OUT, or CheckSpecificity is asked to judge a criterion whose
// command it cannot see — which is how the gate would report `provable` false on a task that
// genuinely had one.
func TestCriterionRoundTripsToTheServer(t *testing.T) {
	in := []api.WorkItemCriterion{{
		Text: "schedule tests pass", Verify: "executable check",
		Direction: "acceptance", Command: "cd web && npx vitest run src/lib/api/schedule.test.ts",
	}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"command":`, `"direction":`, "vitest", "acceptance"} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("marshalled criterion is missing %s — the server would never see it:\n%s", want, b)
		}
	}
}

// omitempty is deliberate: a review-only criterion legitimately has no command, and emitting
// `"command":""` would make an empty string look like an answer to a gate that tests for one.
func TestReviewOnlyCriterionOmitsTheEmptyCommand(t *testing.T) {
	b, err := json.Marshal([]api.WorkItemCriterion{{Text: "a human reads the diff", Verify: "adversarial review"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"command"`) {
		t.Fatalf("empty command should be omitted, not sent as \"\":\n%s", b)
	}
}
