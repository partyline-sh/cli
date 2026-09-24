package main

import (
	"strings"
	"testing"

	"partyline.sh/partyline/internal/api"
)

// board_tile_test.go — a card is drawn as a card.
//
// The tile used to be three flat lines with the title CLIPPED, so a column of real tracker tasks
// read as a run-on list of half-sentences: "Confirm the pick-delta / charge-ceil…" tells you
// nothing about the card, and the names are where the information is.

func tileFor(title string, w int) []string {
	m := newBoardModel()
	return m.tileLines(api.BoardCard{ID: "1", Task: title, StateLabel: "New", Foreign: true}, w, false)
}

// The whole point: a long name is readable, not truncated to a fragment.
func TestTileWrapsTheTitleInsteadOfClipping(t *testing.T) {
	lines := tileFor("Confirm the pick-delta / charge-ceiling rule with Epic", 30)
	body := plain(strings.Join(lines, "\n"))
	for _, word := range []string{"Confirm", "charge-ceiling", "Epic"} {
		if !strings.Contains(body, word) {
			t.Errorf("the title lost %q — it was clipped, not wrapped:\n%s", word, body)
		}
	}
}

// A card is framed, so a column reads as a stack of cards.
func TestTileIsFramed(t *testing.T) {
	lines := tileFor("Short one", 30)
	first, last := plain(lines[0]), plain(lines[len(lines)-1])
	if !strings.HasPrefix(strings.TrimRight(first, " "), "╭") || !strings.HasSuffix(strings.TrimRight(first, " "), "╮") {
		t.Errorf("no top frame: %q", first)
	}
	if !strings.HasPrefix(strings.TrimRight(last, " "), "╰") || !strings.HasSuffix(strings.TrimRight(last, " "), "╯") {
		t.Errorf("no bottom frame: %q", last)
	}
	for _, l := range lines[1 : len(lines)-1] {
		p := strings.TrimRight(plain(l), " ")
		if !strings.HasPrefix(p, "│") || !strings.HasSuffix(p, "│") {
			t.Errorf("a body row is not inside the frame: %q", p)
		}
	}
}

// Every row is exactly the column's width, or the columns beside it shear.
func TestTileRowsAreExactlyColumnWidth(t *testing.T) {
	for _, w := range []int{18, 26, 40, 61} {
		for _, l := range tileFor("A reasonably long task name that must wrap several times over", w) {
			if got := visWidth(l); got != w {
				t.Errorf("width %d: a row measured %d columns:\n%q", w, got, plain(l))
			}
		}
	}
}

// One card must not be able to fill a column, and a title cut at the cap has to say so.
func TestTileCapsAVeryLongTitle(t *testing.T) {
	lines := tileFor(strings.Repeat("wordy ", 60), 26)
	// Frame, capped title, live line, state line — and nothing beyond that however long the name.
	if len(lines) > tileTitleMax+4 {
		t.Fatalf("a single card took %d lines", len(lines))
	}
	if !strings.Contains(plain(strings.Join(lines, "\n")), "…") {
		t.Error("a truncated title must be marked, or it reads as the whole name")
	}
}

// The scroll arithmetic has to MEASURE tiles now that they differ in height; a fixed guess would
// scroll the cursor off screen in any column holding a long name.
func TestTileHeightMatchesWhatIsDrawn(t *testing.T) {
	m := newBoardModel()
	for _, title := range []string{"Short", "A somewhat longer task name here", strings.Repeat("very ", 40)} {
		c := api.BoardCard{ID: "1", Task: title, StateLabel: "New", Foreign: true}
		if got, want := tileHeightOf(c, 28), len(m.tileLines(c, 28, false)); got != want {
			t.Errorf("tileHeightOf = %d but %d lines were drawn for %q", got, want, title)
		}
	}
}

// The selected card is obvious without a cursor — the frame changes and the top rail is marked.
func TestSelectedTileIsMarked(t *testing.T) {
	m := newBoardModel()
	c := api.BoardCard{ID: "1", Task: "Pick me", StateLabel: "New", Foreign: true}
	if !strings.Contains(plain(m.tileLines(c, 30, true)[0]), "▸") {
		t.Error("the selected tile's frame is not marked")
	}
	if strings.Contains(plain(m.tileLines(c, 30, false)[0]), "▸") {
		t.Error("an unselected tile must not be marked")
	}
}
