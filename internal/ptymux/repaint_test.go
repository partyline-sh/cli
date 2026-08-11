package ptymux

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"
)

// The tab-switch repaint has to satisfy two things at once, and the shipped ordering could only
// ever satisfy one: the child's screen lands at row 1 (so it doesn't float onto the bar row), AND
// the cursor lands exactly where the child left it (so the composer isn't drawn a row off).
//
// This is the bug reported against v0.55.0: the block cursor sitting one row above the `>` prompt
// after a tab switch, self-correcting only when a busy agent happened to repaint. An idle agent
// never repaints, so the wrong state just sits there.
//
// The snapshot is synthesized rather than taken from a real ptysess.Session because the only
// property that matters here is its SHAPE: SnapshotHistory pads the screen block to the FULL
// terminal height, so a repaint writes termRows lines. That is exactly what a bodyRows-tall
// scroll region cannot hold without scrolling.
func TestComposeRepaintLandsScreenAtTopAndCursorWhereTold(t *testing.T) {
	const cols, termRows = 40, 10
	const bodyRows = termRows - 1 // the mux reserves the bottom row for its status bar

	// A full-height snapshot: wipe, home, then termRows lines. Mirrors paintBody's main-screen path.
	var snap strings.Builder
	snap.WriteString("\x1b[?1049l\x1b[3J\x1b[2J\x1b[H")
	for i := 0; i < termRows; i++ {
		if i > 0 {
			snap.WriteString("\r\n")
		}
		fmt.Fprintf(&snap, "line %d", i)
	}

	// Where the child's composer actually is: last body row, a few columns in.
	const wantRow, wantCol = bodyRows, 3

	term := vt.NewSafeEmulator(cols, termRows)
	if _, err := term.Write(composeRepaint([]byte(snap.String()), nil, bodyRows, wantCol, wantRow)); err != nil {
		t.Fatalf("write repaint: %v", err)
	}

	got := strings.Split(term.Render(), "\n")
	if len(got) < termRows {
		t.Fatalf("rendered %d rows, want %d", len(got), termRows)
	}

	// The screen must start at row 1. If the region scrolled it, row 1 holds "line 1" (or later)
	// and everything below is shifted — which is what puts the cursor a row off.
	if want := "line 0"; !strings.HasPrefix(strings.TrimRight(got[0], " "), want) {
		t.Errorf("screen did not land at row 1: row 1 = %q, want it to start with %q\n(the full-height snapshot scrolled inside the %d-row region)",
			strings.TrimRight(got[0], " "), want, bodyRows)
	}

	// And the cursor must land on the CONTENT the child left it on — not merely at the absolute
	// screen cell we asked for. Both orderings put the cursor at the same cell; what differs is
	// whether the content underneath it scrolled out from under it. That relationship is the
	// reported symptom: a block cursor sitting one row above the `>` prompt it belongs to.
	pos := term.CursorPosition() // 0-indexed; the escape we emitted is 1-indexed
	if pos.Y < 0 || pos.Y >= len(got) {
		t.Fatalf("cursor row %d outside the rendered screen (%d rows)", pos.Y, len(got))
	}
	// Row `wantRow` (1-indexed) holds "line wantRow-1" once the screen lands at row 1.
	wantLine := fmt.Sprintf("line %d", wantRow-1)
	if onRow := strings.TrimRight(got[pos.Y], " "); !strings.HasPrefix(onRow, wantLine) {
		t.Errorf("cursor is on the wrong content row: it sits on %q, want %q\n(the screen moved out from under the cursor — this is the composer drawn a row off)",
			onRow, wantLine)
	}
	if pos.X != wantCol-1 {
		t.Errorf("cursor column off: want %d (0-indexed %d), got %d", wantCol, wantCol-1, pos.X)
	}
}

// The bar row must still be pinned after a repaint — that is the whole reason the region is
// emitted at all. A newline at the bottom of the body must not scroll the reserved row away.
func TestComposeRepaintStillPinsTheBarRow(t *testing.T) {
	const bodyRows = 9
	out := string(composeRepaint([]byte("hi"), nil, bodyRows, 1, 1))
	region := string(scrollRegionFor(bodyRows))
	if !strings.Contains(out, region) {
		t.Fatalf("repaint did not assert the body scroll region %q:\n %q", region, out)
	}
	// The cursor reposition has to come AFTER the region, or DECSTBM's homing wins.
	if strings.Index(out, region) > strings.Index(out, "\x1b[1;1H") {
		t.Error("scroll region emitted after the cursor reposition — DECSTBM homes the cursor, so the position is lost")
	}
}

// Without a real cursor position there is nothing to restore, so the region must be withheld too —
// asserting it alone would home the cursor and reintroduce #238.
func TestComposeRepaintWithholdsRegionWithoutACursor(t *testing.T) {
	for _, c := range []struct{ col, row int }{{0, 5}, {5, 0}, {0, 0}} {
		out := string(composeRepaint([]byte("hi"), nil, 9, c.col, c.row))
		if out != "hi" {
			t.Errorf("col=%d row=%d: want the snapshot untouched, got %q", c.col, c.row, out)
		}
	}
}
