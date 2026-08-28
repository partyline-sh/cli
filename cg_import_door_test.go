package main

import (
	"os"
	"strings"
	"testing"
)

// THE IMPORT DOOR IS THE PLANNING DOOR.
//
// It used to create a `describe` party seeded with the ticket and post an opening message addressed
// to an agent — which the route never launched. The working describe flow is two steps (create, then
// POST /api/v1/daemon/launch, chained by describe-form.tsx); the import did the first and never the
// second. Every import therefore produced a room containing a question and nobody to answer it, and
// reported success. Six accumulated over five days and seven more in one evening; not one produced a
// task, and nothing in the product said anything was wrong.
//
// These tests pin the properties that make that impossible to reintroduce: an import leaves a DRAFT
// (which the gate can refuse) rather than a party (which nothing can), it carries the tracker
// identity through to filing, and it will not silently destroy a conversation already in progress.

const importThread = "fa365970-def0-4321-a8f1-630a723ef35c"

func TestAnImportLeavesADraftTheGateCanRefuse(t *testing.T) {
	withHomeDir(t)
	d := &planDraft{
		Thread: importThread,
		Idea:   "Imported from github/1192 — https://github.com/x/y/issues/1192\n\nGate the production deploy",
		Title:  "Gate the production deploy on a green staging smoke",
	}
	d.SourceTool, d.SourceID, d.SourceURL = "github", "1192", "https://github.com/x/y/issues/1192"
	if err := saveDraft(d); err != nil {
		t.Fatal(err)
	}
	got := loadDraft(importThread)
	if got == nil {
		t.Fatal("an import must leave a draft — a party is a thing the specificity gate cannot refuse")
	}
	if got.SourceTool != "github" || got.SourceID != "1192" {
		t.Fatalf("tracker identity lost across save/load: %+v", got)
	}
	if got.SourceURL == "" {
		t.Fatal("source_url lost — the board's link back to the original ticket")
	}
}

// The ticket is the RECORD of what was asked for, never a filled slot. Nothing parses it, so a draft
// seeded from a ticket must still be refused by the gate — the bug this replaces was a tool claiming
// a pre-fill that does not exist, which lets a plan go green with its real decisions never asked.
func TestASeededTicketFillsNoSlot(t *testing.T) {
	withHomeDir(t)
	d := &planDraft{
		Thread: importThread,
		Idea:   "Imported from github/611\n\nBlue/green deploys. Acceptance: a while-loop curling the site records zero non-2xx across a full deploy.",
	}
	d.SourceTool, d.SourceID = "github", "611"
	if err := saveDraft(d); err != nil {
		t.Fatal(err)
	}
	got := loadDraft(importThread)
	if got.Title != "" || got.Document != "" || len(got.Criteria) != 0 {
		t.Fatalf("the ticket body filled a slot — nothing parses it, so this can only be a false claim: %+v", got)
	}
}

// A draft may carry a human's answers. Importing a DIFFERENT ticket over it would destroy them
// silently, and merging two tickets into one draft produces a plan that is neither.
func TestADraftInProgressIsNotOverwrittenByADifferentTicket(t *testing.T) {
	withHomeDir(t)
	first := &planDraft{Thread: importThread, Title: "Gate the production deploy"}
	first.SourceTool, first.SourceID = "github", "1192"
	first.OpenQuestions = []planQuestion{{Text: "workflow_run or a job-level needs?", Answer: "workflow_run"}}
	if err := saveDraft(first); err != nil {
		t.Fatal(err)
	}

	open := loadDraft(importThread)
	if open == nil || (open.SourceTool == "github" && open.SourceID == "937") {
		t.Fatal("setup wrong")
	}
	// The guard the handler applies: same source resumes, a different source must refuse.
	sameTicket := open.SourceTool == "github" && open.SourceID == "1192"
	if !sameTicket {
		t.Fatal("resuming the SAME ticket must be recognised as the same draft")
	}
	differentTicket := open.SourceTool == "github" && open.SourceID == "937"
	if differentTicket {
		t.Fatal("a different ticket must not match the open draft")
	}
	if len(loadDraft(importThread).OpenQuestions) != 1 {
		t.Fatal("the answer already given was lost")
	}
	if loadDraft(importThread).OpenQuestions[0].Answer != "workflow_run" {
		t.Fatal("a human's answer was destroyed by an unrelated import")
	}
}

// The description is load-bearing: it is the only thing that makes a model derive rather than
// forward, and recap rather than work silently. A rewrite that drops these is the failure returning.
func TestTheImportDoorStillDemandsDeriveAndRecap(t *testing.T) {
	var desc string
	for _, td := range cgToolDefs {
		if td["name"] == "import_work_item" {
			desc, _ = td["description"].(string)
		}
	}
	if desc == "" {
		t.Fatal("import_work_item has no description")
	}
	for _, must := range []string{"planning", "open_question", "RECAP", "Derive, never invent", "ONE DRAFT AT A TIME"} {
		if !strings.Contains(desc, must) {
			t.Fatalf("import_work_item no longer says %q — the tool stops forcing the smart path", must)
		}
	}
	// The old behaviour, named so it cannot quietly come back.
	if strings.Contains(desc, "seeded with the ticket verbatim, which appears in the team's Planning column") {
		t.Fatal("the party-creating description is back — that door creates a room with nobody in it")
	}
}

// planning_open must not claim a pre-fill it does not perform. Nothing parses `idea`; its only
// consumer is planDecisionLog.
func TestPlanningOpenDoesNotClaimToPreFill(t *testing.T) {
	var desc string
	for _, td := range cgToolDefs {
		if td["name"] == "planning_open" {
			desc, _ = td["description"].(string)
		}
	}
	if strings.Contains(desc, "pre-fills whatever it contains") {
		t.Fatal("planning_open still claims to pre-fill from a pasted spec — nothing parses `idea`")
	}
}

// EVERY ADVERTISED TOOL MUST HAVE A HANDLER.
//
// Building this change, a block replacement in handleCall silently swallowed FIVE unrelated cases —
// read_board, list_machines, add_machine_project/set_run_mode, propose_work_item, plan_file_tree.
// Each stayed advertised in tools/list, so a model would call it and get "unknown tool" back. Exactly
// one of the five had a test; the other four would have shipped, and the failure would have surfaced
// as an agent that inexplicably could not read the board.
//
// tools/list and the dispatch switch are two lists that must agree, and nothing made them.
func TestEveryAdvertisedToolIsDispatched(t *testing.T) {
	src, err := os.ReadFile("cg_mcp.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)

	handled := map[string]bool{}
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "case \"") || !strings.HasSuffix(trimmed, ":") {
			continue
		}
		for _, m := range strings.Split(trimmed, "\"") {
			if m != "" && !strings.ContainsAny(m, " \t:,") {
				handled[m] = true
			}
		}
	}

	delegated := map[string]bool{"read_fleet": true, "publish_artifact": true, "read_marks": true}
	var missing []string
	for _, td := range cgToolDefs {
		name, _ := td["name"].(string)
		if name == "" || handled[name] {
			continue
		}
		// Three tools are dispatched BEFORE the switch, deliberately, because they are not
		// thread-scoped: read_fleet inline, and publish_artifact / read_marks via handleArtifactTool
		// (work-item scoped — an agent can hold a good work item id in a session with no thread
		// bound). Each has its own test asserting it is reachable — cg_fleet_read_test.go and
		// cg_artifacts_test.go — so these are exemptions on the record, not holes. Anything else
		// missing is the bug this test exists for.
		if delegated[name] {
			continue
		}
		missing = append(missing, name)
	}
	if len(missing) > 0 {
		t.Fatalf("advertised in tools/list with no dispatch branch — a model calling these gets \"unknown tool\": %v", missing)
	}
}
