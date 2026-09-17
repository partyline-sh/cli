package main

import (
	"strings"
	"testing"
)

// The status column only works as a column if every cell is exactly menuStatusWidth of VISIBLE
// text — a ragged cell shears the whole wall.
func TestMenuStatusCellWidth(t *testing.T) {
	for _, status := range []string{"waiting", "active", "", "garbage"} {
		for _, selected := range []bool{true, false} {
			cell := menuStatusCell(status, selected)
			if got := len([]rune(stripANSI(cell))); got != menuStatusWidth {
				t.Errorf("menuStatusCell(%q, %v): visible width %d, want %d", status, selected, got, menuStatusWidth)
			}
		}
	}
	// The selected row is drawn on the pill and must carry no colour codes of its own.
	if c := menuStatusCell("waiting", true); strings.Contains(c, "\x1b[") {
		t.Errorf("selected status cell carries ANSI codes — it would fight the pill: %q", c)
	}
}

func TestMenuSummary(t *testing.T) {
	cases := []struct {
		items []tmuxMenuItem
		want  string
	}{
		{[]tmuxMenuItem{{label: "new session", key: "n"}}, ""}, // no live agents → no banner
		{[]tmuxMenuItem{{status: "active"}, {status: "active"}}, "2 agents working · none waiting on you"},
		{[]tmuxMenuItem{{status: "active"}, {status: "waiting"}, {status: "waiting"}, {label: "cmd"}}, "3 agents live · 2 waiting on you"},
	}
	for _, c := range cases {
		if got := menuSummary(c.items); got != c.want {
			t.Errorf("menuSummary = %q, want %q", got, c.want)
		}
	}
}

// stripANSI removes escape sequences so width assertions measure what a human sees.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\x1b' {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
