package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"partyline.sh/partyline/internal/brand"
)

// THE REGRESSION THESE TESTS PIN. The boxed menus used to paint a centered frame and then print the
// numbered list sequentially, so the list landed OUTSIDE the frame — item 1 under it, items 2..n at
// the far left margin. So the assertions are not "does it look nice": they are (a) the list and the
// prompt are part of the painted frame, and (b) NO painted row puts a glyph outside the border
// columns. Everything here works on the pure paint string — no terminal required.

var (
	cgPosRe = regexp.MustCompile(`\x1b\[(\d+);(\d+)H`)
	cgEscRe = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)
)

// cgPlain is the painted string with every escape sequence removed — what a human actually sees.
func cgPlain(s string) string { return cgEscRe.ReplaceAllString(s, "") }

// cgPainted is one positioned write from a painted frame: where it starts, and the visible text it
// puts there (SGR stripped, further positioning excluded — a segment ends at the next move).
type cgPainted struct {
	row, col int
	text     string
}

// cgParsePaint splits a paint into its positioned segments. Anything printed WITHOUT a preceding
// position (the old sequential fmt.Printf) shows up as a segment at row/col 0, which the border test
// then rejects — that is the point.
func cgParsePaint(t *testing.T, paint string) []cgPainted {
	t.Helper()
	var out []cgPainted
	locs := cgPosRe.FindAllStringSubmatchIndex(paint, -1)
	// A newline is what made the old rendering wrong: it returns to column 1, so only the FIRST line
	// after a move is positioned and every later one lands at the left margin. The parser has to model
	// that, or it would score a sequential print as if it were inside the frame.
	add := func(row, col int, s string) {
		for i, part := range strings.Split(cgPlain(s), "\n") {
			r, c := row, col
			if i > 0 {
				r, c = row+i, 1
			}
			if strings.TrimSpace(part) != "" {
				out = append(out, cgPainted{r, c, part})
			}
		}
	}
	if len(locs) == 0 || locs[0][0] > 0 {
		end := len(paint)
		if len(locs) > 0 {
			end = locs[0][0]
		}
		add(0, 0, paint[:end])
	}
	for i, l := range locs {
		row, _ := strconv.Atoi(paint[l[2]:l[3]])
		col, _ := strconv.Atoi(paint[l[4]:l[5]])
		end := len(paint)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		add(row, col, paint[l[1]:end])
	}
	return out
}

func cgTestModal() cgModal {
	return cgModal{
		Title: "Ask a peer",
		Body:  []string{dim("read-only feedback from a teammate's agent — who do you want to ask?")},
		Items: []string{"● mac-studio · partyline", "○ air · acr-cloud", "● air · hoops-ops"},
		Hints: []brand.Hint{{Key: "1-3", Label: "pick a peer"}, {Key: "q · esc", Label: "back"}},
	}
}

// The list and the input row are part of the FRAME. Before this, both were printed after it.
func TestModalFrameCarriesTheListAndThePromptRow(t *testing.T) {
	m := cgTestModal()
	m.Prompt = cgPromptRow("pick a peer", "1-3", "")
	lines, cl, cv, shown := m.lines(40)
	if shown != 3 {
		t.Fatalf("shown = %d, want 3", shown)
	}
	paint := cgPaintLines(lines, cl, cv, 120, 40)
	for _, want := range []string{"mac-studio", "acr-cloud", "hoops-ops", "pick a peer", "Ask a peer", "╭", "╰"} {
		if !strings.Contains(cgPlain(paint), want) {
			t.Errorf("the painted frame is missing %q — is it still being printed after the box?", want)
		}
	}
	// Numbered by the frame, 1-based, and each number is on the same painted row as its item.
	for i, item := range m.Items {
		row := fmt.Sprintf("%2d  %s", i+1, item)
		if !strings.Contains(cgPlain(paint), row) {
			t.Errorf("row %d missing or unnumbered: want %q", i+1, row)
		}
	}
	if cl < 0 {
		t.Error("a modal with a prompt must place the cursor on the prompt row")
	}
}

// cgSpills returns a description of every painted segment that lands outside the box the paint
// itself defines (its top border gives the left column and the total width), plus any text that
// wasn't positioned at all. Empty means the frame contains everything it drew.
func cgSpills(t *testing.T, paint string, rows int) []string {
	t.Helper()
	segs := cgParsePaint(t, paint)
	if len(segs) == 0 {
		t.Fatal("nothing painted")
	}
	top := segs[0]
	if !strings.HasPrefix(top.text, "╭") {
		t.Fatalf("first painted segment isn't the top border: %q", top.text)
	}
	left, width := top.col, brand.VisWidth(top.text)
	var bad []string
	for _, sg := range segs {
		switch {
		case sg.row == 0 || sg.col == 0:
			bad = append(bad, fmt.Sprintf("unpositioned %q", sg.text))
		case sg.col < left || sg.col+brand.VisWidth(sg.text) > left+width:
			bad = append(bad, fmt.Sprintf("row %d col %d outside [%d,%d): %q",
				sg.row, sg.col, left, left+width, sg.text))
		case sg.row < 1 || sg.row > rows:
			bad = append(bad, fmt.Sprintf("row %d off screen: %q", sg.row, sg.text))
		}
	}
	return bad
}

// THE ACTUAL BUG: content outside the border columns. Every positioned segment must fit inside the
// box, and there must be no unpositioned text at all (that's the sequential print, by definition).
func TestNoPaintedRowLandsOutsideTheBorder(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 18}, {40, 12}} {
		cols, rows := size[0], size[1]
		m := cgTestModal()
		m.Prompt = cgPromptRow("pick a peer", "1-3", "12")
		lines, cl, cv, _ := m.lines(rows)
		for _, bad := range cgSpills(t, cgPaintLines(lines, cl, cv, cols, rows), rows) {
			t.Errorf("%dx%d: %s", cols, rows, bad)
		}
	}
}

// The detector above is only worth having if it FAILS on the old rendering, so: paint a frame and
// then print the list after it, the way cgBox + Pick did, and check that the parse flags it.
func TestTheBorderCheckCatchesASequentialPrint(t *testing.T) {
	m := cgTestModal()
	lines, cl, cv, _ := m.lines(24)
	oldStyle := cgPaintLines(lines, cl, cv, 80, 24) +
		"     1  ● mac-studio · partyline\n     2  ○ air · acr-cloud\n  number › "
	if len(cgSpills(t, oldStyle, 24)) == 0 {
		t.Fatal("the border check would not have caught the bug it exists for")
	}
}

// A list taller than the frame renders exactly the rows that fit, and SAYS how many it kept back.
// Overflowing would wrap and shred the border; truncating silently would lie about the list.
func TestModalScrollsRatherThanOverflowing(t *testing.T) {
	m := cgTestModal()
	m.Items = nil
	for i := 0; i < 40; i++ {
		m.Items = append(m.Items, fmt.Sprintf("thread number %d", i+1))
	}
	m.Prompt = cgPromptRow("attach", "1-40 ⏎", "")
	const rows = 20
	lines, cl, cv, shown := m.lines(rows)
	if shown == 0 || shown >= len(m.Items) {
		t.Fatalf("shown = %d, want a window smaller than %d", shown, len(m.Items))
	}
	if len(lines)+2 > rows {
		t.Errorf("the frame is %d rows in a %d-row terminal", len(lines)+2, rows)
	}
	plain := cgPlain(cgPaintLines(lines, cl, cv, 100, rows))
	if want := fmt.Sprintf("↓ %d more", len(m.Items)-shown); !strings.Contains(plain, want) {
		t.Errorf("missing the overflow affordance %q", want)
	}
	if strings.Contains(plain, fmt.Sprintf("thread number %d", shown+1)) {
		t.Errorf("row %d is outside the window but was painted anyway", shown+1)
	}
	// Scrolled: the window starts where Off says, and the numbers stay ABSOLUTE so the key that
	// selects a row is the number printed on it.
	m.Off = 10
	lines, cl, cv, shown = m.lines(rows)
	plain = cgPlain(cgPaintLines(lines, cl, cv, 100, rows))
	if !strings.Contains(plain, "11  thread number 11") {
		t.Errorf("a scrolled window must keep absolute numbering:\n%s", plain)
	}
	if strings.Contains(plain, "thread number 1 ") {
		t.Error("row 1 is scrolled off but was painted")
	}
	if shown+m.Off > len(m.Items) {
		t.Errorf("window %d..%d runs past the list", m.Off, m.Off+shown)
	}
}

// The screens from the screenshot, as the converted call sites actually build them: the peer inbox
// (a list whose rows carry status and a wrapped question) and answer-or-decline (a long wrapped
// question above two choices). Both used to spill; neither may now, at any terminal size.
func TestConvertedScreensPaintInsideTheFrame(t *testing.T) {
	inbox := cgPicker{Title: "Peer messages", Verb: "open",
		Body: []string{dim("questions waiting on you, replies that landed while you worked")},
		Items: []string{
			peerMessageRow(peerMessage{Direction: dirInbound, Peer: "darcy", Project: "partyline",
				Question: "does the limiter retry 429s?", Status: taskAuthRequired}),
			peerMessageRow(peerMessage{Direction: dirOutbound, Peer: "mac-studio", Project: "acr-cloud",
				Question: "safe to merge the tray branch?", Status: taskSubmitted}),
		},
		Extras: []cgChoice{{Key: 'n', Label: "ask someone new"}}}

	long := strings.Repeat("is it safe to merge this branch before the release freeze? ", 6)
	answer := cgPicker{Title: "Peer messages", Verb: "choose",
		Body:  append([]string{dim("darcy asks · partyline"), ""}, questionLines(long, 68, 12)...),
		Items: []string{"answer read-only", "decline"}}

	for _, p := range []cgPicker{inbox, answer} {
		for _, size := range [][2]int{{200, 50}, {120, 40}, {80, 24}, {50, 14}} {
			cols, rows := size[0], size[1]
			m := cgModal{Title: p.Title, Body: p.Body, Items: p.Items,
				Prompt: cgPromptRow(p.Verb, cgNumKey(len(p.Items)), ""),
				Hints:  []brand.Hint{{Key: cgNumKey(len(p.Items)), Label: p.Verb}, {Key: "q · esc", Label: "back"}}}
			lines, cl, cv, _ := m.lines(rows)
			for _, bad := range cgSpills(t, cgPaintLines(lines, cl, cv, cols, rows), rows) {
				t.Errorf("%s at %dx%d: %s", p.Title, cols, rows, bad)
			}
		}
	}
}

// A message box whose body is longer than the screen (cgNote showing a peer's whole answer) also has
// to give up content rather than shape — a frame taller than the terminal scrolls and tears.
func TestLongBodyIsEllipsisedToFitTheScreen(t *testing.T) {
	var body []string
	for i := 0; i < 60; i++ {
		body = append(body, fmt.Sprintf("  answer line %d", i+1))
	}
	m := cgModal{Title: "Peer messages", Body: body,
		Hints: []brand.Hint{{Key: "⏎ · esc", Label: "back"}}}
	for _, rows := range []int{50, 24, 12, 8} {
		lines, cl, cv, _ := m.lines(rows)
		if len(lines)+2 > rows {
			t.Errorf("%d rows: frame is %d rows tall", rows, len(lines)+2)
		}
		paint := cgPaintLines(lines, cl, cv, 80, rows)
		if !strings.Contains(cgPlain(paint), "…") {
			t.Errorf("%d rows: a truncated body must say so", rows)
		}
		for _, bad := range cgSpills(t, paint, rows) {
			t.Errorf("%d rows: %s", rows, bad)
		}
	}
}

// No mode pill on these modals: the title row already says where you are, and an "ASK PEER" pill
// inside a box titled "Ask a peer" was pure redundancy.
func TestModalsCarryNoModePill(t *testing.T) {
	m := cgTestModal()
	m.Prompt = cgPromptRow("pick a peer", "1-3", "")
	lines, cl, cv, _ := m.lines(24)
	paint := cgPaintLines(lines, cl, cv, 100, 24)
	if strings.Contains(paint, brand.PillBg()) {
		t.Error("a one-shot modal has no mode to indicate — the pill is the title's job")
	}
	if !strings.Contains(cgPlain(paint), "back") {
		t.Error("the hint bar's exits must still be there")
	}
}

// A modal with nothing to type hides the cursor rather than parking it somewhere that would corrupt
// the next thing printed — the cgBox behaviour that started all this.
func TestModalWithoutAPromptHidesTheCursor(t *testing.T) {
	m := cgTestModal()
	lines, cl, cv, _ := m.lines(24)
	if cl != cgCursorHide {
		t.Fatalf("cursorLine = %d, want cgCursorHide", cl)
	}
	if !strings.Contains(cgPaintLines(lines, cl, cv, 80, 24), "\x1b[?25l") {
		t.Error("a promptless modal must hide the cursor")
	}
	m.Prompt = cgPromptRow("kind", "1-3", "")
	lines, cl, cv, _ = m.lines(24)
	if !strings.Contains(cgPaintLines(lines, cl, cv, 80, 24), "\x1b[?25h") {
		t.Error("a modal that takes typing must show the cursor")
	}
}
