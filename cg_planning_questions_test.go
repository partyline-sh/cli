package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"partyline.sh/partyline/internal/api"
)

// THE OPEN-QUESTIONS GATE. Every other slot in planning mode is mechanical — target, criteria,
// executable check, length — so a model satisfies all of them by reading the repo and never speaks
// to the user, and the checklist goes green over product decisions nobody made. These tests exist to
// prove the one slot the repo cannot fill actually REFUSES, and that it refuses at the same moment a
// missing mechanical slot would.
//
// They drive the real MCP handler (not a helper) against a fake control plane, because the gate
// lives in planning_finalize and a test of a helper it happens to call would keep passing if the
// handler stopped calling it.

// planTestServer stands in for the control plane: the specificity verdict it should return, and a
// record of whether the tree was actually filed.
type planTestServer struct {
	spec  api.Specificity
	filed bool
	tree  api.WorkTreeNode
}

func (p *planTestServer) start(t *testing.T) *api.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/work-items/specificity", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(p.spec)
	})
	mux.HandleFunc("/api/v1/work-items/tree", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Root api.WorkTreeNode `json:"root"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		p.filed, p.tree = true, in.Root
		_ = json.NewEncoder(w).Encode(map[string]any{"root_id": "wi_root", "count": 1})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &api.Client{Base: srv.URL, HTTP: srv.Client()}
}

const planTestThread = "fa365970-def0-4321-a8f1-630a723ef35c"

// planCall invokes one planning tool exactly as an engine would, and returns the text the model sees
// plus whether it came back as an error (which is what makes a model treat it as work to finish).
func planCall(t *testing.T, s *cgServer, name string, args map[string]any) (text string, isErr bool) {
	t.Helper()
	params, err := json.Marshal(map[string]any{"name": name, "arguments": args})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	s.handleCall(json.NewEncoder(&buf), rpcReq{ID: json.RawMessage(`1`), Params: params})

	var resp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
		t.Fatalf("unparseable tool response %q: %v", buf.String(), err)
	}
	if len(resp.Result.Content) == 0 {
		t.Fatalf("tool %s returned no content: %s", name, buf.String())
	}
	return resp.Result.Content[0].Text, resp.Result.IsError
}

// specAllGreen is the verdict for a draft whose every mechanical slot is satisfied — the exact state
// in which an unanswered question is the ONLY thing that can still be wrong.
func specAllGreen() api.Specificity {
	return api.Specificity{
		OK: true,
		Checks: []api.SpecCheck{
			{ID: "target", Label: "Name what to change.", OK: true, Required: true},
			{ID: "criteria", Label: "Add at least one acceptance criterion.", OK: true, Required: true},
			{ID: "executable", Label: "Make one criterion an executable check.", OK: true, Required: true},
		},
	}
}

func newPlanServer(t *testing.T, spec api.Specificity) (*cgServer, *planTestServer) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	fake := &planTestServer{spec: spec}
	return &cgServer{c: fake.start(t), thread: planTestThread}, fake
}

// openDraft opens a draft and fills the slots, so the only remaining variable is the questions.
func openDraft(t *testing.T, s *cgServer) {
	t.Helper()
	planCall(t, s, "planning_open", map[string]any{"idea": "make the fleet page readable", "title": "Fleet readability"})
	planCall(t, s, "planning_note", map[string]any{
		"title":    "Fleet readability",
		"kind":     "task",
		"document": "Change web/src/app/fleet/page.tsx so the node table is readable.",
		"acceptance_criteria": []map[string]any{
			{"text": "npm run build exits 0", "verify": "executable check"},
		},
	})
}

// THE POINT OF THE WHOLE FEATURE: a green checklist is not consent. With every mechanical slot
// satisfied, one unanswered question must still refuse the file — and the refusal must SAY the
// question, or the model cannot act on it.
func TestPlanningFinalizeRefusesAnUnansweredQuestionWithEverySlotGreen(t *testing.T) {
	s, fake := newPlanServer(t, specAllGreen())
	openDraft(t, s)

	planCall(t, s, "planning_note", map[string]any{
		"open_questions": []string{"Should offline nodes be hidden, or shown greyed out?"},
	})

	out, isErr := planCall(t, s, "planning_finalize", nil)
	if !isErr {
		t.Error("finalize succeeded as a non-error with a question outstanding — the model will move on")
	}
	if fake.filed {
		t.Fatal("THE GATE DID NOT HOLD: the plan was filed with a product decision never asked")
	}
	if !strings.Contains(out, "Should offline nodes be hidden") {
		t.Errorf("the refusal does not name the open question, so it is unactionable:\n%s", out)
	}
}

// The gate must OPEN, not just close. An answered question files exactly as it would have before
// questions existed — otherwise the feature is a wall, not a gate.
func TestPlanningAnAnsweredQuestionUnblocksFinalize(t *testing.T) {
	s, fake := newPlanServer(t, specAllGreen())
	openDraft(t, s)
	planCall(t, s, "planning_note", map[string]any{
		"open_questions": []string{"Should offline nodes be hidden, or shown greyed out?"},
	})
	if _, isErr := planCall(t, s, "planning_finalize", nil); !isErr {
		t.Fatal("precondition failed: finalize did not refuse the unanswered question")
	}

	planCall(t, s, "planning_note", map[string]any{
		"answers": []map[string]any{{"index": 1, "answer": "Greyed out, at the bottom."}},
	})

	out, isErr := planCall(t, s, "planning_finalize", nil)
	if isErr {
		t.Fatalf("finalize still refused after the user answered:\n%s", out)
	}
	if !fake.filed {
		t.Error("finalize reported success but never filed the tree")
	}
	if loadDraft(planTestThread) != nil {
		t.Error("the draft outlived a successful finalize — the next plan would resume a filed one")
	}
}

// A question can also be answered by naming it, because a model that lost the numbering to a
// compaction still knows what it asked.
func TestPlanningAnAnswerCanNameTheQuestionInsteadOfItsNumber(t *testing.T) {
	s, _ := newPlanServer(t, specAllGreen())
	openDraft(t, s)
	planCall(t, s, "planning_note", map[string]any{
		"open_questions": []string{"Which teams see the fleet page?", "Should offline nodes be hidden?"},
	})
	planCall(t, s, "planning_note", map[string]any{
		"answers": []map[string]any{
			{"question": "offline nodes", "answer": "Hidden."},
			{"question": "Which teams", "answer": "Admins only."},
		},
	})
	if _, isErr := planCall(t, s, "planning_finalize", nil); isErr {
		t.Error("finalize refused after both questions were answered by name")
	}
}

// THE GUARDRAIL: the model must not be able to close its own question. An empty answer is not an
// answer, and an answer aimed at a question that does not exist is not one either — both leave the
// question open, and the second says so rather than vanishing.
func TestPlanningAnEmptyAnswerStillCountsAsUnanswered(t *testing.T) {
	s, fake := newPlanServer(t, specAllGreen())
	openDraft(t, s)
	planCall(t, s, "planning_note", map[string]any{
		"open_questions": []string{"Should offline nodes be hidden, or shown greyed out?"},
	})

	for _, answer := range []string{"", "   ", "\n\t"} {
		planCall(t, s, "planning_note", map[string]any{
			"answers": []map[string]any{{"index": 1, "answer": answer}},
		})
		out, isErr := planCall(t, s, "planning_finalize", nil)
		if !isErr || fake.filed {
			t.Fatalf("an empty answer (%q) closed the question — the model can now answer itself:\n%s", answer, out)
		}
	}

	// An answer that matches nothing must be reported, not swallowed: silently dropping it leaves a
	// question the user DID answer still blocking, with nothing on screen to explain why.
	out, _ := planCall(t, s, "planning_note", map[string]any{
		"answers": []map[string]any{{"index": 9, "answer": "Greyed out."}},
	})
	if !strings.Contains(out, "NOT RECORDED") {
		t.Errorf("an unmatched answer was accepted silently:\n%s", out)
	}
	if _, isErr := planCall(t, s, "planning_finalize", nil); !isErr {
		t.Error("an answer to a nonexistent question unblocked the gate")
	}
}

// A plan that genuinely needs nothing asked must behave EXACTLY as it did before this feature. A
// gate that adds a ritual to every task is one people route around.
func TestPlanningADraftWithNoQuestionsIsUnaffected(t *testing.T) {
	s, fake := newPlanServer(t, specAllGreen())
	openDraft(t, s)

	status, _ := planCall(t, s, "planning_note", map[string]any{"title": "Fleet readability"})
	if strings.Contains(status, "Open questions") || strings.Contains(status, "NEXT — open question") {
		t.Errorf("a question-free draft was shown a questions section:\n%s", status)
	}
	out, isErr := planCall(t, s, "planning_finalize", nil)
	if isErr || !fake.filed {
		t.Fatalf("a draft with no questions was refused: %s", out)
	}
}

// ONE NEXT THING, QUESTIONS FIRST. An unanswered question outranks every mechanical slot: the slots
// can be filled by reading the repo, so asking the human for a file path while the product decision
// goes unasked is the exact inversion this replaces.
func TestPlanningStatusNamesAnOpenQuestionAheadOfAMissingSlot(t *testing.T) {
	d := &planDraft{Thread: "t", Title: "Fleet readability"}
	d.addQuestion("Should offline nodes be hidden, or shown greyed out?")
	spec := api.Specificity{
		OK: false,
		Checks: []api.SpecCheck{
			{ID: "target", Label: "Name what to change.", Required: true},
			{ID: "executable", Label: "Make one criterion an executable check.", Required: true},
		},
		Blocking: []api.SpecCheck{
			{ID: "target", Label: "Name what to change.", Required: true},
			{ID: "executable", Label: "Make one criterion an executable check.", Required: true},
		},
	}

	out := planStatus(spec, d)
	if n := strings.Count(out, "NEXT — "); n != 1 {
		t.Fatalf("status names %d next steps, want exactly 1:\n%s", n, out)
	}
	if !strings.Contains(out, "NEXT — open question") {
		t.Errorf("a mechanical slot was put ahead of an unanswered question:\n%s", out)
	}
	if !strings.Contains(out, "Should offline nodes be hidden") {
		t.Error("the NEXT step does not say what the question is")
	}
	// The questions are listed ABOVE the checklist, so the human decision reads as the headline.
	if qi, ci := strings.Index(out, "Open questions"), strings.Index(out, "Checklist:"); qi < 0 || qi > ci {
		t.Errorf("open questions are not shown before the checklist:\n%s", out)
	}

	// Once answered, the mechanical agenda resumes exactly where it was.
	d.answerQuestion(1, "", "Greyed out.")
	if next := planStatus(spec, d); !strings.Contains(next, "NEXT — target") {
		t.Errorf("after the answer, the status did not fall back to the first blocking slot:\n%s", next)
	}
}

// A green checklist must not advertise the exit while a question is open, or the model calls
// finalize, gets refused, and burns a turn discovering what the status already knew.
func TestPlanningStatusDoesNotOfferTheExitWithAQuestionOpen(t *testing.T) {
	d := &planDraft{Thread: "t", Title: "Fleet readability"}
	d.addQuestion("Should offline nodes be hidden?")
	out := planStatus(specAllGreen(), d)
	if strings.Contains(out, "Call planning_finalize to file") {
		t.Errorf("status invited finalize with a question outstanding:\n%s", out)
	}
}

// ASSUMPTIONS DO NOT BLOCK — but they must not disappear either. The builder and the reviewer both
// read the filed document, and "assumed X" read as a given requirement is how a guess becomes a
// spec nobody wrote.
func TestPlanningAssumptionsDoNotBlockAndAreFiledWithTheDocument(t *testing.T) {
	s, fake := newPlanServer(t, specAllGreen())
	openDraft(t, s)
	planCall(t, s, "planning_note", map[string]any{
		"assumptions": []string{"Sorting by last-heartbeat, because no order was specified."},
	})

	out, isErr := planCall(t, s, "planning_finalize", nil)
	if isErr || !fake.filed {
		t.Fatalf("an assumption blocked filing; assumptions are not a gate: %s", out)
	}
	if !strings.Contains(fake.tree.Document, "## Assumptions") ||
		!strings.Contains(fake.tree.Document, "Sorting by last-heartbeat") {
		t.Errorf("the assumption never reached the filed document:\n%s", fake.tree.Document)
	}
	// The original document survives alongside it.
	if !strings.Contains(fake.tree.Document, "web/src/app/fleet/page.tsx") {
		t.Error("folding in the assumptions clobbered the document")
	}
}

// Questions ACCUMULATE across calls and de-duplicate. A model re-sending its list every turn (which
// they do) must not end up with the same question four times, and sending a new one must not erase
// the old ones — the draft is the record of the conversation.
func TestPlanningQuestionsAccumulateAndDeduplicate(t *testing.T) {
	s, _ := newPlanServer(t, specAllGreen())
	openDraft(t, s)
	planCall(t, s, "planning_note", map[string]any{"open_questions": []string{"Hide offline nodes?"}})
	planCall(t, s, "planning_note", map[string]any{
		"open_questions": []string{"hide offline nodes?", "  ", "Who can see this page?"},
	})

	d := loadDraft(planTestThread)
	if d == nil {
		t.Fatal("draft vanished")
	}
	if len(d.OpenQuestions) != 2 {
		t.Fatalf("questions = %+v, want the original plus one new one", d.OpenQuestions)
	}
	if len(d.unanswered()) != 2 {
		t.Error("a freshly added question is not unanswered")
	}
}
