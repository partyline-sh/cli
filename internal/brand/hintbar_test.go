package brand

import (
	"strings"
	"testing"
)

var testHints = []Hint{
	{"↵", "open"}, {"|", "open in split"}, {"o", "new tab"},
	{"S", "share"}, {"d", "diff"}, {"/", "search"}, {"?", "all keys"},
}

// The pill is the part that says WHERE YOU ARE; hints are the part you can afford to lose.
// So truncation must come off the right and never off the pill.
func TestHintBarTruncatesFromTheRight(t *testing.T) {
	full := HintBar("SESSION", testHints, 0)
	for w := 1; w <= VisWidth(full)+4; w++ {
		got := HintBar("SESSION", testHints, w)
		if !strings.Contains(got, "SESSION") {
			t.Fatalf("width %d: pill lost — %q", w, got)
		}
		if strings.ContainsAny(got, "\r\n") {
			t.Fatalf("width %d: hint bar must be exactly one row — %q", w, got)
		}
		// Every hint present must be a PREFIX of the full list (dropped from the right only).
		prev := -1
		for i, h := range testHints {
			if strings.Contains(got, h.Label) {
				if i != prev+1 {
					t.Fatalf("width %d: kept hint %q after dropping an earlier one — %q", w, h.Label, got)
				}
				prev = i
			}
		}
		// Never wider than asked, once there is room for the pill at all.
		if v := VisWidth(got); w >= VisWidth(" "+Pill("SESSION")) && v > w {
			t.Fatalf("width %d: bar is %d columns — %q", w, v, got)
		}
	}
	// Uncapped keeps everything.
	for _, h := range testHints {
		if !strings.Contains(full, h.Label) {
			t.Errorf("uncapped bar dropped %q: %q", h.Label, full)
		}
	}
}

// Growing the budget monotonically adds hints; it never takes one away.
func TestHintBarGrowsMonotonically(t *testing.T) {
	prev := 0
	for w := 1; w <= 140; w++ {
		n := 0
		got := HintBar("LIVE", testHints, w)
		for _, h := range testHints {
			if strings.Contains(got, h.Label) {
				n++
			}
		}
		if n < prev {
			t.Fatalf("width %d shows %d hints, fewer than the narrower bar's %d", w, n, prev)
		}
		prev = n
	}
}

// An overlay footer must never wrap (a second row paints over the frame it belongs to) and must
// never lose its exits to the indent. It gives up indentation instead.
func TestIndentedHintBarFitsBeforeItIndents(t *testing.T) {
	hints := PickerHints()
	for _, cols := range []int{20, 40, 60, 66, 80, 100, 132} {
		for _, boxW := range []int{34, 36, 48} {
			indent := (cols - boxW) / 2
			if indent < 0 {
				indent = 0
			}
			got := IndentedHintBar("HOST", hints, indent, cols)
			if w := VisWidth(got); w > cols {
				t.Fatalf("cols=%d boxW=%d: footer is %d columns: %q", cols, boxW, w, got)
			}
			if strings.ContainsAny(got, "\r\n") {
				t.Fatalf("cols=%d: footer wrapped: %q", cols, got)
			}
			if !strings.Contains(got, "HOST") {
				t.Fatalf("cols=%d: footer lost its pill: %q", cols, got)
			}
			// At any width a real terminal actually has, the way OUT stays on screen.
			if cols >= 60 && !strings.Contains(got, "close") {
				t.Errorf("cols=%d boxW=%d: footer dropped its exit hint: %q", cols, boxW, got)
			}
		}
	}
}

func TestHintBarKeylessNote(t *testing.T) {
	got := HintBar("SETUP", []Hint{{Label: "pick two sessions — they become one tab"}}, 0)
	if !strings.Contains(got, "pick two sessions") || !strings.Contains(got, "SETUP") {
		t.Errorf("keyless note bar = %q", got)
	}
}

// The pill must survive themed()-style 256 remapping: neither its bg nor its fg index may be
// one the launcher's theme tables rewrite. See the THEME DECISION comment in pill.go.
func TestPillIsThemeImmune(t *testing.T) {
	// Source indexes remapped by at least one theme in llms_theme.go.
	remapped := []string{
		"231", "252", "250", "245", "243", "242", "240", "238", "237", "236",
		"215", "214", "46", "114", "108", "51", "203", "117", "111", "80", "75", "207",
		"208", "209", "220", "213", "212", "211", "205", "204",
	}
	for _, idx := range remapped {
		for _, seq := range []string{"\x1b[48;5;" + idx + "m", "\x1b[38;5;" + idx + "m", ";5;" + idx + "m"} {
			if strings.Contains(pillBg256+pillFg, seq) {
				t.Errorf("pill uses themed 256 index %s (%q) — themed() will rewrite it", idx, seq)
			}
		}
	}
}
