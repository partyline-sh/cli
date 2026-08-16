package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"partyline.sh/partyline/internal/api"
)

// Planning mode is a state machine whose whole value is that it CANNOT be walked away from. The
// failure modes are all quiet ones: a draft that vanishes when the model forgets it was planning, a
// merge that wipes earlier answers, a status that hands over every remaining question at once, a
// machine auto-picked because "there was probably only one".

func withHomeDir(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

func TestADraftSurvivesTheModelForgettingItExists(t *testing.T) {
	withHomeDir(t)
	const thread = "fa365970-def0-4321-a8f1-630a723ef35c"

	d := &planDraft{Thread: thread, Idea: "make the fleet page readable", Title: "Fleet readability"}
	d.Criteria = []api.WorkItemCriterion{{Text: "npm run build exits 0", Verify: "executable check"}}
	if err := saveDraft(d); err != nil {
		t.Fatal(err)
	}

	// A new process — a compaction, a crash, a fresh session — must find the same draft. This is the
	// whole reason it is a file and not a field: the model can forget, the draft cannot.
	got := loadDraft(thread)
	if got == nil {
		t.Fatal("the draft did not survive — planning would silently start over and lose the interview")
	}
	if got.Title != "Fleet readability" || len(got.Criteria) != 1 {
		t.Errorf("draft came back changed: %+v", got)
	}

	clearDraft(thread)
	if loadDraft(thread) != nil {
		t.Error("clearDraft left the draft behind — the next plan would resume a finished one")
	}
}

// A corrupt draft must read as ABSENT, not as an error. Otherwise a single bad write bricks planning
// for that thread forever, with no way out from inside the conversation.
func TestACorruptDraftReadsAsNoDraft(t *testing.T) {
	withHomeDir(t)
	const thread = "fa365970-def0-4321-a8f1-630a723ef35c"
	if err := os.MkdirAll(filepath.Dir(planPath(thread)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planPath(thread), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if loadDraft(thread) != nil {
		t.Error("a corrupt draft was returned as a draft")
	}
}

// THE ONE-QUESTION RULE. Handing a model every remaining requirement invites it to answer them all
// from its own assumptions — which is how a plan ends up specified confidently and wrongly. The
// status must name exactly one NEXT, even when several are unmet.
func TestStatusAsksForOneThingAtATime(t *testing.T) {
	d := &planDraft{Thread: "t", Title: "Fleet readability"}
	spec := api.Specificity{
		OK: false,
		Checks: []api.SpecCheck{
			{ID: "target", Label: "Name what to change.", Required: true},
			{ID: "criteria", Label: "Add at least one acceptance criterion.", Required: true},
			{ID: "executable", Label: "Make one criterion an executable check.", Required: true},
		},
		Blocking: []api.SpecCheck{
			{ID: "target", Label: "Name what to change.", Required: true},
			{ID: "criteria", Label: "Add at least one acceptance criterion.", Required: true},
			{ID: "executable", Label: "Make one criterion an executable check.", Required: true},
		},
	}
	out := planStatus(spec, d)

	if n := strings.Count(out, "NEXT — "); n != 1 {
		t.Errorf("status names %d next steps, want exactly 1", n)
	}
	if !strings.Contains(out, "NEXT — target") {
		t.Error("the next step should be the FIRST blocking check, in the server's order")
	}
	// The full checklist is still shown — the user must not be trapped in an opaque interview — but
	// only the first one carries an instruction.
	for _, id := range []string{"target", "criteria", "executable"} {
		if !strings.Contains(out, id) {
			t.Errorf("checklist omits %q; the user can't see how much is left", id)
		}
	}
	if strings.Contains(out, "Add at least one acceptance criterion.") {
		t.Error("a NON-next check's instruction leaked into the status — that invites answering ahead")
	}
}

// When everything is satisfied the status must point at finalize. A mode with no visible exit is one
// the model keeps interviewing inside of.
func TestStatusPointsAtTheExitWhenComplete(t *testing.T) {
	out := planStatus(api.Specificity{OK: true, Checks: []api.SpecCheck{{ID: "target", OK: true, Required: true}}},
		&planDraft{Thread: "t", Title: "Done thing"})
	if !strings.Contains(out, "planning_finalize") {
		t.Error("a complete draft never tells the model how to leave the mode")
	}
	if strings.Contains(out, "NEXT — ") {
		t.Error("a complete draft still asked for something")
	}
}

// A PRD is INPUT to the mode, never a bypass. The hint must push extraction, never skipping — if the
// model can decide the mode does not apply, the gate is advisory again.
func TestAPastedSpecIsExtractedNotSkipped(t *testing.T) {
	hint := planOpenHint(strings.Repeat("a long product requirements document. ", 40))
	if hint == "" {
		t.Fatal("a long paste produced no guidance at all")
	}
	if !strings.Contains(hint, "EXTRACT") {
		t.Error("the hint should tell the model to extract from the spec")
	}
	for _, forbidden := range []string{"skip the", "straight to planning_finalize", "bypass"} {
		if strings.Contains(strings.ToLower(hint), forbidden) {
			t.Errorf("the hint offers a way around the mode (%q)", forbidden)
		}
	}
	// A short idea gets no lecture about specs.
	if planOpenHint("make the fleet page readable") != "" {
		t.Error("a one-line idea should not be told it looks like a spec")
	}
}

// THE AUTO-PICK RULE. Promoting lands real work in a real worktree on someone's actual laptop, so
// "probably that one" is not good enough: exactly one online candidate auto-picks, anything else
// asks — and says what the options were, so the user can answer in one turn.
func TestMachineIsAutoPickedOnlyWhenUnambiguous(t *testing.T) {
	tests := []struct {
		name    string
		peers   []api.Peer
		want    string // "" = must refuse
		mustSay []string
	}{
		{
			name:  "one online candidate is picked",
			peers: []api.Peer{{DaemonID: "d1", DeviceLabel: "mini", Online: true, Projects: []string{"proj"}}},
			want:  "d1",
		},
		{
			name: "two online candidates must ask, and name both",
			peers: []api.Peer{
				{DaemonID: "d1", DeviceLabel: "mini", Online: true, Projects: []string{"proj"}},
				{DaemonID: "d2", DeviceLabel: "monolith", Online: true, Projects: []string{"proj"}},
			},
			mustSay: []string{"mini", "monolith"},
		},
		{
			name:    "registered but offline says so rather than failing at the daemon",
			peers:   []api.Peer{{DaemonID: "d1", DeviceLabel: "mini", Online: false, Projects: []string{"proj"}}},
			mustSay: []string{"online", "mini"},
		},
		{
			name:    "a machine that doesn't advertise the project is not a candidate",
			peers:   []api.Peer{{DaemonID: "d1", DeviceLabel: "mini", Online: true, Projects: []string{"other"}}},
			mustSay: []string{"add-project"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reason := pickFrom(tt.peers, "", "proj")
			if got != tt.want {
				t.Errorf("daemon = %q, want %q (reason: %s)", got, tt.want, reason)
			}
			for _, m := range tt.mustSay {
				if !strings.Contains(strings.ToLower(reason), strings.ToLower(m)) {
					t.Errorf("reason %q doesn't mention %q — the user can't act on it", reason, m)
				}
			}
		})
	}
}
