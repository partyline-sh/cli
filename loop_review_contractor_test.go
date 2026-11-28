package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// A round's reviewers run as contractors under the loop's session: two reviewers, two contractor
// records with role reviewer, and their reviews parsed as before; a limit in a reviewer's
// diagnostics still reaches failover; ⏎ on the card watches a reviewer.
func TestReviewerContractor(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo, write := loopRepo(t)
	r := testLoopRun(t, repo)
	r.Batch = "" // a single run: reviewed the same way
	write("main.go", "package main\n\nfunc main() { println(\"changed for the review\") }\n")
	oldRC, oldStart, oldReviewer := runReviewerContractor, startContractor, runReviewer
	t.Cleanup(func() { runReviewerContractor, startContractor, runReviewer = oldRC, oldStart, oldReviewer })
	runReviewerContractor = realRunReviewerContractor
	var mu sync.Mutex
	seen := map[string]bool{}
	runReviewer = func(_ context.Context, e loopEngine, dir, prompt string) (string, loopTokens, error) {
		mu.Lock()
		seen[e.String()] = true
		mu.Unlock()
		if !strings.Contains(prompt, "Diff") {
			t.Errorf("%s got another prompt", e)
		}
		if e.Name == "codex" {
			return `{"findings":[{"priority":"P2","file":"main.go","line":3,"quote":"func main() { println(\"changed for the review\") }","claim":"prints","fix":"log"}],"verdict":"approve"}`, loopTokens{In: 10}, nil
		}
		return `{"findings":[],"verdict":"approve"}`, loopTokens{In: 5}, nil
	}
	var started []*contractor
	startContractor = func(c *contractor) error {
		c.State, c.Started = "running", time.Now().UTC()
		if err := withContractors(c.save); err != nil {
			return err
		}
		mu.Lock()
		started = append(started, c)
		mu.Unlock()
		go func() {
			var err error
			if res := runReviewContractor(c.Argv[3:]); res.Err != "" {
				err = fmt.Errorf("exit status 1")
			}
			finishContractor(c.ID, err, "")
		}()
		return nil
	}
	snap, err := takeSnapshot(repo, r.Base)
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Cleanup()
	diff, _ := snap.Diff()
	engines, _ := parseLoopEngines("claude,codex")
	reviews := reviewSnapshot(snap, engines, loopReviewPrompt("brief", "report", diff), diff, loopDir(r.ID), "r1-", nil, r)
	if len(started) != 2 {
		t.Fatalf("%d reviewer contractors started, want 2", len(started))
	}
	for _, c := range started {
		if c.Role != "reviewer" || c.Loop != r.ID || c.Parent != "pane:%7" || c.Dir != snap.Dir || !strings.HasPrefix(c.Title, "review r1-") {
			t.Errorf("reviewer contractor: %+v", c)
		}
		if got, _ := loadContractor(c.ID); got == nil || got.State != "done" {
			t.Errorf("reviewer %s record: %+v", c.ID, got)
		}
	}
	if !seen["claude"] || !seen["codex"] {
		t.Errorf("reviewers run: %v", seen)
	}
	if reviews[0].Engine != "claude" || reviews[0].Verdict != "approve" || len(reviews[1].Findings) != 1 || reviews[1].Findings[0].Claim != "prints" || reviews[1].Tokens.In != 10 {
		t.Errorf("reviews: %+v", reviews)
	}

	// A limit in the reviewer's own diagnostics still reaches the slot's failover.
	runReviewer = func(context.Context, loopEngine, string, string) (string, loopTokens, error) {
		return "", loopTokens{}, &reviewerFailure{"claude failed", "Claude AI usage limit reached|" + "1893456000"}
	}
	before := len(started)
	rv := reviewSlot([]string{"claude:a"}, true, snap.Dir, "prompt", "", reviewAt{r, "r1-1"})
	if rv.LimitedUntil.IsZero() {
		t.Errorf("a limit inside the reviewer contractor did not reach failover: %+v", rv)
	}
	if c, _ := loadContractor(started[before].ID); c == nil || c.State != "failed" {
		t.Errorf("a failed review's chip: %+v", c)
	}
	// The round reads its reviewers itself: nothing reached the session's inbox.
	if loopPending(r.ID) {
		t.Errorf("reviewers posted to the session: %v", loopTake(r.ID))
	}

	// ⏎ on an In review card with a reviewer at work watches it.
	fakeContractor(t, contractor{ID: "abc1230000", Loop: r.ID, Role: "reviewer", State: "running", Window: "@live"})
	_ = r.set(loopChecking, "round 1: checks, then review", "ready", nil)
	for _, row := range tabRows(time.Now()) {
		if row.RunID == r.ID && row.Watch != "abc1230000" {
			t.Errorf("the In review card watches %q", row.Watch)
		}
	}
}

// When the round's review time runs out, the reviewer gets the grace period to finish before its
// window is closed.
func TestReviewerContractorGrace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo, _ := loopRepo(t)
	r := testLoopRun(t, repo)
	oldStart, oldStop, oldGrace := startContractor, stopContractor, reviewerGrace
	t.Cleanup(func() { startContractor, stopContractor, reviewerGrace = oldStart, oldStop, oldGrace })
	reviewerGrace = 3 * time.Second
	stopped := false
	stopContractor = func(string) { stopped = true }
	startContractor = func(c *contractor) error {
		c.State, c.Started = "running", time.Now().UTC()
		_ = withContractors(c.save)
		go func() {
			time.Sleep(5500 * time.Millisecond) // after the round's time (4 s), inside the grace from then
			writeLoopJSON(r.ID, "review-"+c.ID+".result.json", reviewResult{Text: "late but done"})
			finishContractor(c.ID, nil, "")
		}()
		return nil
	}
	// The round's time runs out after more than a grace period from the start: a grace measured
	// from the start would already be over.
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	text, _, err := realRunReviewerContractor(ctx, reviewAt{r, "r1-1"}, loopEngine{Name: "claude"}, t.TempDir(), "prompt")
	if err != nil || text != "late but done" || stopped {
		t.Errorf("within the grace: %q, %v, window closed %v", text, err, stopped)
	}
}
