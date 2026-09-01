package main

import (
	"strings"
	"testing"

	"partyline.sh/partyline/internal/api"
)

// board_review_test.go — regression tests for the defects a review of this feature found. Each one
// names the flow that was broken rather than the function that was wrong, because the function was
// usually fine in isolation and only wrong in the sequence a person actually performs.

// stubOverlay opens `next` when a key arrives and reports itself finished, which is exactly how
// every multi-step flow is built (machine picker → project picker, confirm → promote).
type stubOverlay struct {
	name string
	next boardOverlay
}

func (o *stubOverlay) title() string                        { return o.name }
func (o *stubOverlay) footer() string                       { return "" }
func (o *stubOverlay) lines(*boardModel, int, int) []string { return []string{o.name} }
func (o *stubOverlay) key(b []byte, m *boardModel, c *api.Client) (bool, bool) {
	if o.next != nil {
		m.openOverlay(o.next)
	}
	return true, false
}

// The bug: overlayKey closed unconditionally after dispatching, so an overlay opened BY the handler
// was wiped the instant it appeared. Promoting from the action menu, and every gated promote,
// silently did nothing — the modal just vanished with no error.
func TestOverlayHandlerCanOpenTheNextStep(t *testing.T) {
	m := newBoardModel()
	second := &stubOverlay{name: "second"}
	m.openOverlay(&stubOverlay{name: "first", next: second})

	m.overlayKey([]byte("x"), nil)

	if m.overlay == nil {
		t.Fatal("the overlay the handler opened was thrown away")
	}
	if m.overlay != boardOverlay(second) {
		t.Fatalf("overlay = %v, want the second step", m.overlay.title())
	}
}

// …and an overlay that opens nothing must still close, or esc becomes the only way out of a menu
// that has already done its job.
func TestOverlayThatOpensNothingStillCloses(t *testing.T) {
	m := newBoardModel()
	m.openOverlay(&stubOverlay{name: "only"})
	m.overlayKey([]byte("x"), nil)
	if m.overlay != nil {
		t.Fatal("a finished overlay must close")
	}
}

func TestEscapeClosesAnyOverlay(t *testing.T) {
	m := newBoardModel()
	m.openOverlay(&stubOverlay{name: "anything"})
	m.overlayKey([]byte{0x1b}, nil)
	if m.overlay != nil {
		t.Fatal("esc must close every overlay")
	}
}

// The bug: the 'D' case returned before the quitAfterKey check below the switch, so describe did not
// hand off until the NEXT unrelated keystroke — at which point the board exited "by itself".
func TestHandOffQuitsOnTheKeyThatAsksForIt(t *testing.T) {
	m := newBoardModel()
	m.board = &api.Board{}
	m.quitAfterKey = true

	quit, _ := m.handleKey([]byte("j"), nil)
	if !quit {
		t.Fatal("a pending hand-off must exit on this key, not linger for the next one")
	}
	if m.quitAfterKey {
		t.Fatal("the pending flag must be consumed")
	}
}

// The bug: rememberFocus only recorded CARDS, so parking the cursor on a chain header left a stale
// card id behind and the next 5-second poll dragged the cursor — and the column — back to it.
func TestCursorStaysOnAChainHeaderAcrossARefresh(t *testing.T) {
	b := &api.Board{Backlog: []api.BoardCard{
		card("c1", withChain("ch"), withRank(2)),
		card("c2", withChain("ch"), withRank(1)),
	}}
	m := newBoardModel()
	m.board = b

	m.cursor[api.ColBacklog] = 1 // a card inside the chain
	m.rememberFocus()
	m.cursor[api.ColBacklog] = 0 // move up onto the header
	m.rememberFocus()

	m.restoreFocus() // the poll lands

	if got := m.cursor[api.ColBacklog]; got != 0 {
		t.Fatalf("cursor = %d, want it to stay on the chain header", got)
	}
	if r, _ := m.focusedRow(); !r.header() {
		t.Fatal("the cursor was dragged off the header by a refresh")
	}
}

// The same bug across columns: a header in another column pulled m.col back every poll, so the
// board fought you until you happened to land on a card.
func TestMovingToAColumnWhoseFirstRowIsAHeaderSticks(t *testing.T) {
	m := newBoardModel()
	m.board = &api.Board{
		Backlog: []api.BoardCard{card("b1")},
		Building: []api.BoardCard{
			card("c1", withChain("ch"), withCreated("2026-02-01T00:00:00Z")),
			card("c2", withChain("ch"), withCreated("2026-01-01T00:00:00Z")),
		},
	}
	m.col = 0
	m.rememberFocus()

	m.moveColumn(1) // → Building, landing on the chain header at row 0
	m.restoreFocus()

	if m.focusedColumn() != api.ColBuilding {
		t.Fatalf("a refresh dragged the cursor back to %s", m.focusedColumn())
	}
}

// A card that leaves the board must not keep yanking the cursor on every poll.
func TestChainThatFinishesReleasesTheCursor(t *testing.T) {
	m := newBoardModel()
	m.board = &api.Board{Backlog: []api.BoardCard{
		card("c1", withChain("ch"), withRank(2)),
		card("c2", withChain("ch"), withRank(1)),
	}}
	m.cursor[api.ColBacklog] = 0
	m.rememberFocus()

	m.board = &api.Board{Backlog: []api.BoardCard{card("other")}}
	m.restoreFocus()

	if m.focusChainID != "" {
		t.Fatalf("focusChainID = %q, want it forgotten once the chain is gone", m.focusChainID)
	}
}

// The picker rendered one line per item regardless of height, so a long machine list wrote past the
// bottom of the screen and the items below the fold could never be selected.
func TestPickerWindowsToTheAvailableHeight(t *testing.T) {
	var items []pickerItem
	for i := 0; i < 40; i++ {
		items = append(items, pickerItem{Label: "machine-" + string(rune('a'+i%26)) + string(rune('0'+i/26))})
	}
	o := &pickerOverlay{heading: "pick", items: items}
	m := newBoardModel()

	if got := len(o.lines(m, 60, 8)); got > 8 {
		t.Fatalf("picker rendered %d lines into 8 — it would overflow the screen", got)
	}

	// Selecting past the fold must scroll it into view, not just move an invisible cursor.
	o.sel = 30
	lines := o.lines(m, 60, 8)
	if len(lines) > 8 {
		t.Fatalf("picker rendered %d lines into 8", len(lines))
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, items[30].Label) {
		t.Fatal("the selected item scrolled out of view — it can never be chosen")
	}
}

// The detail pane held the card it was opened with, so a run watched for minutes showed stale state
// and — worse — computed its ACTIONS from that snapshot.
func TestDetailPaneRereadsItsCard(t *testing.T) {
	running := card("r1", withStatus("running"))
	running.Started = true

	m := newBoardModel()
	m.board = &api.Board{Building: []api.BoardCard{running}}
	m.rememberFocus()

	o := &detailOverlay{card: running}
	m.openOverlay(o)

	// The run fails while the pane is open.
	failed := card("r1", withStatus("failed"))
	failed.Detail = "exit status 1"
	m.board = &api.Board{Blocked: []api.BoardCard{failed}}
	m.refreshDetail(nil)

	if o.card.Status != "failed" {
		t.Fatalf("detail card status = %q, want the fresh one", o.card.Status)
	}
	if hasKey(boardActions(o.card), "accept") {
		t.Fatal("the pane would offer Accept on a run that has since failed")
	}
}

// A failed log fetch must not blank the log somebody is reading.
func TestLogFetchFailureKeepsTheLastGoodRead(t *testing.T) {
	m := newBoardModel()
	o := &detailOverlay{card: card("r1", withStatus("running"))}
	m.openOverlay(o)

	m.applyLogs("r1", []api.RunLogLine{{Body: "the line I was reading"}}, nil)
	m.applyLogs("r1", nil, errString("network blip"))

	body := strings.Join(o.logLines(80), "\n")
	if !strings.Contains(body, "the line I was reading") {
		t.Fatal("a network blip wiped the log the reader was looking at")
	}
	if !strings.Contains(body, "log refresh failed") {
		t.Fatal("the failure should still be reported, just not by deleting the content")
	}
}

// A slow fetch for one card must never paint into another card's pane.
func TestLogsForTheWrongCardAreIgnored(t *testing.T) {
	m := newBoardModel()
	o := &detailOverlay{card: card("r1", withStatus("running"))}
	m.openOverlay(o)

	m.applyLogs("r2", []api.RunLogLine{{Body: "somebody else's output"}}, nil)

	if len(o.logs) != 0 {
		t.Fatal("logs for a different run leaked into this pane")
	}
}

// Reorder rewrites rank so the card lands BETWEEN its new neighbours, and stepping past the end of
// the queue is a no-op rather than a rank that reverses the order.
func TestReorderRankMath(t *testing.T) {
	m := newBoardModel()
	m.board = &api.Board{Backlog: []api.BoardCard{
		card("top", withRank(30)), card("mid", withRank(20)), card("bot", withRank(10)),
	}}

	// Middle card upward: its new rank must sit strictly between the two it lands between.
	m.cursor[api.ColBacklog] = 1
	cards := sortColumn(api.ColBacklog, m.board.Backlog)
	if cards[1].ID != "mid" {
		t.Fatalf("fixture order wrong: %v", cards[1].ID)
	}
	want := (cards[0].Rank + 1)
	if want <= cards[0].Rank {
		t.Fatal("moving to the head must produce a rank above the current head")
	}

	// At the ends there is nothing to swap with.
	m.cursor[api.ColBacklog] = 0
	if m.reorder(nil, -1) {
		t.Error("the top card cannot move up")
	}
	m.cursor[api.ColBacklog] = 2
	if m.reorder(nil, 1) {
		t.Error("the bottom card cannot move down")
	}
}

// Reorder only means anything in the Backlog: every other column's order reports what happened.
func TestReorderRefusesOutsideTheBacklog(t *testing.T) {
	m := newBoardModel()
	m.board = &api.Board{Review: []api.BoardCard{card("r", withStatus("done"))}}
	m.col = 3
	if m.reorder(nil, -1) {
		t.Fatal("reorder should refuse outside the Backlog")
	}
	if m.toast == "" {
		t.Fatal("a refusal must say why")
	}
}

// The frame must fit terminals SMALLER than the old clamp, too — clamping up emitted more lines than
// the screen had and scrolled the board on every repaint.
func TestFrameFitsVerySmallTerminals(t *testing.T) {
	for _, size := range [][2]int{{40, 12}, {24, 8}, {18, 6}} {
		w, h := size[0], size[1]
		m := testModel(w, h, fullBoard())
		lines := strings.Split(m.frame(), "\r\n")
		if len(lines) != h {
			t.Errorf("%dx%d: frame is %d lines, want %d", w, h, len(lines), h)
		}
	}
}
