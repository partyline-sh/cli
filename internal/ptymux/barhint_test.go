package ptymux

import (
	"strings"
	"testing"

	"partyline.sh/partyline/internal/brand"
)

// barModes covers every branch of barHint, in dispatcher-precedence order.
var barModes = []struct {
	name                         string
	selecting, pfx, setup, split bool
	pill                         string
}{
	{name: "live", pill: "LIVE"},
	{name: "split", split: true, pill: "SPLIT"},
	{name: "setup", setup: true, pill: "SETUP"},
	{name: "chord", pfx: true, pill: "CHORD"},
	{name: "select", selecting: true, pill: "SELECT"},
}

// THE INVARIANT: the status bar is exactly one row. The child screens are sized rows-1 and
// snapshot replay lands at row 1 on that assumption (#238) — a bar that wrapped to a second row
// would mis-size every child and put replay one line off, which is precisely the regression that
// was landed, reverted and re-solved. So: no newline, and never wider than the terminal.
func TestBarStaysOneRow(t *testing.T) {
	for _, colorterm := range []string{"", "truecolor"} {
		t.Setenv("COLORTERM", colorterm)
		for _, m := range barModes {
			for _, cols := range []int{1, 2, 5, 8, 12, 20, 24, 40, 60, 80, 100, 132, 200, 400} {
				// Simulate drawBar's own budgeting: some tabs already occupy part of the row.
				for _, used := range []int{0, cols / 3, cols - 4} {
					if used < 0 {
						used = 0
					}
					hint := barHint(m.selecting, m.pfx, m.setup, m.split, cols-used-2)
					if strings.ContainsAny(hint, "\r\n") {
						t.Fatalf("%s/%s cols=%d: hint contains a line break: %q", colorterm, m.name, cols, hint)
					}
					row := strings.Repeat("x", used) + "  " + hint
					clipped := clipANSI(row, cols)
					if strings.ContainsAny(clipped, "\r\n") {
						t.Fatalf("%s/%s cols=%d: bar row has a line break: %q", colorterm, m.name, cols, clipped)
					}
					// clipANSI budgets BYTES, so the column count can only ever be <= cols.
					// That is what keeps the row from wrapping onto a second line.
					if w := brand.VisWidth(clipped); w > cols {
						t.Fatalf("%s/%s cols=%d: bar is %d columns wide: %q", colorterm, m.name, cols, w, clipped)
					}
				}
			}
		}
	}
}

// Every mode is BADGED, and the badge is what survives a narrow row.
func TestBarHintAlwaysCarriesItsPill(t *testing.T) {
	for _, m := range barModes {
		for w := 1; w <= 120; w++ {
			got := barHint(m.selecting, m.pfx, m.setup, m.split, w)
			if !strings.Contains(got, m.pill) {
				t.Fatalf("%s at width %d lost its pill: %q", m.name, w, got)
			}
		}
	}
	// Distinct modes must be distinguishable — LIVE and SPLIT rendered identically before.
	seen := map[string]string{}
	for _, m := range barModes {
		got := barHint(m.selecting, m.pfx, m.setup, m.split, 0)
		if prev, dup := seen[got]; dup {
			t.Errorf("%s and %s render the same bar: %q", prev, m.name, got)
		}
		seen[got] = m.name
	}
}

// The keys named in SPLIT mode are exactly the ones the dispatcher gates on splitActive().
func TestBarHintSplitNamesItsKeys(t *testing.T) {
	got := barHint(false, false, false, true, 0)
	for _, key := range []string{"tab", "z", "x"} {
		if !strings.Contains(got, key) {
			t.Errorf("SPLIT bar omits %q, which only works in a split: %q", key, got)
		}
	}
	if strings.Contains(barHint(false, false, false, false, 0), "focus pane") {
		t.Error("LIVE bar advertises a pane key that does nothing outside a split")
	}
}
