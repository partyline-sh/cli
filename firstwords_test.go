package main

import "testing"

func TestFirstWordsRound2(t *testing.T) {
	cases := []struct {
		name string
		s    string
		n    int
		want string
	}{
		{"empty string", "", 4, ""},
		{"fewer words than n", "one two", 4, "one two"},
		{"exactly n words", "a b c d", 4, "a b c d"},
		{"more words than n", "a b c d e f", 4, "a b c d"},
		{"collapses leading and duplicate spaces", "  lots   of   spaces here", 2, "lots of"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstWords(tc.s, tc.n); got != tc.want {
				t.Errorf("firstWords(%q, %d) = %q, want %q", tc.s, tc.n, got, tc.want)
			}
		})
	}
}
