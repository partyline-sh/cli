package main

import (
	"os"
	"testing"
	"time"

	"partyline.sh/partyline/internal/api"
)

// The tray must show exactly what `ptln join` (no link) would offer — one rule, two surfaces.
func TestInvitesFromSessionsMatchesTheJoinPickerFilter(t *testing.T) {
	rows := invitesFromSessions([]api.SessionInfo{
		{ID: "a", Status: "live", JoinLink: "https://x/j/a#k=1", JoinCode: "a-code", Host: "darcy"},
		{ID: "mine", Status: "live", JoinLink: "https://x/j/m#k=1", IsHost: true},    // I host it
		{ID: "planned", Status: "planned", JoinLink: "https://x/j/p#k=1"},            // not live yet
		{ID: "nolink", Status: "live", JoinLink: ""},                                 // nothing to join with
		{ID: "b", Status: "live", JoinLink: "https://x/j/b#k=1", JoinCode: "b-code"}, // no host name
	})
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 (a, b)", len(rows))
	}
	if rows[0].Label() != "darcy" {
		t.Errorf("label = %q, want the host's name", rows[0].Label())
	}
	// An older control plane sends no host — the row must still say something a human recognises.
	if rows[1].Label() != "b-code" {
		t.Errorf("label = %q, want the join code as the fallback", rows[1].Label())
	}
	if rows[0].Link == "" {
		t.Error("the join link is the one field that must survive — it carries the key")
	}
}

// A stale cache is treated as "we don't know", not as truth: a daemon that stopped an hour ago must
// not still be advertising sessions that have since closed.
func TestInviteCacheExpires(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()
	if err := writeInviteCache([]inviteRow{{ID: "a", Code: "c", Link: "l"}}, now); err != nil {
		t.Fatal(err)
	}
	if got := readInviteCache(now.Add(time.Second)); len(got) != 1 {
		t.Fatalf("fresh read = %d rows, want 1", len(got))
	}
	if got := readInviteCache(now.Add(inviteCacheTTL + time.Second)); got != nil {
		t.Errorf("stale read = %v, want nil", got)
	}
}

func TestInviteCacheIsQuietWhenMissingOrCorrupt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if got := readInviteCache(time.Now()); got != nil {
		t.Errorf("missing file = %v, want nil", got)
	}
	if err := os.MkdirAll(daemonDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inviteCachePath(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readInviteCache(time.Now()); got != nil {
		t.Errorf("corrupt file = %v, want nil", got)
	}
}

// Signed out is not an error state — there is simply nothing addressed to you.
func TestRefreshIsANoOpWhenSignedOut(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if got := refreshInviteCache(&api.Client{}, time.Now()); got != nil {
		t.Errorf("signed-out refresh = %v, want nil", got)
	}
	if _, err := os.Stat(inviteCachePath()); !os.IsNotExist(err) {
		t.Error("signed-out refresh must not create a cache file")
	}
}
