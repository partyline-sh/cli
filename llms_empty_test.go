package main

import (
	"strings"
	"testing"

	ansix "github.com/charmbracelet/x/ansi"

	"partyline.sh/partyline/internal/brand"
)

// This modal is TUI output nobody can eyeball in CI, so the tests pin the two things that can be
// wrong without anyone noticing: what it OFFERS versus what it HANDLES, and whether it fits.
//
// The first version built the row list and the key switch as separate code, which makes "shows 3,
// pressing 3 does nothing" permanently one edit away. Both now derive from emptyChoices.

func TestEveryRowDrawnIsARowYouCanPress(t *testing.T) {
	for _, found := range [][]string{
		nil,
		{"/Users/acr"},
		{"/Users/acr", "/Users/build", "/Users/old"},
	} {
		choices := emptyChoices(found)
		model := emptyStateForm(found)
		lines := strings.Split(model.View().Content, "\n")

		// Every key in the rendered body must belong to a choice, and every choice must appear.
		body := strings.Join(lines, "\n")
		for _, c := range choices {
			if !strings.Contains(body, string(c.Key)) {
				t.Errorf("found=%v: choice %q is handled but never drawn", found, c.Key)
			}
			if c.Label == "" {
				t.Errorf("found=%v: choice %q has no label", found, c.Key)
			}
		}
		// And the reverse: a drawn row with no choice behind it is a dead key.
		rows := 0
		for _, line := range lines {
			for _, c := range choices {
				if strings.HasPrefix(ansix.Strip(line), string(c.Key)+"  ") {
					rows++
				}
			}
		}
		if rows != len(choices) {
			t.Errorf("found=%v: drew %d action rows for %d choices", found, rows, len(choices))
		}
	}
}

// Keys are 1-9. A tenth detected root would be drawn with no way to select it — a menu item you can
// see and cannot press is worse than one that was never offered.
func TestMoreThanNineRootsAreCappedNotDrawnUnpressable(t *testing.T) {
	many := make([]string, 12)
	for i := range many {
		many[i] = "/Users/h" + string(rune('a'+i))
	}
	choices := emptyChoices(many)

	roots := 0
	seen := map[rune]bool{}
	for _, c := range choices {
		if seen[c.Key] {
			t.Fatalf("duplicate key %q — the later row would be unreachable", c.Key)
		}
		seen[c.Key] = true
		if c.Root != "" {
			roots++
			if c.Key < '1' || c.Key > '9' {
				t.Errorf("root row key %q is outside 1-9", c.Key)
			}
		}
	}
	if roots != 9 {
		t.Errorf("offered %d roots, want the 9 that are actually pressable", roots)
	}
}

// q must always be present and must always mean "do nothing" — it is the escape hatch, and a modal
// with no visible way out is the one that makes people kill the terminal.
func TestQuitIsAlwaysOffered(t *testing.T) {
	for _, found := range [][]string{nil, {"/Users/acr"}} {
		var hasQ bool
		for _, c := range emptyChoices(found) {
			if c.Key == 'q' {
				hasQ = true
				if c.Root != "" {
					t.Error("q must not adopt anything")
				}
			}
		}
		if !hasQ {
			t.Errorf("found=%v: no way out of the modal", found)
		}
	}
}

// The frame clips at cols-4, so a long path would be silently truncated rather than breaking the
// box — but a row whose VISIBLE width is already absurd means the modal renders as a wall. Checking
// the visible width (not the byte length) is the point: these lines are full of escape codes.
func TestRowsStayWithinASensibleWidth(t *testing.T) {
	long := "/Users/a-very-long-service-account-name-from-some-migration/nested/deeper"
	for _, l := range strings.Split(emptyStateForm([]string{long}).View().Content, "\n") {
		if w := brand.VisWidth(l); w > 76 {
			t.Errorf("line is %d visible cols, wider than a comfortable modal: %q", w, l)
		}
	}
}

// The prose must not silently become empty if HOME is unresolvable — an empty line in the middle of
// the explanation reads as a rendering bug.
func TestTheExplanationAlwaysSaysSomething(t *testing.T) {
	lines := strings.Split(emptyStateForm(nil).View().Content, "\n")
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"No AI sessions found", "different HOME"} {
		if !strings.Contains(joined, want) {
			t.Errorf("modal body is missing %q", want)
		}
	}
}
