//go:build darwin && tray

package main

import "testing"

func TestTrayTitle(t *testing.T) {
	cases := []struct{ version, want string }{
		{"0.8.1", "Partyline v0.8.1"},
		// A dev build keeps its word: "vdev" would read as a release that does not exist.
		{"dev", "Partyline dev"},
		// An older `ptln` sends no version. Degrade to exactly the old header rather than an empty "v".
		{"", "Partyline"},
	}
	for _, c := range cases {
		if got := trayTitle(c.version); got != c.want {
			t.Errorf("trayTitle(%q) = %q, want %q", c.version, got, c.want)
		}
	}
}
