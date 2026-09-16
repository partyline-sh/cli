package main

import (
	"testing"

	"partyline.sh/partyline/internal/api"
)

// backlogDropRank is the arithmetic the whole carry gesture commits through — pin every landing.
func TestBacklogDropRank(t *testing.T) {
	cards := []api.BoardCard{ // display order: rank strictly descending
		{ID: "a", Rank: 40},
		{ID: "b", Rank: 30},
		{ID: "c", Rank: 20},
		{ID: "d", Rank: 10},
	}

	// Carry d up onto b: lands ABOVE b, between a and b.
	if r, ok := backlogDropRank(cards, "d", "b"); !ok || r <= 30 || r >= 40 {
		t.Fatalf("above-b rank = %v ok=%v, want between 30 and 40", r, ok)
	}
	// Carry d onto a (the top): above everything.
	if r, ok := backlogDropRank(cards, "d", "a"); !ok || r <= 40 {
		t.Fatalf("top rank = %v ok=%v, want > 40", r, ok)
	}
	// Carry a down onto d — d is the LAST card, so the drop goes BELOW it: the true bottom,
	// which a drop-above rule could otherwise never reach.
	if r, ok := backlogDropRank(cards, "a", "d"); !ok || r >= 10 {
		t.Fatalf("bottom rank = %v ok=%v, want < 10", r, ok)
	}
	// Carry a onto c: c is last-but-one; with a removed, c is not last, so above-c means
	// between b and c.
	if r, ok := backlogDropRank(cards, "a", "c"); !ok || r <= 20 || r >= 30 {
		t.Fatalf("above-c rank = %v ok=%v, want between 20 and 30", r, ok)
	}
	// Dropped on itself: nothing to do.
	if _, ok := backlogDropRank(cards, "b", "b"); ok {
		t.Fatal("dropping a card on itself must be a no-op")
	}
	// A target that is not in the column (refresh raced the carry): refuse rather than guess.
	if _, ok := backlogDropRank(cards, "a", "gone"); ok {
		t.Fatal("a vanished target must not produce a rank")
	}
}

// The legal-landing set drives which columns ←/→ will even stop on. A partyline card's set is
// its transition destinations plus home; grabbing a card with neither is refused upstream.
func TestCarryLegalColumnsForPartylineCard(t *testing.T) {
	card := api.BoardCard{ID: "r1", Status: "failed", Column: api.ColBlocked}
	legal := map[api.BoardColumn]bool{card.Column: true}
	for _, act := range boardActions(card) {
		if dest, ok := moveDestination(act.Key); ok {
			legal[dest] = true
		}
	}
	for _, want := range []api.BoardColumn{api.ColBlocked, api.ColBuilding, api.ColBacklog} {
		if !legal[want] {
			t.Errorf("a failed card must be able to land in %s", want)
		}
	}
	if legal[api.ColAccepted] {
		t.Error("a failed card must NOT be able to land in Accepted — there is no transition there")
	}
}
