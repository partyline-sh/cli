package ptymux

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/vt"
	"partyline.sh/partyline/internal/brand"
)

// plainRow strips ANSI SGR from a rendered row so we can assert on absolute COLUMNS.
func plainRow(s string) []rune {
	var out []rune
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			if i < len(s) {
				i++
			}
			continue
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		out = append(out, r)
		i += n
	}
	return out
}

func bodyLines(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s%d", prefix, i)
	}
	return out
}

// TestSplitFrameGeometry is the split analogue of ptysess' TestSnapshotHistoryLandsAtTop: it
// renders a two-pane frame into a fake full-height terminal and pins the geometry contract —
// left pane at column 1, right pane at the divider+1 offset, the status-bar row untouched, and
// nothing painted below row (rows-1).
func TestSplitFrameGeometry(t *testing.T) {
	const cols, rows = 41, 10
	leftW, rightW, bodyRows, ok := splitGeom(cols, rows)
	if !ok {
		t.Fatalf("splitGeom(%d,%d) not ok", cols, rows)
	}
	if leftW != 20 || rightW != 20 || bodyRows != rows-2 {
		t.Fatalf("geom = (%d,%d,%d), want (20,20,%d)", leftW, rightW, bodyRows, rows-2)
	}

	l := paneView{title: "left", lines: bodyLines("L", bodyRows), focused: true, curRow: 2, curCol: 3}
	r := paneView{title: "right", lines: bodyLines("R", bodyRows)}

	term := vt.NewSafeEmulator(cols, rows)
	if _, err := term.Write(splitFrame(l, r, leftW, rightW, bodyRows, rows)); err != nil {
		t.Fatal(err)
	}
	grid := strings.Split(term.Render(), "\n")
	if len(grid) < rows {
		t.Fatalf("terminal rendered %d rows, want %d", len(grid), rows)
	}

	for i := 0; i < bodyRows; i++ {
		row := plainRow(grid[splitTitleRow+i]) // grid is 0-based; body starts at index 1
		// (a) the left pane's content starts at column 1.
		want := fmt.Sprintf("L%d", i)
		if got := string(row[:len(want)]); got != want {
			t.Errorf("body row %d: left content starts %q, want %q", i, got, want)
		}
		// The divider occupies exactly the column after the left pane.
		if row[leftW] != '│' {
			t.Errorf("body row %d: col %d = %q, want the divider", i, leftW+1, row[leftW])
		}
		// (b) the right pane's content starts at the expected offset column (leftW+2, 1-based).
		want = fmt.Sprintf("R%d", i)
		if got := string(row[leftW+1 : leftW+1+len(want)]); got != want {
			t.Errorf("body row %d: right content at col %d is %q, want %q", i, leftW+2, got, want)
		}
	}

	// (c) the bottom bar row is not overwritten by either pane, and (d) nothing paints below
	// row (rows-1) — the bar row is the LAST row, so both reduce to "row `rows` is blank".
	if got := strings.TrimSpace(string(plainRow(grid[rows-1]))); got != "" {
		t.Errorf("status-bar row (row %d) was painted by a pane: %q", rows, got)
	}

	// The focused pane owns the real cursor, placed inside its own body at an absolute position.
	pos := term.CursorPosition()
	if pos.X != 3 || pos.Y != splitTitleRow+2 {
		t.Errorf("cursor at (col=%d,row=%d), want (col=3,row=%d)", pos.X, pos.Y, splitTitleRow+2)
	}
	// The focus indicator is on the focused title only.
	title := string(plainRow(grid[0]))
	if !strings.HasPrefix(title, "▸ left") {
		t.Errorf("title row = %q, want the focused marker on the left pane", title)
	}
	// The emulator re-orders/merges SGR params, so match the bg parameter itself, not the sequence.
	bg := strings.TrimSuffix(strings.TrimPrefix(brand.PillBg(), "\x1b["), "m")
	if !strings.Contains(grid[0], bg) {
		t.Errorf("title row lacks the brand-pink focus background: %q", grid[0])
	}
}

// A zoomed pane spans the full width with no divider, and still never touches the bar row.
func TestSplitFrameZoomedSpansFullWidth(t *testing.T) {
	const cols, rows = 41, 10
	_, _, bodyRows, _ := splitGeom(cols, rows)
	l := paneView{title: "zoomed", lines: []string{strings.Repeat("Z", cols+20)}, focused: true}

	term := vt.NewSafeEmulator(cols, rows)
	_, _ = term.Write(splitFrame(l, paneView{}, cols, 0, bodyRows, rows))
	grid := strings.Split(term.Render(), "\n")

	row := plainRow(grid[splitTitleRow])
	if len(row) != cols {
		t.Fatalf("zoomed body row has %d cells, want %d", len(row), cols)
	}
	if strings.ContainsRune(string(row), '│') {
		t.Errorf("zoomed frame drew a divider: %q", string(row))
	}
	if got := string(row); got != strings.Repeat("Z", cols) {
		t.Errorf("zoomed row = %q, want the pane clipped to exactly %d columns", got, cols)
	}
	if got := strings.TrimSpace(string(plainRow(grid[rows-1]))); got != "" {
		t.Errorf("zoomed frame painted the status-bar row: %q", got)
	}
}

// Panes must never be handed a geometry that would collide with the bar row, and a terminal too
// small for two panes + the bar must refuse to split at all.
func TestSplitGeomRejectsTinyTerminals(t *testing.T) {
	for _, tc := range []struct{ cols, rows int }{{7, 24}, {80, 3}, {0, 0}, {4, 4}} {
		if _, _, _, ok := splitGeom(tc.cols, tc.rows); ok {
			t.Errorf("splitGeom(%d,%d) allowed a split", tc.cols, tc.rows)
		}
	}
	// Widths always account for the divider column, and the body always leaves the bar row free.
	for _, tc := range []struct{ cols, rows int }{{8, 4}, {41, 10}, {80, 24}, {201, 50}} {
		lw, rw, body, ok := splitGeom(tc.cols, tc.rows)
		if !ok {
			t.Fatalf("splitGeom(%d,%d) refused a workable terminal", tc.cols, tc.rows)
		}
		if lw+rw+1 != tc.cols {
			t.Errorf("%dx%d: widths %d+%d+divider != %d", tc.cols, tc.rows, lw, rw, tc.cols)
		}
		if body != tc.rows-2 {
			t.Errorf("%dx%d: body %d rows, want %d (title + bar reserved)", tc.cols, tc.rows, body, tc.rows-2)
		}
	}
}

func TestClipPadANSIExactColumns(t *testing.T) {
	if got := brand.VisWidth(clipPadANSI("ab", 5)); got != 5 {
		t.Errorf("padded width = %d, want 5", got)
	}
	if got := brand.VisWidth(clipPadANSI("abcdefgh", 4)); got != 4 {
		t.Errorf("clipped width = %d, want 4", got)
	}
	// SGR is free; the visible payload is still clipped to the budget.
	if got := brand.VisWidth(clipPadANSI("\x1b[31mabcdef\x1b[0m", 3)); got != 3 {
		t.Errorf("ANSI-laden clip width = %d, want 3", got)
	}
	// A wide glyph that would straddle the edge is dropped, not half-drawn.
	if got := brand.VisWidth(clipPadANSI("a⏳", 2)); got != 2 {
		t.Errorf("wide-glyph clip width = %d, want 2", got)
	}
	if clipPadANSI("x", 0) != "" {
		t.Error("zero width should render nothing")
	}
}

// TestSplitFramePlacesHomeLines is the in-pane manager contract at the painter level: a pane
// whose body came from PaneHome.RenderLines (SGR-only rows) lands exactly inside its own half,
// the divider survives, and the bar row stays untouched. It also pins WHY RenderLines must not
// emit erase escapes (see the closing assertion).
func TestSplitFramePlacesHomeLines(t *testing.T) {
	const cols, rows = 41, 10
	leftW, rightW, bodyRows, _ := splitGeom(cols, rows)

	home := make([]string, bodyRows)
	for i := range home {
		home[i] = "\x1b[38;5;111m╭─┤ sessions ├─╮\x1b[0m" // an SGR-only manager row
	}
	l := paneView{title: "⌂ pick a session", lines: home, focused: true, modes: []byte("\x1b[?25l")}
	r := paneView{title: "right", lines: bodyLines("R", bodyRows)}

	term := vt.NewSafeEmulator(cols, rows)
	if _, err := term.Write(splitFrame(l, r, leftW, rightW, bodyRows, rows)); err != nil {
		t.Fatal(err)
	}
	grid := strings.Split(term.Render(), "\n")
	row := plainRow(grid[splitTitleRow]) // first body row (the emulator trims trailing blanks)
	if len(row) < leftW+2 {
		t.Fatalf("body row has %d cells, want at least %d", len(row), leftW+2)
	}
	if got, want := string(row[:leftW]), "╭─┤ sessions ├─╮"; !strings.HasPrefix(got, want) {
		t.Errorf("left pane = %q, want it to start with the manager row %q", got, want)
	}
	if row[leftW] != '│' {
		t.Errorf("divider column holds %q, want │", string(row[leftW]))
	}
	if got := string(row[leftW+1:]); !strings.HasPrefix(got, "R0") {
		t.Errorf("right pane = %q, want the other pane's own row", got)
	}
	if got := strings.TrimSpace(string(plainRow(grid[rows-1]))); got != "" {
		t.Errorf("frame painted the status-bar row: %q", got)
	}

	// And the hazard the contract exists for: clipPadANSI copies an escape only up to 'm', so a
	// \x1b[K inside a pane row reads as an open-ended escape — its trailing text is emitted but
	// counted as zero width, and the row bleeds past the pane. Hence "SGR only" in RenderLines.
	if bleed := clipPadANSI("abc\x1b[Kdef", 8); !strings.Contains(bleed, "\x1b[Kdef") {
		t.Errorf("clipPadANSI now parses erase escapes (%q) — RenderLines' no-erase rule can relax", bleed)
	}
}
