package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"partyline.sh/partyline/internal/api"
)

// fakeSource is a provider stand-in: it returns whatever the test hands it, so the model and render
// paths can be exercised against a foreign board without spawning a subprocess.
type fakeSource struct {
	name   string
	scopes []boardScope
	data   *boardData
	err    error
	loads  int
	last   string // the scope id it was asked for
}

func (f *fakeSource) Name() string                  { return f.name }
func (f *fakeSource) Scopes() ([]boardScope, error) { return f.scopes, nil }
func (f *fakeSource) Load(scope string) (*boardData, error) {
	f.loads++
	f.last = scope
	return f.data, f.err
}

func foreignBoard() *boardData {
	d := &boardData{
		Columns: []boardColumn{
			{Key: "new", Title: "New"},
			{Key: "doing", Title: "In Progress"},
		},
		ByColumn: map[api.BoardColumn][]api.BoardCard{
			"new":   {{ID: "t1", Task: "Printer drops a line", Foreign: true, StateLabel: "open", Column: "new"}},
			"doing": {{ID: "t2", Task: "Tax rounding", Foreign: true, StateLabel: "assigned", Column: "doing"}},
		},
		Source: "odoo", Scope: "ACR POS", Live: false, ReadAt: time.Now(),
	}
	return d
}

// The point of the whole seam: the board renders the columns the SOURCE declared, not partyline's
// five. Squeezing a foreign tracker into backlog/building/blocked/review/accepted loses exactly what
// you opened it for.
func TestForeignBoardRendersItsOwnColumns(t *testing.T) {
	m := newBoardModel()
	m.sources = []boardSource{&fakeSource{name: "odoo", data: foreignBoard()}}
	m.data, m.w, m.h = foreignBoard(), 120, 30

	out := plain(m.frame())
	for _, want := range []string{"New", "In Progress", "Printer drops a line"} {
		if !strings.Contains(out, want) {
			t.Errorf("frame is missing %q", want)
		}
	}
	for _, unwanted := range []string{"Backlog", "Building", "Accepted"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("partyline's column %q leaked onto a foreign board", unwanted)
		}
	}
}

// A foreign card has no run behind it, so nothing that acts on a run may be offered. The server
// would refuse anyway; offering the move is the bug.
func TestForeignCardOffersNoRunActions(t *testing.T) {
	c := api.BoardCard{ID: "t1", Task: "x", Foreign: true, StateLabel: "open"}
	if got := boardActions(c); len(got) != 0 {
		t.Fatalf("foreign card offered %v", keysOf(got))
	}
	if _, ok := primaryAction(c); ok {
		t.Fatal("a foreign card has no primary move")
	}
}

// The provider's own word is the state, since cardState's run reasoning does not apply.
func TestForeignCardStateComesFromTheProvider(t *testing.T) {
	c := api.BoardCard{Foreign: true, StateLabel: "waiting on customer"}
	label, urgent := cardState(c)
	if label != "waiting on customer" || urgent {
		t.Fatalf("cardState = (%q,%v)", label, urgent)
	}

	// Urgency is the provider's to declare, and it must survive.
	c.Attention = true
	if _, urgent := cardState(c); !urgent {
		t.Fatal("a provider-declared urgent card must count as urgent")
	}

	// A provider that says nothing gets a dash rather than an invented status.
	if label, _ := cardState(api.BoardCard{Foreign: true}); label != "—" {
		t.Fatalf("empty state = %q", label)
	}
}

// Terminal injection. A ticket title is written by whoever files tickets in someone else's tracker
// and lands in a renderer that draws with escape sequences; VisWidth measures ESC as zero-width, so
// an unsanitized title reaches the screen intact.
func TestForeignTextIsStrippedOfControlCharacters(t *testing.T) {
	nasty := "\x1b[2Jwipe\x07 the \x1b]0;title\x07screen\x1b[31m"
	got := safeForeignText(nasty)

	if strings.ContainsRune(got, 0x1b) {
		t.Fatalf("ESC survived sanitizing: %q", got)
	}
	for _, r := range got {
		if r < 0x20 && r != ' ' {
			t.Fatalf("control character %q survived in %q", r, got)
		}
	}
	if !strings.Contains(got, "wipe") || !strings.Contains(got, "screen") {
		t.Fatalf("sanitizing ate the readable text: %q", got)
	}
}

func TestForeignTextFlattensNewlinesAndCaps(t *testing.T) {
	if got := safeForeignText("one\ntwo\r\nthree"); strings.ContainsAny(got, "\n\r") {
		t.Fatalf("newline survived: %q", got)
	}
	long := strings.Repeat("x", maxForeignField*2)
	if got := safeForeignText(long); len(got) > maxForeignField {
		t.Fatalf("length cap not applied: %d", len(got))
	}
}

// A provider must not be able to smuggle a URL the board would hand to the desktop.
func TestForeignURLKeepsOnlyHTTP(t *testing.T) {
	for in, want := range map[string]string{
		"https://odoo.example/web#id=1": "https://odoo.example/web#id=1",
		"http://odoo.example/x":         "http://odoo.example/x",
		"javascript:alert(1)":           "",
		"file:///etc/passwd":            "",
		"":                              "",
	} {
		if got := safeForeignURL(in); got != want {
			t.Errorf("safeForeignURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeForeignBoardCleansEveryField(t *testing.T) {
	d := &boardData{
		Columns:  []boardColumn{{Key: "new", Title: "\x1b[31mNew"}},
		ByColumn: map[api.BoardColumn][]api.BoardCard{"new": {{ID: "1", Task: "\x1bbad", SourceURL: "javascript:x"}}},
		Source:   "od\x1boo",
	}
	sanitizeForeignBoard(d)

	if strings.ContainsRune(d.Columns[0].Title, 0x1b) || strings.ContainsRune(d.Source, 0x1b) {
		t.Fatal("column title or source name not sanitized")
	}
	c := d.ByColumn["new"][0]
	if strings.ContainsRune(c.Task, 0x1b) {
		t.Fatal("card text not sanitized")
	}
	if c.SourceURL != "" {
		t.Fatalf("non-http URL survived: %q", c.SourceURL)
	}
	if !c.Foreign {
		t.Fatal("sanitizing must mark the cards foreign — it is the flag every read-only guard keys off")
	}
}

// Scope is remembered per SOURCE, so switching to Odoo returns to the project you were in and
// switching back to partyline does not inherit Odoo's scope.
func TestScopeIsRememberedPerSource(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	saveBoardScope("odoo", "42", "ACR POS")
	saveBoardScope("jira", "ENG", "Engineering")

	if got := loadBoardScope("odoo"); got != "42" {
		t.Fatalf("odoo scope = %q", got)
	}
	if got := loadBoardScope("jira"); got != "ENG" {
		t.Fatalf("jira scope = %q", got)
	}
	if got := loadBoardScope("partyline"); got != "" {
		t.Fatalf("a source with no remembered scope returned %q", got)
	}

	saveBoardScope("odoo", "", "")
	if got := loadBoardScope("odoo"); got != "" {
		t.Fatalf("clearing a scope left %q", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".partyline", "board-scopes.json")); err != nil {
		t.Fatalf("scope file not written: %v", err)
	}
}

// Switching sources must not carry the previous source's cursor or scope across.
func TestSwitchingSourceResetsView(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	odoo := &fakeSource{name: "odoo", data: foreignBoard()}

	m := newBoardModel()
	m.sources = []boardSource{partylineSource{}, odoo}
	m.data = pboard(&api.Board{Backlog: []api.BoardCard{card("a"), card("b")}})
	m.cursor[api.ColBacklog] = 1
	m.rememberFocus()

	m.pickSource(nil)
	if !pickFromOverlay(t, m, "odoo") {
		t.Fatal("choosing a second board should ask for a reload")
	}
	if m.activeSource().Name() != "odoo" {
		t.Fatalf("active source = %q", m.activeSource().Name())
	}
	if m.focusID != "" || m.data != nil {
		t.Fatal("the previous source's board and cursor must not survive the switch")
	}
}

// With nothing but partyline connected the picker still opens — it is also where a board gets
// added, and a dead end that says "nothing to switch to" would hide the way forward.
func TestPickerWithOneSourceStillOffersAdd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := newBoardModel()
	m.sources = []boardSource{partylineSource{}}
	m.pickSource(nil)

	if m.toast == "" {
		t.Error("having only one board should say so")
	}
	p, ok := m.overlay.(*pickerOverlay)
	if !ok {
		t.Fatal("the board picker should still be open")
	}
	if len(p.items) != 2 || p.items[1].Value != addBoardChoice {
		t.Fatalf("the picker must end with the add row, got %+v", p.items)
	}
}

// pickFromOverlay drives the open picker the way a keypress would: find the row whose label starts
// with `label`, and choose it.
func pickFromOverlay(t *testing.T, m *boardModel, label string) bool {
	t.Helper()
	p, ok := m.overlay.(*pickerOverlay)
	if !ok {
		t.Fatal("no picker is open")
	}
	for _, it := range p.items {
		if strings.HasPrefix(it.Label, label) {
			m.closeOverlay()
			return p.onPick(m, nil, it)
		}
	}
	t.Fatalf("no row labelled %q in %+v", label, p.items)
	return false
}

// The refresh contract: partyline polls, a foreign board does not.
func TestForeignBoardIsNotLive(t *testing.T) {
	if !boardFromAPI(&api.Board{}).Live {
		t.Fatal("partyline's own board is live")
	}
	if foreignBoard().Live {
		t.Fatal("a foreign board must never auto-poll — that is somebody else's Odoo")
	}
}

// …and it must SAY it is manual, with an age. A board that looks live but is not is worse than one
// that admits it.
func TestFreshnessNoteOnlyForManualSources(t *testing.T) {
	if got := freshnessNote(boardFromAPI(&api.Board{})); got != "" {
		t.Fatalf("a live board should say nothing about freshness, got %q", got)
	}
	d := foreignBoard()
	d.ReadAt = time.Now().Add(-4 * time.Minute)
	got := freshnessNote(d)
	if !strings.Contains(got, "4m ago") || !strings.Contains(got, "g to refresh") {
		t.Fatalf("freshness = %q, want an age and the key", got)
	}
	d.ReadAt = time.Now().Add(-3 * time.Hour)
	if got := freshnessNote(d); !strings.Contains(got, "3h ago") {
		t.Fatalf("hours = %q", got)
	}
}

// The wire decoder is the trust boundary: a malformed or hostile payload must produce an error or a
// clean board, never a panic and never unsanitized text.
func TestProviderRejectsUnusablePayloads(t *testing.T) {
	for name, body := range map[string]string{
		"not json":     `{{{`,
		"no columns":   `{"columns":[],"cards":[]}`,
		"blank column": `{"columns":[{"key":"","title":"x"}],"cards":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			var w wireBoard
			if json.Unmarshal([]byte(body), &w) == nil && len(w.Columns) > 0 {
				// decoded, so the column filter is what must reject it
				ok := false
				for _, c := range w.Columns {
					if strings.TrimSpace(c.Key) != "" {
						ok = true
					}
				}
				if ok {
					t.Fatalf("payload %q should not have yielded a usable column", name)
				}
			}
		})
	}
}

// A card naming a column the provider never declared has nowhere to go, and must be dropped rather
// than inventing a column or panicking.
func TestCardsInUndeclaredColumnsAreDropped(t *testing.T) {
	d := &boardData{
		Columns:  []boardColumn{{Key: "new", Title: "New"}},
		ByColumn: map[api.BoardColumn][]api.BoardCard{"new": {{ID: "1", Task: "kept"}}},
	}
	sanitizeForeignBoard(d)
	if len(d.Column("ghost")) != 0 {
		t.Fatal("an undeclared column must be empty")
	}
	if len(d.Column("new")) != 1 {
		t.Fatal("the declared column lost its card")
	}
}

func TestSortedScopesIsAlphabetical(t *testing.T) {
	got := sortedScopes([]boardScope{{Label: "zeta"}, {Label: "Alpha"}, {Label: "mid"}})
	want := []string{"Alpha", "mid", "zeta"}
	for i, w := range want {
		if got[i].Label != w {
			t.Fatalf("scope %d = %q, want %q", i, got[i].Label, w)
		}
	}
}

// Only the built-in board rings. We have no basis for deciding which of somebody's Jira statuses
// deserves a bell, and beeping at a foreign column change would be noise.
func TestBellIgnoresForeignBoards(t *testing.T) {
	before := &boardData{ByColumn: map[api.BoardColumn][]api.BoardCard{}}
	after := foreignBoard()
	after.ByColumn[api.ColReview] = []api.BoardCard{{ID: "x", Task: "arrived", Foreign: true}}
	if ring, note := boardBell(before, after); ring {
		t.Fatalf("a foreign card must not ring: %q", note)
	}
}

// A picker label must get the room the box actually has. It was clipped to a flat 24 columns
// whatever the terminal, which is fine for "monolith" and cuts a real Odoo or Jira project name in
// half — the name being chosen matters more than the count beside it.
func TestPickerLabelUsesTheAvailableWidth(t *testing.T) {
	long := "ACR POS — receipt and payment terminal integration"
	o := &pickerOverlay{items: []pickerItem{{Label: long, Note: "48 open"}}}

	line := plain(o.lines(newBoardModel(), 74, 10)[0])
	if !strings.Contains(line, "ACR POS — receipt and payment") {
		t.Fatalf("a 74-column box clipped the name too early:\n%q", line)
	}
	if !strings.Contains(line, "48 open") {
		t.Fatalf("the note was lost:\n%q", line)
	}
}

// …and in a cramped box the NAME keeps a usable minimum rather than the note taking it.
func TestPickerKeepsTheNameInANarrowBox(t *testing.T) {
	o := &pickerOverlay{items: []pickerItem{{Label: "ACR POS terminal", Note: "48 open"}}}
	line := plain(o.lines(newBoardModel(), 30, 10)[0])
	if !strings.Contains(line, "ACR POS") {
		t.Fatalf("narrow box lost the name:\n%q", line)
	}
}

// With no notes at all, the name gets the whole row.
func TestPickerWithoutNotesGivesTheNameEverything(t *testing.T) {
	o := &pickerOverlay{items: []pickerItem{{Label: strings.Repeat("x", 60)}}}
	line := plain(o.lines(newBoardModel(), 74, 10)[0])
	if visWidth(strings.TrimRight(line, " ")) < 50 {
		t.Fatalf("name should have filled the row, got %d columns:\n%q", visWidth(line), line)
	}
}

// The point of showing a foreign board is not having to open the tracker. A tile is three lines and
// must stay scannable, so everything else belongs in the detail pane — and before this the pane
// showed three lines and an empty "run log" heading, which sent you straight back to Odoo.
func TestForeignDetailPaneCarriesTheProvidersFields(t *testing.T) {
	c := api.BoardCard{
		ID: "118", Task: "Receipt printer drops a line", Foreign: true, StateLabel: "open",
		Fields: []api.BoardCardField{
			{Label: "assignee", Value: "Sam"},
			{Label: "customer", Value: "Northgate Store"},
			{Label: "deadline", Value: "2026-09-15"},
		},
		Body: "Till 3 only.\n\nReproduced twice on Friday.",
	}
	o := &detailOverlay{card: c}
	out := strings.Join(o.factLines(70), "\n")

	for _, want := range []string{"assignee", "Sam", "customer", "Northgate Store", "deadline",
		"Till 3 only", "Reproduced twice"} {
		if !strings.Contains(out, want) {
			t.Errorf("detail pane is missing %q", want)
		}
	}
	if strings.Contains(out, "run log") {
		t.Error("a foreign card has no run — that heading can never fill in")
	}
	if o.logLines(70) != nil {
		t.Error("a foreign card must not render a log section")
	}
}

// Field ORDER is the provider's, because it knows which of its fields a reader looks at first.
func TestForeignFieldsKeepProviderOrder(t *testing.T) {
	o := &detailOverlay{card: api.BoardCard{
		Foreign: true,
		Fields: []api.BoardCardField{
			{Label: "zeta", Value: "1"}, {Label: "alpha", Value: "2"},
		},
	}}
	out := strings.Join(o.factLines(70), "\n")
	if strings.Index(out, "zeta") > strings.Index(out, "alpha") {
		t.Fatal("fields were reordered — the provider's order is the meaningful one")
	}
}

// A partyline card is untouched: it still heads its run log and ignores the foreign-only fields.
func TestPartylineCardStillShowsItsRunLog(t *testing.T) {
	o := &detailOverlay{card: card("r1", withStatus("running"))}
	if !strings.Contains(strings.Join(o.factLines(70), "\n"), "run log") {
		t.Fatal("a partyline card lost its run-log heading")
	}
}

// A description is meant to have lines. The tile sanitizer flattens them, which would run a whole
// task description into one paragraph.
func TestForeignBlockKeepsLinesButNotControlCharacters(t *testing.T) {
	got := safeForeignBlock("first\r\n\r\nsecond\x1b[2Jthird\ttab")

	if !strings.Contains(got, "\n") {
		t.Fatal("line structure was flattened — a description must keep its paragraphs")
	}
	if strings.ContainsRune(got, 0x1b) || strings.ContainsRune(got, '\r') {
		t.Fatalf("control characters survived: %q", got)
	}
	if !strings.Contains(got, "first") || !strings.Contains(got, "third") {
		t.Fatalf("readable text was lost: %q", got)
	}
	if strings.Contains(got, "\t") {
		t.Fatalf("tab should become spaces so it cannot break the layout: %q", got)
	}
}

func TestForeignBlockIsBounded(t *testing.T) {
	got := safeForeignBlock(strings.Repeat("x", maxForeignBody*2))
	if len(got) > maxForeignBody+32 {
		t.Fatalf("body not capped: %d", len(got))
	}
	if !strings.Contains(got, "truncated") {
		t.Error("a truncated body should say so rather than just stopping")
	}
}

// The whole payload path: fields and body survive decode and sanitizing together.
func TestProviderPayloadCarriesFieldsAndBody(t *testing.T) {
	p := writeFakeProviderWithDetail(t)
	d, err := p.Load("42")
	if err != nil {
		t.Fatal(err)
	}
	c, ok := d.Find("t1")
	if !ok {
		t.Fatal("card missing")
	}
	if len(c.Fields) != 2 || c.Fields[0].Label != "assignee" {
		t.Fatalf("fields = %+v", c.Fields)
	}
	if !strings.Contains(c.Body, "Till 3 only") {
		t.Fatalf("body = %q", c.Body)
	}
	if strings.ContainsRune(c.Body, 0x1b) {
		t.Fatal("an escape sequence in a description reached the board")
	}
}
