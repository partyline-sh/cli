package main

import (
	"testing"
)

func paneMenu() *aiMenu {
	m := &aiMenu{
		all: []aiSession{
			{ID: "s1", Cwd: "/proj/a", Tool: "claude", Title: "fix the parser"},
			{ID: "s2", Cwd: "/proj/a", Tool: "codex", Title: "add tests"},
			{ID: "s3", Cwd: "/proj/b", Tool: "gemini", Title: "docs pass"},
		},
		meta: map[string]sessMeta{},
	}
	m.applyFilter()
	return m
}

// TestManagerSplitKeyRequestsSetup: bare `|` in the manager asks for the guided split (and does
// not fall through to some other verb or a silent no-op).
func TestManagerSplitKeyRequestsSetup(t *testing.T) {
	m := paneMenu()
	if done, chosen := m.handleKey([]byte("|")); done || chosen != nil {
		t.Fatalf("`|` = (done=%v, chosen=%v), want it to neither quit nor resume", done, chosen)
	}
	if !m.splitSetup {
		t.Fatal("`|` did not request the guided split")
	}
	if m.spawnShell || m.filter || m.help || m.picking {
		t.Error("`|` triggered some other manager verb as well")
	}
}

// TestFirstPaintIsNotScrolled is the clampScroll regression: applyFilter clamps before the menu
// has a size (m.h == 0 ⇒ bodyH() == -5), which used to push m.top to 6 — the launcher's FIRST
// paint opened scrolled 6 rows down, blank whenever the tree had fewer than 7 rows, until a
// keypress re-clamped. Reproduces on the full-screen switchboard, not just in a pane.
func TestFirstPaintIsNotScrolled(t *testing.T) {
	m := paneMenu() // built + applyFilter()ed with no size, exactly as the real launcher is
	if m.top != 0 {
		t.Fatalf("m.top = %d before the first paint, want 0", m.top)
	}
	// And the first frame really shows the top of the tree (it used to open blank).
	expandAll(m)
	m.top, m.cursor = 0, 0
	m.applyFilter() // the launcher's own build order: filter with no size, then paint
	if m.top != 0 {
		t.Fatalf("m.top = %d after a size-less applyFilter, want 0", m.top)
	}
	// A cursor genuinely below the fold still scrolls once the height is known.
	m.h = 10 // bodyH() == 5
	m.cursor = 7
	m.clampScroll()
	if m.top != 3 {
		t.Errorf("m.top = %d with cursor 7 in a 5-row body, want 3", m.top)
	}
}

// expandAll opens every project in the tree so a SESSION row can be selected.
func expandAll(m *aiMenu) {
	for _, r := range append([]treeRow(nil), m.rows...) {
		if r.header() {
			m.setRowCollapsed(r, false)
		}
	}
	for i := range m.rows { // put the cursor on the first session row, not a header
		if !m.rows[i].header() {
			m.cursor = i
			break
		}
	}
}
