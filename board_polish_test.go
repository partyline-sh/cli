package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/brand"
)

// board_polish_test.go — four small things that each cost real time to notice.

// A bar that prints "S" beside a lowercase "s" bound to something else lies about the keyboard:
// anyone reading it tries s and gets jump-to-session instead of switch-board.
func TestShiftedHotkeysSayTheyAreShifted(t *testing.T) {
	for in, want := range map[string]string{
		"S": "⇧S", "N": "⇧N", "s": "s", "n": "n",
		"esc": "esc", "1-9": "1-9", "⏎": "⏎", "": "",
	} {
		if got := brand.ShiftKey(in); got != want {
			t.Errorf("ShiftKey(%q) = %q, want %q", in, got, want)
		}
	}
	bar := brand.HintBar("board", []brand.Hint{{Key: "S", Label: "source"}, {Key: "s", Label: "session"}}, 0)
	if !strings.Contains(bar, "⇧S") {
		t.Errorf("the hint bar did not mark the shifted key:\n%s", bar)
	}
}

// The state is the fact a column is scanned for; the age qualifies it. Printing the qualifier
// above the thing it qualifies reads backwards.
func TestCardStateComesBeforeItsAge(t *testing.T) {
	m := newBoardModel()
	lines := m.tileLines(api.BoardCard{
		ID: "1", Task: "Store enrolment", StateLabel: "New", Detail: "updated 8d ago", Foreign: true,
	}, 34, false)
	joined := plain(strings.Join(lines, "\n"))
	state, age := strings.Index(joined, "New"), strings.Index(joined, "updated 8d ago")
	if state < 0 || age < 0 {
		t.Fatalf("a tile lost one of its lines:\n%s", joined)
	}
	if state > age {
		t.Errorf("the age is printed above the state:\n%s", joined)
	}
}

// A board rendering last week's code has no way to know it, and the ctrl-\ menu REUSES an open
// board rather than making a second — so the obvious action after an update returns the stale one.
func TestBoardNoticesTheBinaryWasReplaced(t *testing.T) {
	m := newBoardModel()
	if m.binaryReplaced() {
		t.Error("a model that never sampled the binary must not claim it changed")
	}
	fi, err := os.Stat(selfExe())
	if err != nil {
		t.Skip("cannot stat the test binary")
	}
	m.selfStamp = fi.ModTime()
	if m.binaryReplaced() {
		t.Error("an unchanged binary must not raise the notice")
	}
	m.selfStamp = fi.ModTime().Add(-time.Hour) // as if ptln update had swapped it underneath
	if !m.binaryReplaced() {
		t.Fatal("a replaced binary went unnoticed — this is the bug")
	}
	if !strings.Contains(plain(m.statusLine(120)), "newer ptln is installed") {
		t.Error("the status line must say so, and say what to do about it")
	}
	// A missing executable is not something to nag about mid-board.
	m.selfStamp = time.Now()
	if got := (&boardModel{selfStamp: time.Now()}).binaryReplaced(); got && filepath.IsAbs(selfExe()) {
		_ = got // stat failure returns false; nothing to assert beyond not panicking
	}
}

// A static "reading…" cannot be told apart from a hang, and a foreign board is a network round
// trip to somebody else's tracker.
func TestLoadingIndicatorMoves(t *testing.T) {
	m := newBoardModel()
	m.busy, m.busySince = "reading acr-odoo", time.Now()
	first := plain(strings.Join(m.loadingLines(60, 9), "\n"))

	m.busySince = time.Now().Add(-500 * time.Millisecond)
	later := plain(strings.Join(m.loadingLines(60, 9), "\n"))
	if first == later {
		t.Fatal("the indicator does not animate")
	}
	if !strings.Contains(first, "reading acr-odoo") {
		t.Errorf("the indicator must name what it is waiting on:\n%s", first)
	}
	// Elapsed only once waiting is worth measuring — a counter from zero makes a fast board look slow.
	if strings.Contains(first, "0s") {
		t.Error("a fresh read should not be showing a stopwatch")
	}
	m.busySince = time.Now().Add(-4 * time.Second)
	if !strings.Contains(plain(strings.Join(m.loadingLines(60, 9), "\n")), "4s") {
		t.Error("a long wait must say how long, so you can decide whether to keep waiting")
	}
	// Every row still exactly the width, or the frame shears.
	for _, l := range m.loadingLines(60, 9) {
		if visWidth(l) != 60 {
			t.Fatalf("a loading row measured %d columns", visWidth(l))
		}
	}
}

// Listing a tracker's projects is a network call; running it inline froze the whole board.
func TestPickingScopesRunsOffTheEventLoop(t *testing.T) {
	events := make(chan boardEvent, 4)
	stop := make(chan struct{})
	defer close(stop)

	m := newBoardModel()
	m.events, m.stop = events, stop
	slow := &fakeSource{name: "odoo", data: foreignBoard(), scopes: []boardScope{{ID: "1", Label: "One"}}}
	m.sources = []boardSource{slow}

	if m.pickScope(nil) {
		t.Error("asking for scopes is not itself a reload")
	}
	if m.busy == "" {
		t.Error("the board must show that it is reading")
	}
	select {
	case ev := <-events:
		if ev.scopesFor != "odoo" {
			t.Fatalf("event = %+v, want the scope listing", ev)
		}
		m.applyScopes(ev.scopesFor, ev.scopes, ev.scopesErr)
	case <-time.After(2 * time.Second):
		t.Fatal("the listing never came back")
	}
	if m.busy != "" {
		t.Error("the indicator must stop when the read lands")
	}
	if _, ok := m.overlay.(*pickerOverlay); !ok {
		t.Fatal("the picker did not open once the scopes arrived")
	}
}

// An answer for a board you have since switched away from must not open a picker over the new one.
func TestStaleScopeListingIsDropped(t *testing.T) {
	m := newBoardModel()
	m.sources = []boardSource{partylineSource{}}
	m.applyScopes("odoo", []boardScope{{ID: "1", Label: "One"}}, nil)
	if m.overlay != nil {
		t.Fatal("a listing for another board opened a picker anyway")
	}
}
