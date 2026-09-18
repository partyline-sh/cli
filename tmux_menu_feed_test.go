package main

import "testing"

// The activity feed is a PANE, and the session list is one row per pane — so it appeared as a
// second row for whatever window it was opened beside, sharing that window's name and number.
// Two rows read "3 FLEET MANAGER", one of which could not be previewed, closed or merged as a
// session, and "close the highlighted session" pointed at the wrong thing half the time.
//
// This tests the parse directly, because the menu itself needs a live tmux server.
func TestTheFeedPaneIsNotListedAsASession(t *testing.T) {
	// pane_id, window_index, window_name, active, window_panes, spec, feed
	rows := []string{
		"%1\t3\tFLEET MANAGER\t11\t2\t\t",
		"%2\t3\tFLEET MANAGER\t10\t2\t\t1", // the feed, opened beside it
		"%3\t4\tHOOPS\t00\t1\t\t",
	}
	kept, panesPerWindow := parseSessionPanes(rows)
	if len(kept) != 2 {
		t.Fatalf("kept %d rows, want 2 — the feed must not be listed", len(kept))
	}
	for _, r := range kept {
		if r.feed {
			t.Fatal("a feed pane survived the filter")
		}
	}
	// And a window holding one agent plus a feed is still ONE agent: the count decides whether
	// rows are renamed for a shared window.
	if got := panesPerWindow["3"]; got != 1 {
		t.Errorf("window 3 counted %d session panes, want 1 (the feed must not count)", got)
	}
	if got := panesPerWindow["4"]; got != 1 {
		t.Errorf("window 4 counted %d, want 1", got)
	}
}
