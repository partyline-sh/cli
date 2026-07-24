package ptymux

import "testing"

// visLen must measure terminal display columns (not bytes), ignoring ANSI — otherwise the
// centered picker box frays/jumps. The selected-marker case is the key regression: "▸ "
// must equal "  " in width so the box doesn't resize as the selection moves.
func TestVisLen(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"claude", 6},
		{"⏳", 2},                // wide emoji = 2 cells
		{"●", 1},                // ambiguous shape = 1
		{"·", 1},                // middle dot = 1
		{"▸ ", 2},               // selected marker
		{"  ", 2},               // unselected marker — MUST match "▸ "
		{"\x1b[1m▸ \x1b[0m", 2}, // ANSI stripped, marker still 2
		{"\x1b[38;5;245mwaiting\x1b[0m", 7},
		{"1 ⏳ claude", 1 + 1 + 2 + 1 + 6}, // "1"+" "+"⏳"+" "+"claude"
	}
	for _, c := range cases {
		if got := visLen(c.in); got != c.want {
			t.Errorf("visLen(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}
