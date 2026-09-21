//go:build darwin && tray

package main

import "testing"

func TestProjectLine(t *testing.T) {
	cases := []struct {
		in   trayProject
		want string
	}{
		{trayProject{Label: "partyline", AdoptedHere: true, Visibility: "team"}, "✓ partyline"},
		{trayProject{Label: "xero-receipts", Visibility: "team"}, "· xero-receipts"},
		{trayProject{Label: "scratch", Visibility: "private", Mine: true}, "· scratch — private"},
		// The display name wins when the project has one — same choice the web makes.
		{trayProject{Label: "acr-pos", DisplayName: "POS Server", AdoptedHere: true, Visibility: "team"}, "✓ POS Server"},
	}
	for _, c := range cases {
		if got := projectLine(c.in); got != c.want {
			t.Errorf("projectLine(%+v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestProjectWatchSeedsSilentlyThenAnnouncesOnce(t *testing.T) {
	w := newProjectWatch()
	first := []trayProject{{ID: "a", Label: "partyline"}}
	if got := w.notices(true, first); len(got) != 0 {
		t.Fatalf("first poll must seed silently, got %v", got)
	}
	// The same list persisting must not re-announce.
	if got := w.notices(true, first); len(got) != 0 {
		t.Fatalf("a persisting project must not re-announce, got %v", got)
	}
	// A NEW project announces exactly once, and says it is ready when adopted here.
	second := append(first, trayProject{ID: "b", Label: "new-thing", AdoptedHere: true})
	got := w.notices(true, second)
	if len(got) != 1 || got[0] != "New project: new-thing — ready on this machine" {
		t.Fatalf("expected one adoption banner, got %v", got)
	}
	if got := w.notices(true, second); len(got) != 0 {
		t.Fatalf("announced twice: %v", got)
	}
}

func TestProjectWatchIgnoresFailedReads(t *testing.T) {
	w := newProjectWatch()
	w.notices(true, []trayProject{{ID: "a", Label: "one"}})
	// A failed poll (CLI briefly missing) carries no rows; the seen set must survive it.
	if got := w.notices(false, nil); len(got) != 0 {
		t.Fatalf("failed read produced notices: %v", got)
	}
	if got := w.notices(true, []trayProject{{ID: "a", Label: "one"}}); len(got) != 0 {
		t.Fatalf("re-announced after a failed read: %v", got)
	}
}
