package main

import "testing"

func TestCgVisLen(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"hello", 5},
		{"\x1b[1mhello\x1b[0m", 5},            // SGR escapes don't count
		{cgKey + "r" + cgOff + "  record", 9}, // "r" + two spaces + "record"
		{"⎇ main", 6},                         // ⎇ is width 1, space, "main"
		{"\x1b[38;5;39m●\x1b[0m ok", 4},       // ● width 1 + space + "ok"
	}
	for _, c := range cases {
		if got := cgVisLen(c.in); got != c.want {
			t.Errorf("cgVisLen(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestCgClip(t *testing.T) {
	// Short line is returned untouched.
	if got := cgClip("abc", 10); got != "abc" {
		t.Errorf("cgClip short = %q, want %q", got, "abc")
	}
	// Over-long line is clipped to max visible cols (ellipsis included) and reset.
	got := cgClip("abcdefgh", 4)
	if cgVisLen(got) != 4 {
		t.Errorf("cgClip visible width = %d, want 4 (got %q)", cgVisLen(got), got)
	}
	// ANSI escapes survive clipping and color is reset at the end.
	styled := cgKey + "abcdefghij" + cgOff
	clipped := cgClip(styled, 5)
	if cgVisLen(clipped) != 5 {
		t.Errorf("cgClip styled visible width = %d, want 5 (got %q)", cgVisLen(clipped), clipped)
	}
	if clipped[len(clipped)-len("\x1b[0m"):] != "\x1b[0m" {
		t.Errorf("cgClip styled should end with reset, got %q", clipped)
	}
}

func TestCgFit(t *testing.T) {
	out := cgFit([]string{"ok", "waytoolongforthis"}, 6)
	if len(out) != 2 {
		t.Fatalf("cgFit len = %d, want 2", len(out))
	}
	if out[0] != "ok" {
		t.Errorf("cgFit[0] = %q, want unchanged %q", out[0], "ok")
	}
	if cgVisLen(out[1]) > 6 {
		t.Errorf("cgFit[1] visible width = %d, want <= 6", cgVisLen(out[1]))
	}
}
