package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"partyline.sh/partyline/internal/api"
)

// THE RECORD a terminal planning session leaves behind (see the "the RECORD" block in
// cg_planning.go). Two properties are load-bearing and both fail SILENTLY if they break:
//
//  1. the record says how the plan was specified, WITHOUT inventing a conversation — every
//     question appears with the answer that closed it, and everything the model decided for
//     itself is labelled as assumed;
//  2. filing is BLOCKED when the record cannot be written. A tree filed without a party is
//     reachable from nowhere, and the user is told to go look for it.

func draftForRecord() *planDraft {
	return &planDraft{
		Thread:   "t-record",
		Idea:     "planning from the CLI leaves nothing to look at",
		Kind:     "task",
		Title:    "Plan records",
		Document: "Create a party at finalize and file the tree against it.",
		Criteria: []api.WorkItemCriterion{{Text: "go test ./... passes", Verify: "executable check"}},
		OpenQuestions: []planQuestion{
			{Text: "Should the party be created at open or at finalize?", Answer: "At finalize — an abandoned draft must not leave an abandoned party."},
			{Text: "Read-only, or reopenable?", Answer: "Read-only for now."},
		},
		Assumptions: []string{"the record lives in the user's only org"},
	}
}

func TestPlanRecordDecisionLogCarriesIdeaQuestionsAndAssumptions(t *testing.T) {
	d := draftForRecord()
	doc := planRecordDocument(d)

	if !strings.Contains(doc, "How this was specified") {
		t.Fatalf("no decision-log section:\n%s", doc)
	}
	if !strings.Contains(doc, d.Idea) {
		t.Errorf("the idea must appear verbatim; got:\n%s", doc)
	}
	for _, q := range d.OpenQuestions {
		if !strings.Contains(doc, q.Text) {
			t.Errorf("question missing from the record: %q", q.Text)
		}
		// A question without its answer is the failure that matters: it reads as an OPEN
		// question on an item that was filed as fully specified.
		if !strings.Contains(doc, q.Answer) {
			t.Errorf("answer missing for %q — the record would show it as unanswered", q.Text)
		}
	}
	for _, a := range d.Assumptions {
		if !strings.Contains(doc, "assumed: "+a) {
			t.Errorf("assumption %q must be marked as assumed, not stated as fact:\n%s", a, doc)
		}
	}
	// The honesty label: this is a decision log, and it must never be read as the conversation.
	if !strings.Contains(doc, "not a transcript") {
		t.Errorf("the record must say plainly that it is not a transcript:\n%s", doc)
	}
}

func TestPlanRecordWithNoQuestionsOrAssumptionsIsStillValid(t *testing.T) {
	d := &planDraft{
		Thread:   "t-bare",
		Idea:     "rename the button",
		Title:    "Rename the button",
		Document: "web/src/app/page.tsx — change the label.",
	}
	doc := planRecordDocument(d)
	for _, want := range []string{"# Rename the button", "## Specification", "rename the button", "How this was specified"} {
		if !strings.Contains(doc, want) {
			t.Fatalf("a question-less, assumption-less draft must still produce a whole document (missing %q):\n%s", want, doc)
		}
	}
	// Nothing fabricated: with no questions and no assumptions, those sub-sections do not appear.
	if strings.Contains(doc, "Questions asked") || strings.Contains(doc, "Assumed, not asked") {
		t.Errorf("empty sections must be omitted, not filled with invented content:\n%s", doc)
	}
	// Fully empty draft: must not panic and must still be a document.
	if got := planRecordDocument(&planDraft{}); !strings.Contains(got, "## Specification") {
		t.Errorf("an empty draft produced no document:\n%s", got)
	}
}

// NEVER SYNTHESISE A TRANSCRIPT. The conversation happened in a terminal and we do not have it, so
// no code path on the planning-record route may write party messages. Enforced by reading the
// source: an assertion about behaviour would only cover the paths a test happens to exercise, and
// the thing being prevented is someone ADDING a path later.
func TestPlanRecordWritesNoPartyMessages(t *testing.T) {
	for _, f := range []string{"cg_planning.go", "cg_mcp.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		for _, banned := range []string{"party_messages", "/messages", "PartyClient", ".Post("} {
			// Comments are allowed to NAME the thing they forbid; code is not.
			for i, line := range strings.Split(src, "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "//") || !strings.Contains(line, banned) {
					continue
				}
				t.Errorf("%s:%d writes to a party channel (%q) — the planning record must never fabricate a transcript:\n%s",
					f, i+1, banned, line)
			}
		}
	}
}

// ---- finalize end-to-end against a stand-in control plane ----

type recordServer struct {
	mu        sync.Mutex
	partyFail bool     // break party creation on purpose
	treeBody  string   // the body of the /work-items/tree POST, if it happened at all
	docBody   string   // the document written to the party
	hits      []string // every path touched, in order
}

// planRecordTestPlane stands the control plane up: an org, the specificity gate (always green), a
// party with a document, and the tree endpoint.
func planRecordTestPlane(t *testing.T, rs *recordServer) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.mu.Lock()
		rs.hits = append(rs.hits, r.Method+" "+r.URL.Path)
		rs.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/orgs":
			_, _ = w.Write([]byte(`{"orgs":[{"id":"o1","name":"Team","slug":"team","personal":true}]}`))
		case r.URL.Path == "/api/v1/work-items/specificity":
			_, _ = w.Write([]byte(`{"ok":true,"checks":[],"blocking":[],"message":""}`))
		case r.URL.Path == "/api/v1/parties" && r.Method == "POST":
			if rs.partyFail {
				w.WriteHeader(500)
				_, _ = w.Write([]byte(`{"error":"control plane unreachable"}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"party-1","join_code":"abc","org":"team","link":"x"}`))
		case strings.HasSuffix(r.URL.Path, "/doc") && r.Method == "GET":
			_, _ = w.Write([]byte(`{"body":"","version":0}`))
		case strings.HasSuffix(r.URL.Path, "/doc") && r.Method == "PUT":
			b, _ := readAllBody(r)
			rs.mu.Lock()
			rs.docBody = b
			rs.mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true,"version":1}`))
		case r.URL.Path == "/api/v1/work-items/tree" && r.Method == "POST":
			b, _ := readAllBody(r)
			rs.mu.Lock()
			rs.treeBody = b
			rs.mu.Unlock()
			_, _ = w.Write([]byte(`{"root_id":"wi-1","count":1}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("PARTYLINE_API", srv.URL)
	cfg := api.ConfigDir()
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "token"), []byte("tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readAllBody(r *http.Request) (string, error) {
	var buf bytes.Buffer
	_, err := buf.ReadFrom(r.Body)
	return buf.String(), err
}

func finalizeWithDraft(t *testing.T, d *planDraft) string {
	t.Helper()
	if err := saveDraft(d); err != nil {
		t.Fatal(err)
	}
	s := &cgServer{c: api.New(), thread: d.Thread}
	var out bytes.Buffer
	params, _ := json.Marshal(map[string]any{"name": "planning_finalize", "arguments": map[string]any{}})
	s.handleCall(json.NewEncoder(&out), rpcReq{ID: json.RawMessage(`1`), Params: params})
	return out.String()
}

func TestPlanRecordFinalizeFilesTheTreeAgainstTheParty(t *testing.T) {
	rs := &recordServer{}
	planRecordTestPlane(t, rs)

	d := draftForRecord()
	got := finalizeWithDraft(t, d)

	if !strings.Contains(rs.treeBody, `"origin_party_id":"party-1"`) {
		t.Errorf("the filed tree must point back at the record party; body was: %s", rs.treeBody)
	}
	if !strings.Contains(rs.docBody, "How this was specified") || !strings.Contains(rs.docBody, d.Idea) {
		t.Errorf("the party document must carry the record; got: %s", rs.docBody)
	}
	if !strings.Contains(got, "/p/party-1") {
		t.Errorf("the result must tell the model where the plan is readable; got: %s", got)
	}
	if loadDraft(d.Thread) != nil {
		t.Errorf("a successful finalize must clear the draft")
	}
}

// THE ONE THAT MATTERS. With the record unwritable, filing must not happen at all — and the draft
// must survive, so "refused" never means "lost".
func TestPlanRecordFinalizeFilesNothingWhenTheRecordFails(t *testing.T) {
	rs := &recordServer{partyFail: true}
	planRecordTestPlane(t, rs)

	d := draftForRecord()
	got := finalizeWithDraft(t, d)

	if rs.treeBody != "" {
		t.Fatalf("work items were filed without a record — they would be reachable from nowhere: %s", rs.treeBody)
	}
	for _, h := range rs.hits {
		if strings.Contains(h, "/work-items/tree") {
			t.Fatalf("the tree endpoint was called despite the record failing: %v", rs.hits)
		}
	}
	if loadDraft(d.Thread) == nil {
		t.Fatal("the draft was cleared on a refusal — the planning conversation would be lost")
	}
	if !strings.Contains(got, "NOTHING WAS FILED") || !strings.Contains(got, "draft is KEPT") {
		t.Errorf("the refusal must say plainly that nothing was filed and the draft is kept; got: %s", got)
	}
}
