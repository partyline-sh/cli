package main

import (
	"encoding/json"
	"testing"

	"partyline.sh/partyline/internal/api"
)

// jsonUnmarshalString keeps the payload-shape tests readable: they exist to pin FIELD NAMES against
// the live endpoint, so the JSON should be the visible part of the test.
func jsonUnmarshalString(s string, v any) error { return json.Unmarshal([]byte(s), v) }

// pboard builds partyline's own board as boardData, the way the model now holds it.
func pboard(b *api.Board) *boardData { return boardFromAPI(b) }

func card(id string, opts ...func(*api.BoardCard)) api.BoardCard {
	c := api.BoardCard{ID: id, Task: "task " + id, Title: "proj", Status: "queued"}
	for _, o := range opts {
		o(&c)
	}
	return c
}

func withChain(ch string) func(*api.BoardCard) { return func(c *api.BoardCard) { c.ChainID = ch } }
func withRank(r float64) func(*api.BoardCard)  { return func(c *api.BoardCard) { c.Rank = r } }
func withCreated(s string) func(*api.BoardCard) {
	return func(c *api.BoardCard) { c.CreatedAt = s }
}
func withStatus(s string) func(*api.BoardCard) { return func(c *api.BoardCard) { c.Status = s } }

// The board API's last column is "accepted". The Go client read "shipped" for long enough that
// every Go consumer — read_board included — showed an empty last column and nobody noticed, because
// an empty column looks like an empty column. This pins the key to the payload.
func TestBoardUnmarshalsAcceptedColumn(t *testing.T) {
	var b api.Board
	if err := jsonUnmarshalString(`{"backlog":[],"building":[],"blocked":[],"review":[],
		"accepted":[{"id":"r1","task":"shipped thing","status":"done"}]}`, &b); err != nil {
		t.Fatal(err)
	}
	if len(b.Accepted) != 1 || b.Accepted[0].ID != "r1" {
		t.Fatalf("accepted column did not unmarshal: %+v", b.Accepted)
	}
	if got := b.Column(api.ColAccepted); len(got) != 1 {
		t.Fatalf("Column(accepted) = %d cards, want 1", len(got))
	}
}

func TestBoardCardWideFieldsUnmarshal(t *testing.T) {
	var b api.Board
	err := jsonUnmarshalString(`{"building":[{"id":"r1","task":"t","status":"running",
		"chainWaiting":true,"stalled":false,"lastLine":"running tests",
		"chainBlocker":{"id":"r0","status":"needs_approval","task":"earlier"},
		"readiness":3,"conflict":{"count":2,"resolvable":true}}]}`, &b)
	if err != nil {
		t.Fatal(err)
	}
	c := b.Building[0]
	if !c.ChainWaiting || c.LastLine != "running tests" {
		t.Fatalf("flat fields lost: %+v", c)
	}
	if c.ChainBlocker == nil || c.ChainBlocker.ID != "r0" {
		t.Fatalf("chainBlocker lost: %+v", c.ChainBlocker)
	}
	if c.Readiness == nil || *c.Readiness != 3 {
		t.Fatalf("readiness lost: %+v", c.Readiness)
	}
	if !c.StartBlockedByReadiness() {
		t.Fatal("readiness 3 is under the floor and must gate a start")
	}
	if c.Conflict == nil || c.Conflict.Count != 2 {
		t.Fatalf("conflict lost: %+v", c.Conflict)
	}
}

// Readiness 0 and "no plan item" are different answers, and a *int is the only way to tell them
// apart. A plain int would read an absent readiness as 0 and gate every hand-enqueued run.
func TestReadinessAbsentIsNotZero(t *testing.T) {
	var b api.Board
	if err := jsonUnmarshalString(`{"backlog":[{"id":"r1"},{"id":"r2","readiness":0}]}`, &b); err != nil {
		t.Fatal(err)
	}
	if b.Backlog[0].StartBlockedByReadiness() {
		t.Fatal("a run with no plan item must never be readiness-gated")
	}
	if !b.Backlog[1].StartBlockedByReadiness() {
		t.Fatal("readiness 0 IS under the floor and must gate")
	}
}

func TestSortColumnBacklogIsQueueOrder(t *testing.T) {
	cards := []api.BoardCard{
		card("a", withRank(1)), card("b", withRank(9)), card("c", withRank(5)),
	}
	got := sortColumn(api.ColBacklog, cards)
	want := []string{"b", "c", "a"}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("backlog order = %s at %d, want %s", got[i].ID, i, id)
		}
	}
}

func TestSortColumnOthersAreNewestFirst(t *testing.T) {
	cards := []api.BoardCard{
		card("old", withCreated("2026-01-01T00:00:00Z")),
		card("new", withCreated("2026-08-01T00:00:00Z")),
		card("mid", withCreated("2026-04-01T00:00:00Z")),
	}
	got := sortColumn(api.ColReview, cards)
	want := []string{"new", "mid", "old"}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("review order = %s at %d, want %s", got[i].ID, i, id)
		}
	}
}

// sortColumn must not reorder the caller's slice: the model hands it the live board and then reads
// that board again for counts.
func TestSortColumnDoesNotMutateInput(t *testing.T) {
	cards := []api.BoardCard{card("a", withRank(1)), card("b", withRank(9))}
	_ = sortColumn(api.ColBacklog, cards)
	if cards[0].ID != "a" {
		t.Fatal("sortColumn reordered its input")
	}
}

func TestColumnRowsGroupsChains(t *testing.T) {
	cards := []api.BoardCard{
		card("c1", withChain("ch"), withRank(9)),
		card("solo", withRank(8)),
		card("c2", withChain("ch"), withRank(7)),
	}
	rows := columnRows(api.ColBacklog, cards, map[string]bool{})
	// header, c1, solo, c2 — the header takes the position of the chain's FIRST member.
	if len(rows) != 4 || !rows[0].header() || rows[0].Count != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[1].Card.ID != "c1" || rows[2].Card.ID != "solo" || rows[3].Card.ID != "c2" {
		t.Fatalf("member order wrong: %+v", rows)
	}
}

func TestColumnRowsCollapsedChainHidesMembers(t *testing.T) {
	cards := []api.BoardCard{
		card("c1", withChain("ch"), withRank(9)),
		card("c2", withChain("ch"), withRank(7)),
	}
	rows := columnRows(api.ColBacklog, cards, map[string]bool{"ch": true})
	if len(rows) != 1 || !rows[0].header() {
		t.Fatalf("collapsed chain should be one header row, got %+v", rows)
	}
}

// A "chain" with one member on this board is not a group worth a header — the other members are in
// other columns, and a header over a single card is pure noise.
func TestColumnRowsSingleMemberChainGetsNoHeader(t *testing.T) {
	rows := columnRows(api.ColBacklog, []api.BoardCard{card("only", withChain("ch"))}, map[string]bool{})
	if len(rows) != 1 || rows[0].header() {
		t.Fatalf("want a bare card row, got %+v", rows)
	}
}

func TestVisibleColumnsWideShowsAll(t *testing.T) {
	start, count := visibleColumns(200, 0, 5)
	if start != 0 || count != 5 {
		t.Fatalf("wide terminal: start=%d count=%d, want 0/5", start, count)
	}
}

func TestVisibleColumnsNarrowShowsOne(t *testing.T) {
	start, count := visibleColumns(30, 3, 5)
	if count != 1 || start != 3 {
		t.Fatalf("narrow: start=%d count=%d, want start=3 count=1", start, count)
	}
}

// The middle tier is the one that actually has to be right: a half-width terminal shows a WINDOW of
// whole columns that always contains the focused one, and never runs off either end.
func TestVisibleColumnsWindowContainsFocus(t *testing.T) {
	for _, w := range []int{50, 60, 75, 99} {
		for focus := 0; focus < 5; focus++ {
			start, count := visibleColumns(w, focus, 5)
			if count < 1 {
				t.Fatalf("w=%d focus=%d: count=%d", w, focus, count)
			}
			if focus < start || focus >= start+count {
				t.Fatalf("w=%d focus=%d: window [%d,%d) excludes focus", w, focus, start, start+count)
			}
			if start < 0 || start+count > 5 {
				t.Fatalf("w=%d focus=%d: window [%d,%d) out of range", w, focus, start, start+count)
			}
			if count*minColWidth > w+count {
				t.Fatalf("w=%d: %d columns do not fit", w, count)
			}
		}
	}
}

// A one-column terminal is degenerate but must not divide by zero or return nothing.
func TestVisibleColumnsTinyTerminal(t *testing.T) {
	start, count := visibleColumns(1, 2, 5)
	if count != 1 || start != 2 {
		t.Fatalf("tiny: start=%d count=%d", start, count)
	}
	if s, c := visibleColumns(80, 0, 0); s != 0 || c != 0 {
		t.Fatalf("empty board: %d/%d", s, c)
	}
}

// The cursor must follow the CARD across a refresh, not the row index. This is the difference
// between accepting the run you were looking at and accepting whatever slid into its place.
func TestRestoreFocusFollowsCardAcrossColumns(t *testing.T) {
	m := newBoardModel()
	m.data = pboard(&api.Board{Backlog: []api.BoardCard{card("a"), card("b")}})
	m.col = 0
	m.cursor[api.ColBacklog] = 1
	m.rememberFocus()
	if m.focusID != "b" {
		t.Fatalf("focusID = %q", m.focusID)
	}

	// b starts building; a new card lands in the backlog where b was.
	m.data = pboard(&api.Board{
		Backlog:  []api.BoardCard{card("a"), card("c")},
		Building: []api.BoardCard{card("b", withStatus("running"))},
	})
	m.restoreFocus()

	if m.focusedColumn() != api.ColBuilding {
		t.Fatalf("cursor stayed in %s, want building", m.focusedColumn())
	}
	got, ok := m.focused()
	if !ok || got.ID != "b" {
		t.Fatalf("focused = %+v ok=%v, want card b", got, ok)
	}
}

func TestRestoreFocusForgetsCardThatLeftTheBoard(t *testing.T) {
	m := newBoardModel()
	m.data = pboard(&api.Board{Backlog: []api.BoardCard{card("a"), card("gone")}})
	m.cursor[api.ColBacklog] = 1
	m.rememberFocus()

	m.data = pboard(&api.Board{Backlog: []api.BoardCard{card("a")}})
	m.restoreFocus()

	if m.focusID != "" {
		t.Fatalf("focusID = %q, want cleared", m.focusID)
	}
	if got := m.cursor[api.ColBacklog]; got != 0 {
		t.Fatalf("cursor = %d, want clamped to 0", got)
	}
}

func TestMoveColumnSkipsEmptyColumns(t *testing.T) {
	m := newBoardModel()
	m.data = pboard(&api.Board{
		Backlog:  []api.BoardCard{card("a")},
		Review:   []api.BoardCard{card("r")},
		Accepted: nil,
	})
	m.col = 0
	m.moveColumn(1)
	if m.focusedColumn() != api.ColReview {
		t.Fatalf("→ landed on %s, want review (building and blocked are empty)", m.focusedColumn())
	}
}

func TestMoveColumnWrapsWithNothingOnTheBoard(t *testing.T) {
	m := newBoardModel()
	m.data = pboard(&api.Board{})
	m.col = 4
	m.moveColumn(1) // must not spin or panic
	if m.col != 0 {
		t.Fatalf("col = %d, want wrap to 0", m.col)
	}
}

func TestFocusedIsNilOnAChainHeader(t *testing.T) {
	m := newBoardModel()
	m.data = pboard(&api.Board{Backlog: []api.BoardCard{
		card("c1", withChain("ch"), withRank(2)),
		card("c2", withChain("ch"), withRank(1)),
	}})
	m.cursor[api.ColBacklog] = 0 // the header
	if _, ok := m.focused(); ok {
		t.Fatal("a chain header is not a card")
	}
	r, ok := m.focusedRow()
	if !ok || !r.header() {
		t.Fatalf("focusedRow = %+v ok=%v", r, ok)
	}
}

// cardState's ORDER is the contract: a card can be several things at once and the operator must be
// told the one that decides what they do next.
func TestCardStateChainWaitingOutranksStalled(t *testing.T) {
	c := card("x", withStatus("accepted"))
	c.ChainWaiting, c.Stalled = true, true
	label, urgent := cardState(c)
	if label != "waiting on chain" {
		t.Fatalf("label = %q, want the chain reason", label)
	}
	if urgent {
		t.Fatal("a chain-waiting card is idle by design — calling it urgent sends someone to fix nothing")
	}
}

func TestCardStateUrgencyFlags(t *testing.T) {
	for _, tc := range []struct {
		name   string
		c      api.BoardCard
		label  string
		urgent bool
	}{
		{"failed", card("a", withStatus("failed")), "failed", true},
		{"approval", card("b", withStatus("needs_approval")), "needs you", true},
		{"stalled", func() api.BoardCard { c := card("c", withStatus("running")); c.Stalled = true; return c }(), "stalled", true},
		{"starting", card("d", withStatus("running")), "starting…", false},
		{"building", func() api.BoardCard { c := card("e", withStatus("running")); c.Started = true; return c }(), "building", false},
		{"review", card("f", withStatus("done")), "ready to accept", false},
		{"findings", func() api.BoardCard { c := card("g", withStatus("done")); c.Attention = true; return c }(), "finished with findings", true},
		{"planned", func() api.BoardCard { c := card("h"); c.Unscheduled = true; return c }(), "planned", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			label, urgent := cardState(tc.c)
			if label != tc.label || urgent != tc.urgent {
				t.Fatalf("got (%q,%v) want (%q,%v)", label, urgent, tc.label, tc.urgent)
			}
		})
	}
}

func TestCountUrgent(t *testing.T) {
	b := &api.Board{
		Building: []api.BoardCard{card("ok", withStatus("running"))},
		Blocked:  []api.BoardCard{card("f", withStatus("failed")), card("n", withStatus("needs_approval"))},
		Review:   []api.BoardCard{card("d", withStatus("done"))},
	}
	if got := countUrgent(pboard(b)); got != 2 {
		t.Fatalf("urgent = %d, want 2", got)
	}
	if got := countUrgent(nil); got != 0 {
		t.Fatalf("nil board urgent = %d", got)
	}
}
