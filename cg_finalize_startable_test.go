package main

import (
	"testing"

	"partyline.sh/partyline/internal/api"
)

// A FINALIZED PLAN MUST BE STARTABLE. That is the entire promise of the gate — planning_finalize
// refuses until every required slot is filled and every open question is answered, so what it
// accepts is supposed to be runnable.
//
// It was not. asTree() never set Readiness, so every plan filed through the CLI door landed at 0,
// and web/src/lib/api/work-view.ts sets MIN_START_READINESS = 4 with startBlockedByReadiness()
// DISABLING the board card's Start button below it. Every item this door produced was un-startable,
// the item page had no readiness control to raise it, and nothing anywhere said why. A planning
// session in the web files items that start; this path filed items that could not.
//
// The floor is duplicated here deliberately rather than imported — it lives in TypeScript, and a Go
// test cannot read it. If the two drift, this comment is the trail.
const minStartReadiness = 4 // MUST match MIN_START_READINESS in web/src/lib/api/work-view.ts

func assertStartable(t *testing.T, n api.WorkTreeNode, path string) {
	t.Helper()
	if n.Readiness < minStartReadiness {
		t.Fatalf("%q filed at readiness %d — below the board's Start floor of %d, so its card's Start button is disabled",
			path, n.Readiness, minStartReadiness)
	}
	for _, c := range n.Children {
		assertStartable(t, c, path+"/"+c.Title)
	}
}

func TestAFinalizedPlanIsStartable(t *testing.T) {
	d := &planDraft{Thread: "t", Title: "Gate the production deploy", Document: "change deploy-prod.yml"}
	assertStartable(t, d.asTree(), "root")
}

// A PLAN FILES ONE CARD. The tree is gone: an epic used to become several cards, each running in
// its own worktree forked from the base branch, unable to see each other's work — sequential in
// time and parallel in code, which is what produced the merge collisions and the drift.
func TestAPlanFilesExactlyOneCard(t *testing.T) {
	for _, kind := range []string{"task", "feature", "epic"} {
		d := &planDraft{Thread: "t", Kind: kind, Title: "Environment pipeline",
			Document: "do the thing", Criteria: []api.WorkItemCriterion{{Text: "it works"}}}
		tree := d.asTree()
		if len(tree.Children) != 0 {
			t.Errorf("kind %q filed %d child card(s); a plan files one", kind, len(tree.Children))
		}
		if tree.Readiness != planFiledReadiness {
			t.Errorf("kind %q filed at readiness %d, want %d — below the board's floor it cannot be started",
				kind, tree.Readiness, planFiledReadiness)
		}
	}
}
