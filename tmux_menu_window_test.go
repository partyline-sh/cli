package main

import (
	"regexp"
	"strings"
	"testing"

	"partyline.sh/partyline/internal/brand"
)

// tmux_menu_window_test.go — the menu on a terminal too short for it.
//
// The bug: the popup asked for len(items)+4 rows, tmux clamped it to the screen, and the TUI went
// on printing every row anyway. The top scrolled off, so the FIRST session was simply not in the
// menu — a window you own, with nothing on screen to say it had been cut.

func menuItems(n int) []tmuxMenuItem {
	out := make([]tmuxMenuItem, n)
	for i := range out {
		out[i] = tmuxMenuItem{label: "row"}
	}
	return out
}

func TestMenuHeightIsClampedToTheClient(t *testing.T) {
	items := menuItems(30)
	if h := tmuxMenuHeight(items, 0); h != 34 {
		t.Errorf("with an unknown client height, ask for everything: %d", h)
	}
	if h := tmuxMenuHeight(items, 40); h != 34 {
		t.Errorf("a client with room should get the full list: %d", h)
	}
	h := tmuxMenuHeight(items, 22)
	if h > 22 {
		t.Fatalf("popup height %d exceeds the 22-row client — this is the bug", h)
	}
}

// Whatever the height, the selection stays on screen and nothing is dropped without saying so.
func TestMenuWindowKeepsTheSelectionVisible(t *testing.T) {
	items := menuItems(30)
	for _, sel := range []int{0, 7, 15, 29} {
		start, end := menuWindow(items, sel, 20)
		if sel < start || sel >= end {
			t.Errorf("sel %d fell outside the window [%d,%d)", sel, start, end)
		}
		if end > len(items) || start < 0 {
			t.Errorf("window [%d,%d) is out of range", start, end)
		}
	}
	// A screen with room shows everything.
	if start, end := menuWindow(items, 0, 40); start != 0 || end != 30 {
		t.Errorf("a tall client should window nothing, got [%d,%d)", start, end)
	}
	// Unknown height keeps the old behaviour rather than guessing.
	if start, end := menuWindow(items, 0, 0); start != 0 || end != 30 {
		t.Errorf("unknown height must not truncate, got [%d,%d)", start, end)
	}
}

// The first session must survive a short screen — that is the whole defect.
func TestShortScreenStillReachesTheFirstSession(t *testing.T) {
	items := append([]tmuxMenuItem{{label: "XERO RECEIPTS", num: "1", paneID: "%1"}}, menuItems(29)...)
	out := renderTmuxMenu(items, 0, -1, 20)
	if !strings.Contains(out, "XERO RECEIPTS") {
		t.Fatal("the first session vanished from a short menu")
	}
	if !strings.Contains(out, "more") {
		t.Error("a truncated list must say it was truncated")
	}
}

// A label longer than the popup used to run past the border and wrap into the next row.
func TestLongLabelsAreClippedNotWrapped(t *testing.T) {
	long := strings.Repeat("move the highlighted session back to its own window ", 3)
	out := renderTmuxMenu([]tmuxMenuItem{{label: long, key: "-"}}, -1, -1, 20)
	for _, line := range strings.Split(out, "\r\n") {
		if w := brand.VisWidth(line); w > menuLabelWidth+8 {
			t.Fatalf("a rendered row is %d columns wide — it will wrap in a 56-column popup", w)
		}
	}
}

// ansi strips escape sequences, because a colour code like \x1b[38;2;255;152;56m is full of digits
// and counting characters through it measures the palette, not the text.
var ansi = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// The index was printed twice on single-digit rows ("3  3·NAME") and once on double-digit ones.
func TestTheWindowIndexAppearsExactlyOnce(t *testing.T) {
	out := ansi.ReplaceAllString(
		renderTmuxMenu([]tmuxMenuItem{{label: "FLEET MANAGER", key: "3", num: "3", paneID: "%3"}}, -1, -1, 0), "")
	if n := strings.Count(out, "3"); n != 1 {
		t.Errorf("the index appears %d times in %q, want once", n, out)
	}
	// A window too wide to be a hotkey still shows its number.
	out = ansi.ReplaceAllString(
		renderTmuxMenu([]tmuxMenuItem{{label: "LANDSEARCH", num: "10", paneID: "%10"}}, -1, -1, 0), "")
	if !strings.Contains(out, "10") {
		t.Errorf("an unbound window lost its index: %q", out)
	}
	if strings.Contains(out, "10·") {
		t.Error("the index is still glued to the label as well as the gutter")
	}
}
