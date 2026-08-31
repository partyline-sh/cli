package main

import (
	"os"
	"strings"
	"testing"
)

// EVERY PATH THAT AUTHORS ACCEPTANCE CRITERIA MUST ASK FOR A RUNNABLE COMMAND.
//
// The gate that decides whether a work item can be Started (specificity.ts, `provable`) requires at
// least one criterion with direction="acceptance" AND a non-empty command. That requirement landed on
// 2026-08-19. The authoring paths were not all updated with it, and the failure is silent in the worst
// possible way: the planning conversation goes well, the item files, it shows 5/5 on the board, and
// only START refuses — by which point the human has lost the whole session's work.
//
// It cost six days. The web planning agent was told to emit {text, verify} and nothing else, so
// EVERY task it produced was structurally unstartable; the party plan tools had the same schema.
//
// This is a ratchet, not a style check. A new authoring surface that forgets `command` is not a
// cosmetic omission — it is a surface that can only produce dead work items.
func TestEveryCriteriaAuthoringSurfaceAsksForARunnableCommand(t *testing.T) {
	for _, f := range []struct {
		path, what, anchor string
		span               int // how far past the anchor the definition runs — a JSX form is far wordier than a schema
	}{
		{"cg_mcp.go", "the CLI planning tools (planning_note, propose_work_item)", "acceptance_criteria", 2500},
		{"party_mcp.go", "the party plan tools (plan_propose, plan_upsert)", "acceptance_criteria", 2500},
		{"web/src/lib/api/party-modes.ts", "the web planning agent's persona", "acceptance_criteria", 2500},
		{"web/src/components/plan-editor.tsx", "the human's criteria editor", "Acceptance criteria", 6000},
	} {
		src, err := os.ReadFile(f.path)
		if err != nil {
			t.Errorf("%s (%s) is missing", f.path, f.what)
			continue
		}
		// SCOPED to the criteria definition, not the whole file. A file-wide substring match passes
		// on any unrelated use of the word — the first version of this test did exactly that and a
		// mutation (deleting the party schema's direction field) did not fail it.
		body := string(src)
		at := strings.Index(body, f.anchor)
		if at < 0 {
			t.Errorf("%s (%s) no longer defines acceptance criteria at all", f.path, f.what)
			continue
		}
		end := at + f.span
		if end > len(body) {
			end = len(body)
		}
		block := body[at:end]
		for _, field := range []string{"command", "direction", "acceptance"} {
			if !strings.Contains(block, field) {
				t.Errorf("%s (%s): its acceptance-criteria definition never mentions %q — anything it authors cannot be Started",
					f.path, f.what, field)
			}
		}
	}
}

// The two agent-facing schemas must SAY what happens if the command is missing. A schema that lists
// the field without the consequence gets treated as optional — which is exactly how the party tools
// shipped with it absent and nobody noticed for six days.
func TestAgentSchemasStateTheConsequenceOfOmittingACommand(t *testing.T) {
	for _, f := range []string{"cg_mcp.go", "party_mcp.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		body := strings.ToLower(string(src))
		if !strings.Contains(body, "cannot be started") {
			t.Errorf("%s never tells the agent that a criterion without a command makes the item unstartable", f)
		}
	}
}

// A DOWNSTREAM LIMIT THE UPSTREAM AUTHOR IS NEVER TOLD ABOUT IS A TRAP.
//
// This is the same defect as the missing `command`, in a second place. A task's folded text is capped
// at MAX_TASK_LEN (4000) and refused at Start — but two of the three planners had never heard of the
// cap, so they would write a good task, file it, and the human found out only when they tried to run
// it, with the planning conversation already over.
//
// The web persona did state a budget ("well under 3500 characters"), which is what made its breakage
// so clear when 630 characters of OUR OWN footer were added to every task: a planner correctly
// hitting its stated target started folding past the cap. That is why the footer no longer counts
// (authoredTaskLen) AND why the number has to be stated everywhere it is enforced.
func TestEveryPlannerIsToldTheTaskLengthCap(t *testing.T) {
	for _, f := range []struct{ path, what string }{
		{"cg_mcp.go", "the CLI planner"},
		{"party_mcp.go", "the party plan tools"},
		{"web/src/lib/api/party-modes.ts", "the web planning agent"},
	} {
		src, err := os.ReadFile(f.path)
		if err != nil {
			t.Errorf("%s (%s) is missing", f.path, f.what)
			continue
		}
		body := string(src)
		// Either the cap itself or a stated character budget under it — what matters is that the
		// author is given a number to aim at, not that they all quote the same one.
		if !strings.Contains(body, "4000") && !strings.Contains(body, "3500") {
			t.Errorf("%s (%s) never states a task-length budget — it can write a task that is refused at Start, "+
				"after the planning conversation is over", f.path, f.what)
		}
	}
}

// Our own explanatory footer must not be charged to the author. It is fixed overhead they cannot see
// or shorten, it moves whenever we edit our prose, and at 630 characters on a 4000 budget it silently
// ate the headroom the web planner was explicitly aiming for.
func TestTheLengthCapMeasuresAuthoredContentOnly(t *testing.T) {
	src, err := os.ReadFile("web/src/lib/api/work-items.ts")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !strings.Contains(body, "export function authoredTaskLen") {
		t.Fatal("authoredTaskLen is gone — the cap is charging authors for our own footer again")
	}
	// endsWith() was the first version and it was wrong: a GitHub-sourced item appends "Closes <url>"
	// AFTER the criteria block, so the footer is not last and those items were charged for it.
	if strings.Contains(body, "full.endsWith(CHECKED_BY_NOTE)") {
		t.Error("authoredTaskLen uses endsWith — a ticket-sourced item is charged for the footer again")
	}
}
