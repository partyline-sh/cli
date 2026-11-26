package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// Loops decide by default: a run that starts waiting gets a decider, which approves a waiting brief
// through the popup's own approval. Asking the person, nothing is decided and nothing is started.
func TestLoopDecidesItsOwnApproval(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo, _ := loopRepo(t)
	var started []string
	prev := spawnDecide
	spawnDecide = func(id string) { started = append(started, id) }
	defer func() { spawnDecide = prev }()
	prevRound := spawnRound
	spawnRound = func(string, int) error { return nil }
	defer func() { spawnRound = prevRound }()

	r := testLoopRun(t, repo)
	_ = r.set(loopAwaiting, "brief 1 waiting for approval", "started", nil)
	if len(started) != 1 || started[0] != r.ID {
		t.Fatalf("no decider started for a waiting run: %v", started)
	}
	line, err := loopDecide(r.ID)
	if err != nil || !strings.Contains(line, "approved brief 1") {
		t.Fatalf("decide: %q %v", line, err)
	}
	if r, _ = loadLoopRun(r.ID); r.State != loopBaseline {
		t.Errorf("after the loop's approval: %s", r.State)
	}

	// Asking the person: nothing starts, nothing is decided.
	if err := saveSettings(loopSettings{AskMe: true}); err != nil {
		t.Fatal(err)
	}
	other := testLoopRun(t, repo)
	started = nil
	_ = other.set(loopAwaiting, "brief 1 waiting for approval", "started", nil)
	if len(started) != 0 {
		t.Error("a decider started while the person decides")
	}
	if line, _ := loopDecide(other.ID); line != "" {
		t.Errorf("decided while the person decides: %q", line)
	}
}

// A disputed finding goes to a model that did not build the work, with Jev's estimate in its prompt
// when a key is saved; its ruling stands. Jev failing is skipped; no valid ruling keeps the finding.
func TestLoopDecidesADispute(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo, _ := loopRepo(t)
	jevStatus, jevBody := http.StatusOK, `{"answers":{"stands":{"type":"noul","noul":0.31}}}`
	var jevAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		jevAuth = req.Header.Get("Authorization")
		_, _ = io.ReadAll(req.Body)
		w.WriteHeader(jevStatus)
		_, _ = w.Write([]byte(jevBody))
	}))
	defer srv.Close()
	prevURL := jevEndpoint
	jevEndpoint = srv.URL
	defer func() { jevEndpoint = prevURL }()
	var prompts []string
	reply := `{"decision": "dismiss", "confidence": 0.6, "reason": "the guard is in handler.go"}`
	prev := runReviewer
	runReviewer = func(_ context.Context, _ loopEngine, _ string, prompt string) (string, loopTokens, error) {
		prompts = append(prompts, prompt)
		return reply, loopTokens{}, nil
	}
	defer func() { runReviewer = prev }()

	dispute := func() *loopRun {
		r := testLoopRun(t, repo)
		writeLoopJSON(r.ID, "dispute.json", loopTracked{ID: "F1-1", Dispute: "the check exists already"})
		_ = r.set(loopAwaitYou, "disputed F1-1", "disputed", nil)
		return r
	}
	decision := func(r *loopRun) string {
		var decided []loopTracked
		readLoopJSON(r.ID, "decided.json", &decided)
		if len(decided) == 0 {
			return ""
		}
		return decided[len(decided)-1].Decision
	}

	// With a Jev key: its estimate is in the prompt, and the model's ruling stands though unsure.
	if err := saveJevKey("jev-key"); err != nil {
		t.Fatal(err)
	}
	r := dispute()
	if _, err := loopDecide(r.ID); err != nil {
		t.Fatal(err)
	}
	if decision(r) != "dismiss" || jevAuth != "Bearer jev-key" || !strings.Contains(prompts[0], "probability 0.31") || strings.Contains(prompts[0], "the person decides") {
		t.Fatalf("ruling %q, jev auth %q, prompt:\n%s", decision(r), jevAuth, prompts[0])
	}
	if r, _ = loadLoopRun(r.ID); r.State != loopBuilding {
		t.Errorf("after the ruling: %s", r.State)
	}
	// Jev with no credits (any error): skipped, and the dispute is still decided.
	jevStatus, jevBody = http.StatusPaymentRequired, `{"error":"no credits"}`
	r = dispute()
	_, _ = loopDecide(r.ID)
	if decision(r) != "dismiss" || strings.Contains(prompts[1], "Jev") {
		t.Errorf("a failing Jev was not skipped: %q\n%s", decision(r), prompts[1])
	}
	// No key: no Jev at all.
	_ = os.Remove(jevKeyPath())
	jevAuth = ""
	r = dispute()
	_, _ = loopDecide(r.ID)
	if jevAuth != "" {
		t.Error("Jev was called with no key")
	}
	// No valid ruling: the finding stands.
	reply = "I am not sure."
	r = dispute()
	_, _ = loopDecide(r.ID)
	if decision(r) != "fix" {
		t.Errorf("no valid ruling decided %q, want fix", decision(r))
	}
}
