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

// The leaves are what actually run. A container filed above the floor whose task leaves sit at 0
// looks fine on the board and refuses every Start.
func TestEveryLeafOfADecompositionIsStartable(t *testing.T) {
	d := &planDraft{
		Thread: "t", Kind: "epic", Title: "Environment pipeline",
		Children: []api.WorkTreeNode{{
			Kind: "feature", Title: "config",
			Children: []api.WorkTreeNode{{Kind: "task", Title: "ordered environments"}},
		}},
	}
	assertStartable(t, d.asTree(), "epic")
}

// An explicit score is a judgement and must survive. Only an UNSET one (0) is the omission being
// fixed — silently overwriting a deliberate 2 would hide a plan someone marked as not ready.
func TestAnExplicitChildScoreIsNotOverwritten(t *testing.T) {
	d := &planDraft{Thread: "t", Kind: "epic", Title: "root",
		Children: []api.WorkTreeNode{{Kind: "task", Title: "deliberately not ready", Readiness: 2}}}
	got := d.asTree().Children
	if len(got) != 1 || got[0].Readiness != 2 {
		t.Fatalf("an explicit readiness of 2 was overwritten: %+v", got)
	}
}
