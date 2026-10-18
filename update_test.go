package main

import "testing"

func TestBrewManaged(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		// curl-installer targets — NOT brew (this was the bug on Apple Silicon).
		{"/opt/homebrew/bin/partyline", false},
		{"/usr/local/bin/partyline", false},
		{"/home/me/.local/bin/partyline", false},
		{"/home/linuxbrew/.linuxbrew/bin/partyline", false},
		// real Homebrew kegs — brew.
		{"/opt/homebrew/Cellar/partyline/0.1.50/bin/partyline", true},
		{"/usr/local/Cellar/partyline/0.1.50/bin/partyline", true},
		{"/home/linuxbrew/.linuxbrew/Cellar/partyline/0.1.50/bin/partyline", true},
		// casks — what partyline actually ships. A cask lives in Caskroom, not Cellar.
		{"/opt/homebrew/Caskroom/partyline/0.1.50/partyline", true},
		{"/usr/local/Caskroom/partyline/0.1.50/partyline", true},
	}
	for _, c := range cases {
		if got := brewManaged(c.path); got != c.want {
			t.Errorf("brewManaged(%q)=%v want %v", c.path, got, c.want)
		}
	}
}

func TestVersionLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.1.40", "0.1.42", true},
		{"0.1.42", "0.1.40", false},
		{"0.1.40", "0.1.40", false},
		{"0.1.9", "0.1.10", true},   // numeric, not lexical
		{"v0.1.9", "v0.1.10", true}, // leading v tolerated
		{"0.2.0", "0.1.99", false},
		{"1.0.0", "0.9.9", false},
		{"dev", "0.1.40", true}, // dev sorts oldest
	}
	for _, c := range cases {
		if got := versionLess(c.a, c.b); got != c.want {
			t.Errorf("versionLess(%q,%q)=%v want %v", c.a, c.b, got, c.want)
		}
	}
}
