package main

import "strings"
import "testing"

func TestPairWindowNameKeepsBothSides(t *testing.T) {
	// A pair must not be truncated back to the single-session budget — the second name is the
	// whole point of the composite.
	pair := "ACR POS │ codex · acr-cloud-aggregator"
	got := tmuxPairWindowName(pair)
	if !strings.Contains(got, "│") {
		t.Fatalf("separator lost: %q", got)
	}
	if !strings.HasPrefix(got, "ACR POS") {
		t.Errorf("first session missing: %q", got)
	}
	if !strings.Contains(got, "codex") {
		t.Errorf("second session missing: %q", got)
	}
	// A lone session keeps the old 24-column budget.
	long := strings.Repeat("x", 60)
	if n := len([]rune(tmuxPairWindowName(long))); n != 24 {
		t.Errorf("single-session budget changed: got %d runes, want 24", n)
	}
	if tmuxPairWindowName("  ") != "session" {
		t.Error("blank should fall back to \"session\"")
	}
}

// Closing one half of a pair must re-name the window, or the survivor wears the dead session's
// name. Observed live: tab 6 read "codex · acr-cloud-aggreg" over a pane running ACR POS — naming
// a session that no longer existed and misnaming the one that did.
func TestCloseIsMarkedForRename(t *testing.T) {
	var closeItem, mergeItem *tmuxMenuItem
	items := tmuxCommandItems("/usr/bin/ptln")
	for i := range items {
		switch items[i].key {
		case "x":
			closeItem = &items[i]
		case "|":
			mergeItem = &items[i]
		}
	}
	if closeItem == nil {
		t.Fatal("no close row in the menu")
	}
	if !closeItem.rename {
		t.Error("closing a session does not re-name its window; a surviving pair-mate keeps the dead session's name")
	}
	// merge/brk rename through tmuxRearrange, so they must NOT also carry the flag — the generic
	// path never runs for them and a true here would read as if it did.
	if mergeItem != nil && mergeItem.rename {
		t.Error("| renames via tmuxRearrange; the generic flag would be misleading")
	}
}
