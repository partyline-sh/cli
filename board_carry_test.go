package main

import (
	"reflect"
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

// carryBoard builds a model holding a Backlog of the given cards, cursor on cardIdx, no loop.
func carryBoard(cards []api.BoardCard) *boardModel {
	by := map[api.BoardColumn][]api.BoardCard{api.ColBacklog: cards}
	return &boardModel{
		data:      &boardData{ByColumn: by, Columns: []boardColumn{{Key: api.ColBacklog, Title: "Backlog"}}},
		cursor:    map[api.BoardColumn]int{},
		scroll:    map[api.BoardColumn]int{},
		collapsed: map[string]bool{},
	}
}

func backlogCards() []api.BoardCard {
	return []api.BoardCard{
		{ID: "a", Title: "A", Column: api.ColBacklog, Rank: 30},
		{ID: "b", Title: "B", Column: api.ColBacklog, Rank: 20},
		{ID: "c", Title: "C", Column: api.ColBacklog, Rank: 10},
	}
}

// The heart of the drag feel: while carried, the card renders AT THE CURSOR — gone from its
// real row, present where the arrows put it — so the screen always shows what ⏎ commits.
func TestCarryRowsFollowCursor(t *testing.T) {
	m := carryBoard(backlogCards())
	m.cursor[api.ColBacklog] = 0
	m.carry = &carryState{card: backlogCards()[0], from: api.ColBacklog,
		legal: map[api.BoardColumn]bool{api.ColBacklog: true}}

	ids := func() []string {
		var out []string
		for _, r := range m.rows(api.ColBacklog) {
			if r.Card != nil {
				out = append(out, r.Card.ID)
			}
		}
		return out
	}
	if got := ids(); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("at rest at own row: %v", got)
	}
	m.cursor[api.ColBacklog] = 1
	if got := ids(); !reflect.DeepEqual(got, []string{"b", "a", "c"}) {
		t.Fatalf("one down should render b,a,c — got %v", got)
	}
	m.cursor[api.ColBacklog] = 2
	if got := ids(); !reflect.DeepEqual(got, []string{"b", "c", "a"}) {
		t.Fatalf("bottom should render b,c,a — got %v", got)
	}
}

// The drop writes what the preview showed: carried to the bottom, the rank lands below the
// last card; carried between two, between their ranks. Local state updates before the write.
func TestCarryCommitMatchesPreview(t *testing.T) {
	m := carryBoard(backlogCards())
	m.cursor[api.ColBacklog] = 2 // preview: b, c, a — "a" at the bottom
	m.carry = &carryState{card: backlogCards()[0], from: api.ColBacklog,
		legal: map[api.BoardColumn]bool{api.ColBacklog: true}}

	var wrote float64
	m.rankWrite = func(id string, rank float64) error { wrote = rank; return nil }
	m.carryCommit(nil)

	if wrote >= 10 { // below C (rank 10)
		t.Fatalf("bottom drop must rank below the last card, wrote %v", wrote)
	}
	got := m.data.ByColumn[api.ColBacklog]
	for _, cdd := range got {
		if cdd.ID == "a" && cdd.Rank != wrote {
			t.Fatalf("local rank not applied optimistically: %v vs %v", cdd.Rank, wrote)
		}
	}
	if m.carry != nil {
		t.Fatal("carry must clear on commit")
	}
}

// A cross-column drop shows before the source confirms: the card leaves its column, arrives
// in the target, and carries the new column key.
func TestApplyLocalColumnMove(t *testing.T) {
	m := carryBoard(backlogCards())
	m.data.ByColumn[api.ColBuilding] = nil
	m.applyLocalColumnMove("b", api.ColBacklog, api.ColBuilding)
	if len(m.data.ByColumn[api.ColBacklog]) != 2 {
		t.Fatalf("card not removed from origin: %v", m.data.ByColumn[api.ColBacklog])
	}
	moved := m.data.ByColumn[api.ColBuilding]
	if len(moved) != 1 || moved[0].ID != "b" || moved[0].Column != api.ColBuilding {
		t.Fatalf("card not in target with new column: %+v", moved)
	}
}
