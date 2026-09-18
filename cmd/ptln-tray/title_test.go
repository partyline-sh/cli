//go:build darwin && tray

package main

import "testing"

func TestTrayTitle(t *testing.T) {
	cases := []struct{ version, env, want string }{
		// Production is unlabelled — that is what an unadorned tray has always meant, and adding
		// "production" everywhere would make the common case noisier to buy nothing.
		{"0.8.1", "", "Partyline v0.8.1"},
		{"0.8.1", "staging", "Partyline v0.8.1 · staging"},
		{"0.8.1", "localhost:3111", "Partyline v0.8.1 · localhost:3111"},
		// A dev build keeps its word: "vdev" would read as a release that does not exist.
		{"dev", "staging", "Partyline dev · staging"},
		{"dev", "", "Partyline dev"},
		// An older `ptln` sends neither field. Degrade to exactly the old header rather than
		// rendering a stray separator or an empty "v".
		{"", "", "Partyline"},
		{"", "staging", "Partyline · staging"},
	}
	for _, c := range cases {
		if got := trayTitle(c.version, c.env); got != c.want {
			t.Errorf("trayTitle(%q, %q) = %q, want %q", c.version, c.env, got, c.want)
		}
	}
}
