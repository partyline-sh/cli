package main

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"partyline.sh/partyline/internal/api"
)

func keysOf(acts []boardAction) []string {
	out := make([]string, 0, len(acts))
	for _, a := range acts {
		out = append(out, a.Key)
	}
	return out
}

func hasKey(acts []boardAction, key string) bool {
	for _, a := range acts {
		if a.Key == key {
			return true
		}
	}
	return false
}

// The rule that cost real money: a failed run is not shippable. Offering Accept on one is how a
// rate-limited or crashed run gets marked as delivered.
func TestFailedRunNeverOffersAccept(t *testing.T) {
	acts := boardActions(card("r", withStatus("failed")))
	if hasKey(acts, "accept") {
		t.Fatalf("failed run offered Accept: %v", keysOf(acts))
	}
	if acts[0].Key != "continue" || acts[0].Path != "retry" {
		t.Fatalf("primary for failed = %+v, want continue→retry", acts[0])
	}
}

func TestUnknownStatusOffersNothing(t *testing.T) {
	c := card("r", withStatus("some_status_this_build_predates"))
	if got := boardActions(c); len(got) != 0 {
		t.Fatalf("unknown status offered %v — the board must not guess", keysOf(got))
	}
	if _, ok := primaryAction(c); ok {
		t.Fatal("primaryAction must report there is nothing to do")
	}
}

// An accepted card keeps status "done" — acceptance is recorded as accepted_at, not as a status —
// so keying off status alone re-offered Accept as its primary and offered To-backlog with no
// confirm, which un-ships accepted work in two keystrokes.
func TestAcceptedColumnCardIsFinished(t *testing.T) {
	c := card("r", withStatus("done"))
	c.Column = api.ColAccepted
	acts := boardActions(c)

	if hasKey(acts, "accept") {
		t.Error("an accepted card must not offer Accept again")
	}
	if len(acts) != 1 || acts[0].Key != "requeue" {
		t.Fatalf("acts = %v, want only the un-ship move", keysOf(acts))
	}
	if !acts[0].Danger || acts[0].Confirm == "" {
		t.Error("un-shipping accepted work must be confirmed")
	}
}

// Accept is withheld while the reviewer is still grading — signing off on a grade that has not
// landed is the one move that should not be a keystroke away. Mirrors the web's omit rule.
func TestAcceptWithheldWhileReviewing(t *testing.T) {
	for _, f := range []func(*api.BoardCard){
		func(c *api.BoardCard) { c.Reviewing = true },
		func(c *api.BoardCard) { c.ReviewWaiting = true },
	} {
		c := card("r", withStatus("done"))
		f(&c)
		acts := boardActions(c)
		if hasKey(acts, "accept") {
			t.Fatalf("Accept offered while the review is in flight: %v", keysOf(acts))
		}
		if len(acts) == 0 {
			t.Fatal("a reviewing card still needs a way out (restart / to backlog)")
		}
	}
}

func TestNeedsApprovalContinueIsForced(t *testing.T) {
	acts := boardActions(card("r", withStatus("needs_approval")))
	for _, a := range acts {
		if a.Key == "continue" {
			if !a.Force {
				t.Fatal("Continue from needs_approval must force: a human asking again overrides the predicted quota wait")
			}
			if a.Path != "resume" {
				t.Fatalf("continue path = %q, want resume", a.Path)
			}
			return
		}
	}
	t.Fatalf("no Continue offered: %v", keysOf(acts))
}

// A live-looking run and a stalled one get the SAME moves with different guards. Getting this
// backwards means a mis-keypress clobbers a working crank.
func TestBuildingLiveRunMutesAndForcesRecovery(t *testing.T) {
	live := card("r", withStatus("running"))
	acts := boardActions(live)

	if acts[0].Key != "pause" {
		t.Fatalf("primary on a healthy running run = %q, want pause", acts[0].Key)
	}
	for _, a := range acts {
		switch a.Key {
		case "continue", "restart":
			if !a.Force || !a.Muted || a.Confirm == "" {
				t.Fatalf("%s on a live run must be muted, forced and confirmed: %+v", a.Key, a)
			}
		}
	}
}

func TestBuildingStalledRunOffersPlainRecovery(t *testing.T) {
	c := card("r", withStatus("running"))
	c.Stalled = true
	acts := boardActions(c)

	if hasKey(acts, "pause") {
		t.Fatal("a stalled run has nothing to pause")
	}
	if acts[0].Key != "continue" || acts[0].Force {
		t.Fatalf("primary = %+v, want an unforced Continue (the server agrees it is stale)", acts[0])
	}
	for _, a := range acts {
		if a.Key == "restart" && a.Force {
			t.Fatal("restart on an agreed-stalled run should not need forcing")
		}
	}
}

// An `accepted` run has been dispatched but has not spawned a crank process, so there is nothing to
// SIGSTOP. Offering Pause there is a button that cannot work.
func TestDispatchedRunOffersNoPause(t *testing.T) {
	if hasKey(boardActions(card("r", withStatus("accepted"))), "pause") {
		t.Fatal("an accepted (not yet running) card must not offer Pause")
	}
}

func TestPausedRunResumesTheSameProcess(t *testing.T) {
	acts := boardActions(card("r", withStatus("paused")))
	if acts[0].Key != "resume" || acts[0].Path != "unpause" {
		t.Fatalf("primary = %+v, want resume→unpause", acts[0])
	}
	if hasKey(acts, "continue") {
		t.Fatal("a paused run's process is alive — Continue (retry/resume) would be a different verb")
	}
}

func TestUnscheduledCardPromotesRatherThanStarts(t *testing.T) {
	c := card("i")
	c.Unscheduled = true
	acts := boardActions(c)
	if acts[0].Key != "promote" {
		t.Fatalf("primary = %q, want promote (there is no run to start yet)", acts[0].Key)
	}
}

func TestDestructiveActionsCarryAConfirm(t *testing.T) {
	for _, status := range []string{"queued", "failed", "needs_approval", "done", "running", "paused", "killed"} {
		for _, a := range boardActions(card("r", withStatus(status))) {
			if a.Danger && a.Confirm == "" {
				t.Fatalf("%s/%s is destructive with no confirmation copy", status, a.Key)
			}
		}
	}
}

// Every offered action must name a real endpoint segment, or the key does something else entirely
// (promote, rank) and deliberately carries no path.
func TestEveryActionPathIsARealEndpoint(t *testing.T) {
	known := map[string]bool{
		"start": true, "accept": true, "requeue": true, "retry": true, "restart": true,
		"resume": true, "discard": true, "archive": true, "cancel": true, "pause": true, "unpause": true,
	}
	pathless := map[string]bool{"promote": true, "delete": true, "rank": true}

	for _, status := range []string{"queued", "failed", "needs_approval", "done", "running", "accepted", "paused", "killed"} {
		for _, a := range boardActions(card("r", withStatus(status))) {
			if a.Path == "" {
				if !pathless[a.Key] {
					t.Fatalf("%s/%s has no endpoint and is not a known pathless action", status, a.Key)
				}
				continue
			}
			if !known[a.Path] {
				t.Fatalf("%s/%s posts to unknown endpoint %q", status, a.Key, a.Path)
			}
		}
	}
}

// Parity with the web. The terminal board and the web board offer moves on the same card, and the
// only defence against them drifting apart is reading the other one. This test parses the STATUS
// CASES out of run-actions.tsx and fails if the web grows a state the terminal has never heard of.
//
// It asserts coverage, not identical lists: the terminal narrows one case on purpose (needs_approval,
// where the pause REASON the web filters by is not in the board payload — see boardActions). A new
// case appearing on the web with no counterpart here is the drift worth catching.
func TestBoardActionsCoverEveryWebStatus(t *testing.T) {
	src, err := os.ReadFile("web/src/components/run-actions.tsx")
	if err != nil {
		t.Skipf("web source not available (%v) — parity is checked where the web tree is present", err)
	}
	body := string(src)
	start := strings.Index(body, "function actionsFor(")
	if start < 0 {
		t.Skip("actionsFor has moved; parity test needs updating with it")
	}
	end := strings.Index(body[start:], "\n}")
	if end < 0 {
		end = len(body) - start
	}
	cases := regexp.MustCompile(`case "([a-z_]+)":`).FindAllStringSubmatch(body[start:start+end], -1)
	if len(cases) < 5 {
		t.Fatalf("parsed only %d web statuses — the parser is looking at the wrong thing", len(cases))
	}

	for _, m := range cases {
		status := m[1]
		got := boardActions(card("r", withStatus(status)))
		if len(got) == 0 {
			t.Errorf("web offers actions for status %q and the terminal board offers none", status)
		}
	}
}

// The board is read every few seconds and acted on by keypress; an action list must never depend on
// mutable shared state.
func TestBoardActionsIsPure(t *testing.T) {
	c := card("r", withStatus("running"))
	first := boardActions(c)
	second := boardActions(c)
	if len(first) != len(second) {
		t.Fatal("boardActions is not deterministic")
	}
	first[0].Label = "MUTATED"
	if boardActions(c)[0].Label == "MUTATED" {
		t.Fatal("boardActions handed out a shared slice — a caller mutated the matrix")
	}
}

func TestPrimaryActionIsTheFirstOffered(t *testing.T) {
	c := card("r", withStatus("done"))
	p, ok := primaryAction(c)
	if !ok || p.Key != "accept" {
		t.Fatalf("primary on a Review card = %+v ok=%v, want accept", p, ok)
	}
	_ = api.ColReview
}
