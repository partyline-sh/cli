package main

import (
	"strings"
	"testing"

	"partyline.sh/partyline/internal/api"
)

// backlog_read exists because an agent told a user "the /work board is not reachable" while holding
// plan_read. The rendering has one job: let a model see, in one read, what is already queued,
// running, or stuck — so it stops re-proposing work that exists.

func TestFormatBacklogLeadsWithWhatNeedsAHuman(t *testing.T) {
	b := &api.Backlog{Totals: map[string]int{"queued": 2, "needs_approval": 1}}
	b.NeedsAttention = append(b.NeedsAttention, struct {
		Title  string `json:"title"`
		Status string `json:"status"`
		Reason string `json:"reason"`
	}{Title: "wire Stripe webhooks", Status: "needs_approval", Reason: "token budget hit"})
	b.Queued = append(b.Queued,
		struct {
			Title string `json:"title"`
		}{Title: "first up"},
		struct {
			Title string `json:"title"`
		}{Title: "second up"})

	out := formatBacklog(b)
	// A blocked run is the most decision-relevant row on the board; it must come before the queue.
	if strings.Index(out, "NEEDS ATTENTION") > strings.Index(out, "QUEUED") {
		t.Fatalf("queue listed above what's blocked:\n%s", out)
	}
	if !strings.Contains(out, "token budget hit") {
		t.Errorf("dropped the pause reason — the actionable part:\n%s", out)
	}
	// Run ORDER is the point of the queue section: "next" must be unambiguous.
	if !strings.Contains(out, "1. first up") || !strings.Contains(out, "2. second up") {
		t.Errorf("queue is not presented in run order:\n%s", out)
	}
	if !strings.Contains(out, "queued 2") {
		t.Errorf("totals missing:\n%s", out)
	}
}

// An empty board must READ as empty. "Nothing came back" and "there is nothing queued" are
// different facts, and an agent that conflates them invents work or refuses to plan.
func TestFormatBacklogEmptyAndNil(t *testing.T) {
	if got := formatBacklog(&api.Backlog{}); !strings.Contains(got, "empty") {
		t.Errorf("empty board did not read as empty: %q", got)
	}
	if got := formatBacklog(nil); got == "" {
		t.Error("nil backlog produced no message at all")
	}
}

// Both tools must be advertised, or the agent never learns they exist — the same invisibility that
// caused the original wrong answers.
func TestCapabilityToolsAreAdvertised(t *testing.T) {
	var names []string
	for _, tl := range toolDefs {
		if n, ok := tl["name"].(string); ok {
			names = append(names, n)
		}
	}
	joined := strings.Join(names, ",")
	for _, want := range []string{"capabilities", "backlog_read"} {
		if !strings.Contains(joined, want) {
			t.Errorf("%q is not advertised; tools = %s", want, joined)
		}
	}
}
