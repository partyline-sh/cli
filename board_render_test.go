package main

import (
	"regexp"
	"strings"
	"testing"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/brand"
)

// ansiRe strips colour so a test can assert on what a reader SEES rather than on escape bytes.
var ansiRe = regexp.MustCompile("\x1b\\[[0-9;?]*[a-zA-Z]")

func plain(s string) string { return ansiRe.ReplaceAllString(s, "") }

func testModel(w, h int, b *api.Board) *boardModel {
	m := newBoardModel()
	m.board, m.w, m.h = b, w, h
	m.clamp()
	return m
}

func fullBoard() *api.Board {
	running := card("r1", withStatus("running"))
	running.Started, running.Machine, running.LastLine = true, "monolith", "running the test suite"
	running.Done, running.Total = 2, 5

	failed := card("r2", withStatus("failed"))
	failed.Detail = "exit status 1"

	review := card("r3", withStatus("done"))
	review.PRURL = "https://github.com/o/r/pull/9"

	return &api.Board{
		Backlog:  []api.BoardCard{card("r0")},
		Building: []api.BoardCard{running},
		Blocked:  []api.BoardCard{failed},
		Review:   []api.BoardCard{review},
		Accepted: []api.BoardCard{card("r4", withStatus("done"))},
	}
}

// The frame must be exactly as tall as the terminal. One line too many scrolls the screen on every
// repaint, which in an alt-screen TUI reads as the whole board juddering.
func TestFrameFitsTheTerminalExactly(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 30}, {200, 50}} {
		w, h := size[0], size[1]
		m := testModel(w, h, fullBoard())
		body := strings.Split(m.frame(), "\r\n")
		if len(body) != h {
			t.Errorf("%dx%d: frame is %d lines, want %d", w, h, len(body), h)
		}
	}
}

// No rendered line may exceed the terminal width, or it wraps and every line below it is off by one
// for the rest of the paint.
func TestNoLineExceedsTerminalWidth(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}, {40, 20}} {
		w, h := size[0], size[1]
		m := testModel(w, h, fullBoard())
		for i, line := range strings.Split(m.frame(), "\r\n") {
			if got := brand.VisWidth(plain(line)); got > w {
				t.Errorf("%dx%d: line %d is %d columns wide: %q", w, h, i, got, plain(line))
			}
		}
	}
}

func TestFrameShowsEveryColumnAndCount(t *testing.T) {
	m := testModel(160, 40, fullBoard())
	out := plain(m.frame())
	for _, want := range []string{"Backlog", "Building", "Blocked", "Review", "Accepted"} {
		if !strings.Contains(out, want) {
			t.Errorf("frame does not name the %s column", want)
		}
	}
	if !strings.Contains(out, "running the test suite") {
		t.Error("a building card must show what the agent last said")
	}
	if !strings.Contains(out, "exit status 1") {
		t.Error("a failed card must show why it failed")
	}
	if !strings.Contains(out, "2/5") {
		t.Error("a card with tasks must show its progress")
	}
}

// The urgent count is the header's lead, and it is the number that decides whether the board is
// worth looking at right now.
func TestHeaderLeadsWithWhatNeedsAHuman(t *testing.T) {
	m := testModel(160, 40, fullBoard())
	if !strings.Contains(plain(m.frame()), "1 need you") {
		t.Error("header must say how many cards need a human")
	}
	empty := testModel(160, 40, &api.Board{})
	if strings.Contains(plain(empty.frame()), "need you") {
		t.Error("an empty board must not claim anything needs a human")
	}
}

// A board with nothing on it should teach the next step, not print five copies of the word "empty".
func TestEmptyColumnsTeachTheNextStep(t *testing.T) {
	m := testModel(160, 40, &api.Board{})
	out := plain(m.frame())
	if !strings.Contains(out, "press n to add work") {
		t.Error("the empty backlog must say how to add work")
	}
	if strings.Count(out, "nothing") < 3 {
		t.Error("every empty column should say what it is empty OF")
	}
}

// A narrow terminal drops to one column rather than rendering five unreadable ones, and the tab
// strip is what still says the others exist — abbreviated if it must be, but never truncated off
// the right-hand edge, which is where Review and Accepted live.
func TestNarrowTerminalKeepsEveryColumnInTheTabStrip(t *testing.T) {
	m := testModel(40, 24, fullBoard())
	out := plain(m.frame())
	for _, want := range []string{"Bac", "Bui", "Blo", "Rev", "Acc"} {
		if !strings.Contains(out, want) {
			t.Errorf("narrow: the tab strip lost the %s… column", want)
		}
	}
}

// A wide terminal has room for real words, and should use them.
func TestWideTerminalSpellsColumnsOut(t *testing.T) {
	m := testModel(160, 40, fullBoard())
	if !strings.Contains(plain(m.frame()), "Accepted") {
		t.Error("a wide board should spell the column names out")
	}
}

func TestFrameSurvivesAbsurdTerminals(t *testing.T) {
	for _, size := range [][2]int{{1, 1}, {5, 3}, {10, 10}, {300, 2}} {
		m := testModel(size[0], size[1], fullBoard())
		if got := m.frame(); got == "" {
			t.Errorf("%v produced no frame", size)
		}
	}
}

func TestFrameWithNoBoardYetSaysSo(t *testing.T) {
	m := newBoardModel()
	m.w, m.h = 100, 30
	if !strings.Contains(plain(m.frame()), "reading the board") {
		t.Error("before the first load the board should say it is loading, not look empty")
	}
}

// The status line is where an action's outcome lands, and a refusal must be legible rather than
// silently swallowed.
func TestStatusLineShowsToastsAndErrors(t *testing.T) {
	m := testModel(100, 30, fullBoard())
	m.setToast("Accept — done", false)
	if !strings.Contains(plain(m.frame()), "Accept — done") {
		t.Error("a toast must reach the screen")
	}

	m2 := testModel(100, 30, fullBoard())
	m2.err = errString("network is down")
	if !strings.Contains(plain(m2.frame()), "could not refresh") {
		t.Error("a refresh error must be visible, not silent")
	}
}

// A stale board with an error is better than an empty one: the operator can still read their work.
func TestErrorKeepsTheBoardVisible(t *testing.T) {
	m := testModel(120, 30, fullBoard())
	m.err = errString("timeout")
	out := plain(m.frame())
	if !strings.Contains(out, "task r1") {
		t.Error("an error must not blank the board — the cards stay readable")
	}
}

// The hint bar names what THIS card can do. A bar advertising keys the focused card has no use for
// teaches the wrong thing on every board that is not the one it was written against.
func TestHintBarIsCardSpecific(t *testing.T) {
	b := fullBoard()
	m := testModel(160, 40, b)

	// Focus the Review card, which has a PR.
	m.col = 3
	hints := plain(m.hintLine(160))
	if !strings.Contains(hints, "review diff") {
		t.Error("a Review card should offer the diff key")
	}
	if !strings.Contains(hints, "open PR") {
		t.Error("a card with a PR should offer to open it")
	}

	// The backlog card has neither.
	m.col = 0
	hints = plain(m.hintLine(160))
	if strings.Contains(hints, "open PR") {
		t.Error("a backlog card has no PR and must not offer to open one")
	}
}

func TestChainHeaderRendersFoldState(t *testing.T) {
	b := &api.Board{Backlog: []api.BoardCard{
		card("c1", withChain("ch"), withRank(2)),
		card("c2", withChain("ch"), withRank(1)),
	}}
	m := testModel(120, 30, b)
	if !strings.Contains(plain(m.frame()), "chain · 2 steps") {
		t.Error("a chain should render a header naming how many steps it has")
	}

	m.collapsed["ch"] = true
	out := plain(m.frame())
	if strings.Contains(out, "task c2") {
		t.Error("a collapsed chain must hide its members")
	}
}

// tileDetail's priority is the contract: a card can carry several of these at once and the one
// shown has to be the one that changes what the reader does.
func TestTileDetailPriority(t *testing.T) {
	c := card("x", withStatus("running"))
	c.LastLine = "compiling"
	c.PRURL = "https://example.com/pr/1"
	c.ChainBlocker = &struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Task   string `json:"task"`
	}{ID: "r0", Status: "needs_approval", Task: "earlier step"}

	if got := tileDetail(c); !strings.Contains(got, "earlier step") {
		t.Fatalf("detail = %q, want the chain blocker — it is somebody else's decision", got)
	}

	c.ChainBlocker = nil
	if got := tileDetail(c); got != "https://example.com/pr/1" {
		t.Fatalf("detail = %q, want the PR", got)
	}

	c.PRURL = ""
	if got := tileDetail(c); got != "compiling" {
		t.Fatalf("detail = %q, want the live log line", got)
	}

	if got := tileDetail(card("bare")); got != "—" {
		t.Fatalf("a card with nothing to say should render a dash, got %q", got)
	}
}

// Scrolling is measured in LINES, not rows: a chain header is one line and a tile is three, so a
// row-counting scroll loses the cursor whenever the rows above it are tiles.
//
// Asserted the way a person would check it — the focused card's text is ON SCREEN — rather than by
// re-deriving the layout arithmetic here, which would only prove the test agrees with itself.
func TestScrollKeepsTheCursorVisible(t *testing.T) {
	var cards []api.BoardCard
	for i := 0; i < 30; i++ {
		c := card("card"+string(rune('A'+i)), withRank(float64(30-i)))
		c.Task = "unique task " + string(rune('A'+i))
		cards = append(cards, c)
	}
	m := testModel(120, 24, &api.Board{Backlog: cards})

	for i := 0; i < 29; i++ {
		m.moveCursor(1)
		focused, ok := m.focused()
		if !ok {
			t.Fatalf("step %d: nothing focused", i)
		}
		if got := plain(m.frame()); !strings.Contains(got, focused.Task) {
			t.Fatalf("step %d: the focused card %q is not on screen", i, focused.Task)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }
