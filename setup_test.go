package main

import "testing"

// The post-login offer rule (#149): ask exactly once, only where an answer is possible, and
// never at someone who's already connected. Tested as a rule because the file-stat + TTY
// plumbing around it can't run headlessly — this is the part that must not drift.
func TestShouldOfferSetup(t *testing.T) {
	cases := []struct {
		name                                      string
		interactive, connected, askedBefore, want bool
	}{
		{"fresh interactive login with gaps", true, false, false, true},
		{"already a worker — nothing to offer", true, true, false, false},
		{"asked before — once means once", true, false, true, false},
		{"piped/CI — a prompt nobody can answer is a hang", false, false, false, false},
		{"connected AND asked", true, true, true, false},
	}
	for _, c := range cases {
		if got := shouldOfferSetup(c.interactive, c.connected, c.askedBefore); got != c.want {
			t.Errorf("%s: shouldOfferSetup(%v,%v,%v) = %v, want %v",
				c.name, c.interactive, c.connected, c.askedBefore, got, c.want)
		}
	}
}
