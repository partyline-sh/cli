package main

import (
	"os"
	"strings"
	"testing"

	"partyline.sh/partyline/internal/api"
)

func TestPreviewURLFindsDevServers(t *testing.T) {
	for _, tc := range []struct{ line, want string }{
		{"  ➜  Local:   http://localhost:3000/", "http://localhost:3000/"},
		{"Listening on http://127.0.0.1:8080", "http://127.0.0.1:8080"},
		{"server started at https://localhost:5173/app", "https://localhost:5173/app"},
		{"compiling packages", ""},
		{"see https://github.com/o/r/pull/3 for details", ""}, // a PR is not a preview
		{"", ""},
	} {
		c := card("x")
		c.LastLine = tc.line
		if got := previewURL(c); got != tc.want {
			t.Errorf("previewURL(%q) = %q, want %q", tc.line, got, tc.want)
		}
	}
}

// A remote host in a log line is NOT something to offer as a local preview: only loopback URLs are
// a dev server this operator can actually open.
func TestPreviewURLIgnoresRemoteHosts(t *testing.T) {
	c := card("x")
	c.LastLine = "deployed to https://staging.example.com:8443/"
	if got := previewURL(c); got != "" {
		t.Fatalf("previewURL offered a remote host: %q", got)
	}
}

func TestShortHost(t *testing.T) {
	for in, want := range map[string]string{
		"MacBook-Air.local": "MacBook-Air",
		"monolith":          "monolith",
		"box.example.com":   "box",
		"":                  "",
	} {
		if got := shortHost(in); got != want {
			t.Errorf("shortHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsThisMachineMatchesOnTheShortName(t *testing.T) {
	host, err := os.Hostname()
	if err != nil {
		t.Skip("no hostname on this box")
	}
	if !isThisMachine(host) {
		t.Errorf("isThisMachine(%q) should be true — that is us", host)
	}
	if !isThisMachine(shortHost(host)) {
		t.Error("the fleet label may omit the domain; the short name must still match")
	}
	if isThisMachine("definitely-not-this-box") {
		t.Error("a different machine must not match")
	}
	if isThisMachine("") {
		t.Error("an empty label is not this machine")
	}
}

// The three no-PR kinds exist because they need three different human actions. If the copy does not
// say what to DO, the distinction is decoration.
func TestNoPRExplanationSaysWhatToDo(t *testing.T) {
	for kind, want := range map[string]string{
		"branch-only": "Merge the branch yourself",
		"pr-failed":   "Open it from the run page",
		"no-changes":  "nothing to review",
	} {
		got := noPRExplanation(kind, "detail here")
		if !strings.Contains(got, want) {
			t.Errorf("%s explanation does not tell the reader what to do: %q", kind, got)
		}
		if !strings.Contains(got, "detail here") {
			t.Errorf("%s explanation dropped the server's own detail", kind)
		}
	}
}

// The bell fires on ARRIVAL into a column that wants a human, not on a card that was already there —
// otherwise every refresh would beep for the same blocked card forever.
func TestBellRingsOnlyOnArrival(t *testing.T) {
	before := &api.Board{
		Building: []api.BoardCard{card("a", withStatus("running"))},
		Blocked:  []api.BoardCard{card("old", withStatus("failed"))},
	}
	// Nothing moved: 'old' is still blocked, 'a' still building.
	if ring, _ := boardBell(before, before); ring {
		t.Error("an unchanged board must not ring")
	}

	after := &api.Board{
		Blocked: []api.BoardCard{card("old", withStatus("failed")), card("a", withStatus("failed"))},
	}
	ring, note := boardBell(before, after)
	if !ring {
		t.Fatal("a card arriving in Blocked must ring")
	}
	if !strings.Contains(note, "Blocked") || !strings.Contains(note, "task a") {
		t.Fatalf("note = %q, want it to name the column and the card", note)
	}
}

func TestBellCountsMultipleArrivals(t *testing.T) {
	before := &api.Board{Building: []api.BoardCard{card("a"), card("b"), card("c")}}
	after := &api.Board{Review: []api.BoardCard{card("a"), card("b"), card("c")}}
	ring, note := boardBell(before, after)
	if !ring {
		t.Fatal("three cards reaching Review must ring")
	}
	if !strings.Contains(note, "+2 more") {
		t.Fatalf("note = %q, want it to count the rest", note)
	}
}

// The first load has no previous board to compare against. Ringing then would beep on every launch
// for work that was already sitting there.
func TestBellSilentOnFirstLoad(t *testing.T) {
	if ring, _ := boardBell(nil, fullBoard()); ring {
		t.Error("the first board load must not ring")
	}
	if ring, _ := boardBell(fullBoard(), nil); ring {
		t.Error("a failed refresh must not ring")
	}
}

// Accepted is a good place for a card to end up and needs nobody, so it must not ring.
func TestBellIgnoresAcceptedColumn(t *testing.T) {
	before := &api.Board{Review: []api.BoardCard{card("a", withStatus("done"))}}
	after := &api.Board{Accepted: []api.BoardCard{card("a", withStatus("done"))}}
	if ring, note := boardBell(before, after); ring {
		t.Errorf("accepting work must not ring: %q", note)
	}
}
