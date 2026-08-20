package traypeer

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// fakeItem stands in for *systray.MenuItem. It records the last title and whether the row is
// currently shown, which is the entire observable behaviour of a menu row.
type fakeItem struct {
	title string
	shown bool
}

func (f *fakeItem) SetTitle(s string) { f.title = s }
func (f *fakeItem) Show()             { f.shown = true }
func (f *fakeItem) Hide()             { f.shown = false }

// harness is a whole pre-allocated section plus the notifications it posted.
type harness struct {
	sec                   *Section
	hdr, more, auto, repl *fakeItem
	rows                  []*fakeItem
	qlines                [][]*fakeItem
	rowRefs               []*Row
	posted                []string
	quiet                 bool
}

func newHarness() *harness {
	h := &harness{hdr: &fakeItem{}, more: &fakeItem{}, auto: &fakeItem{}, repl: &fakeItem{}}
	for i := 0; i < MaxRows; i++ {
		item := &fakeItem{}
		lines := make([]*fakeItem, QLines)
		items := make([]Item, QLines)
		for j := range lines {
			lines[j] = &fakeItem{}
			items[j] = lines[j]
		}
		h.rows = append(h.rows, item)
		h.qlines = append(h.qlines, lines)
		h.rowRefs = append(h.rowRefs, &Row{Item: item, QLines: items})
	}
	h.sec = NewSection(Config{
		Hdr: h.hdr, More: h.more, Auto: h.auto, Repl: h.repl, Rows: h.rowRefs,
		Post:  func(b string) { h.posted = append(h.posted, b) },
		Quiet: func() bool { return h.quiet },
	})
	return h
}

func (h *harness) poll(p *Snapshot) { h.sec.Poll(true, p) }

func (h *harness) drain() []string {
	got := h.posted
	h.posted = nil
	return got
}

func one(project, question string) *Snapshot {
	return &Snapshot{Inbound: 1, Consults: []Consult{{ID: "c1", Project: project, Question: question, WaitingSec: 12}}}
}

// ---- visibility

func TestRowsAppearOnlyWhenNonZeroAndDisappearWhenCleared(t *testing.T) {
	h := newHarness()

	// Nothing to report: the whole block is invisible.
	h.poll(&Snapshot{})
	for i, r := range h.rows {
		if r.shown {
			t.Fatalf("row %d visible with an empty snapshot", i)
		}
	}
	for _, it := range []*fakeItem{h.hdr, h.more, h.auto, h.repl} {
		if it.shown {
			t.Fatal("a section line is visible with an empty snapshot")
		}
	}

	// Two questions, one auto-answer, one reply.
	h.poll(&Snapshot{
		Inbound: 2, Answered: 1, AutoAnswered: 3, AutoProject: "partyline",
		Consults: []Consult{
			{ID: "a", Project: "partyline", Question: "does the daemon retry?", WaitingSec: 5},
			{ID: "b", Project: "acr-pos", Question: "is this index used?", WaitingSec: 130},
		},
	})
	if !h.hdr.shown || h.hdr.title != "2 questions need you" {
		t.Fatalf("header = %q shown=%v", h.hdr.title, h.hdr.shown)
	}
	if !h.rows[0].shown || !h.rows[1].shown {
		t.Fatal("the two queued questions should both be visible")
	}
	if h.rows[2].shown || h.rows[3].shown {
		t.Fatal("unused rows must stay hidden")
	}
	if want := "◆ partyline · waiting 5s"; h.rows[0].title != want {
		t.Fatalf("row 0 title = %q, want %q", h.rows[0].title, want)
	}
	if want := "◆ acr-pos · waiting 2m"; h.rows[1].title != want {
		t.Fatalf("row 1 title = %q, want %q", h.rows[1].title, want)
	}
	if h.rowRefs[0].CurrentID() != "a" || h.rowRefs[1].CurrentID() != "b" {
		t.Fatal("rows must carry the id they currently stand for")
	}
	if h.more.shown {
		t.Fatal("no overflow, so the …more line must be hidden")
	}
	if !h.auto.shown || !strings.Contains(h.auto.title, "3 peer questions answered today") {
		t.Fatalf("auto line = %q shown=%v", h.auto.title, h.auto.shown)
	}
	if !strings.Contains(h.auto.title, "latest: partyline") {
		t.Fatalf("auto line should name the latest project: %q", h.auto.title)
	}
	if !h.repl.shown || !strings.Contains(h.repl.title, "1 reply waiting") {
		t.Fatalf("reply line = %q shown=%v", h.repl.title, h.repl.shown)
	}

	// Cleared: every line disappears again and rows forget their ids, so a stale click can't act.
	h.poll(&Snapshot{})
	for i, r := range h.rows {
		if r.shown {
			t.Fatalf("row %d still visible after the queue cleared", i)
		}
		if h.rowRefs[i].CurrentID() != "" {
			t.Fatalf("row %d still holds id %q after being hidden", i, h.rowRefs[i].CurrentID())
		}
	}
	for _, it := range []*fakeItem{h.hdr, h.more, h.auto, h.repl} {
		if it.shown {
			t.Fatal("a section line is still visible after the queue cleared")
		}
	}
}

func TestOverflowCollapsesIntoMoreRow(t *testing.T) {
	h := newHarness()
	cs := make([]Consult, MaxRows)
	for i := range cs {
		cs[i] = Consult{ID: string(rune('a' + i)), Project: "p", Question: "q"}
	}
	h.poll(&Snapshot{Inbound: MaxRows + 3, Consults: cs})
	if !h.more.shown || !strings.Contains(h.more.title, "and 3 more waiting") {
		t.Fatalf("more line = %q shown=%v", h.more.title, h.more.shown)
	}
}

func TestQuestionLinesAlwaysClearLeftovers(t *testing.T) {
	h := newHarness()
	long := "this question is quite long and will certainly wrap across more than one submenu line for sure"
	h.poll(one("p", long))
	if h.qlines[0][1].title == "" {
		t.Fatal("a long question should occupy more than one line")
	}
	// A shorter question must blank the lines the longer one used, or the menu shows a sentence that
	// is half one question and half another.
	h.poll(one("p", "short?"))
	for j := 1; j < QLines; j++ {
		if h.qlines[0][j].title != "" {
			t.Fatalf("line %d = %q, want cleared", j, h.qlines[0][j].title)
		}
	}
}

// ---- notifications: edge-triggered, and content-free

func TestNotificationsAreEdgeTriggered(t *testing.T) {
	h := newHarness()

	// First pass never announces what was already true before the tray started.
	h.poll(&Snapshot{Inbound: 2, Answered: 1, AutoAnswered: 1, Consults: []Consult{{ID: "a", Project: "p"}}})
	if got := h.drain(); len(got) != 0 {
		t.Fatalf("first poll notified %v, want silence", got)
	}

	// A rise notifies.
	h.poll(&Snapshot{Inbound: 3, Answered: 1, AutoAnswered: 1, Consults: []Consult{{ID: "a", Project: "p"}}})
	if got := h.drain(); len(got) != 1 {
		t.Fatalf("a rise notified %v, want exactly one banner", got)
	}

	// THE INVARIANT: an identical repeated poll is silent. A banner every 4s is malware.
	steady := &Snapshot{Inbound: 3, Answered: 1, AutoAnswered: 1, Consults: []Consult{{ID: "a", Project: "p"}}}
	for i := 0; i < 5; i++ {
		h.poll(steady)
		if got := h.drain(); len(got) != 0 {
			t.Fatalf("steady-state poll %d notified %v, want silence", i, got)
		}
	}

	// A FALL is silent too, and re-arms the edge without announcing.
	h.poll(&Snapshot{Inbound: 1, Answered: 1, AutoAnswered: 1, Consults: []Consult{{ID: "a", Project: "p"}}})
	if got := h.drain(); len(got) != 0 {
		t.Fatalf("a falling count notified %v, want silence", got)
	}
	h.poll(&Snapshot{Inbound: 2, Answered: 1, AutoAnswered: 1, Consults: []Consult{{ID: "a", Project: "p"}}})
	if got := h.drain(); len(got) != 1 {
		t.Fatalf("rise after a fall notified %v, want one banner", got)
	}
}

func TestEachCountEdgesIndependently(t *testing.T) {
	h := newHarness()
	h.poll(&Snapshot{}) // arm
	h.drain()

	h.poll(&Snapshot{AutoAnswered: 1, AutoProject: "partyline"})
	got := h.drain()
	if len(got) != 1 || !strings.Contains(got[0], "Answered a teammate's read-only question about partyline") {
		t.Fatalf("auto-answer rise = %v", got)
	}

	h.poll(&Snapshot{AutoAnswered: 1, AutoProject: "partyline", Answered: 2})
	got = h.drain()
	if len(got) != 1 || !strings.Contains(got[0], "A peer answered your question") {
		t.Fatalf("answered rise = %v", got)
	}
}

func TestNotificationBodiesCarryNoQuestionOrAnswerText(t *testing.T) {
	const secret = "why does chargeCard double-post when the terminal reboots"
	h := newHarness()
	h.poll(&Snapshot{}) // arm the edges without notifying

	h.poll(&Snapshot{
		Inbound: 1, Answered: 4, AutoAnswered: 2, AutoProject: "acr-pos",
		Consults: []Consult{{ID: "c1", Project: "acr-pos", Question: secret, WaitingSec: 3}},
	})
	got := h.drain()
	if len(got) == 0 {
		t.Fatal("expected notifications on a rise")
	}
	for _, body := range got {
		// The whole question, and any distinctive word from it, must be absent: bodies land on lock
		// screens and in the system log.
		if strings.Contains(body, secret) {
			t.Fatalf("notification body leaked the question: %q", body)
		}
		for _, word := range []string{"chargeCard", "double-post", "terminal", "reboots"} {
			if strings.Contains(body, word) {
				t.Fatalf("notification body %q leaked question word %q", body, word)
			}
		}
	}
	// It should still be useful: the project name and counts are names, not content.
	if !strings.Contains(got[0], "acr-pos") {
		t.Fatalf("first body should name the project: %q", got[0])
	}
}

func TestQuietSuppressesNotificationsButNotRows(t *testing.T) {
	h := newHarness()
	h.poll(&Snapshot{})
	h.drain()

	h.quiet = true
	h.poll(one("partyline", "is this hot path allocating?"))
	if got := h.drain(); len(got) != 0 {
		t.Fatalf("tray-quiet should suppress banners, got %v", got)
	}
	if !h.rows[0].shown || !h.hdr.shown {
		t.Fatal("tray-quiet silences the banner only — the row must still be there")
	}

	// Unquieting doesn't replay the past: the edge already advanced.
	h.quiet = false
	h.poll(one("partyline", "is this hot path allocating?"))
	if got := h.drain(); len(got) != 0 {
		t.Fatalf("unquieting must not replay a suppressed edge, got %v", got)
	}
}

func TestNilPostIsHarmless(t *testing.T) {
	hdr, more, auto, repl := &fakeItem{}, &fakeItem{}, &fakeItem{}, &fakeItem{}
	row := &Row{Item: &fakeItem{}, QLines: []Item{&fakeItem{}}}
	s := NewSection(Config{Hdr: hdr, More: more, Auto: auto, Repl: repl, Rows: []*Row{row}})
	s.Poll(true, &Snapshot{})
	s.Poll(true, one("p", "q")) // would panic if post dereferenced a nil func
}

// ---- degrading against an older ptln

func TestOlderCLIWithNoPeerFieldsDegradesSilently(t *testing.T) {
	// The "peers" key is omitted entirely when there's nothing to say, so a `ptln` that predates the
	// feature is indistinguishable from a quiet one — and unmarshals to nil.
	var m struct {
		Waiting int       `json:"waiting"`
		Peers   *Snapshot `json:"peers"`
	}
	if err := json.Unmarshal([]byte(`{"waiting":1}`), &m); err != nil {
		t.Fatal(err)
	}
	if m.Peers != nil {
		t.Fatal("a state payload with no peers key must unmarshal to nil")
	}
	if m.Peers.Blocked() != 0 {
		t.Fatal("Blocked() must be nil-safe")
	}

	h := newHarness()
	h.poll(nil)
	for i, r := range h.rows {
		if r.shown {
			t.Fatalf("row %d visible against a peer-less CLI", i)
		}
	}
	for _, it := range []*fakeItem{h.hdr, h.more, h.auto, h.repl} {
		if it.shown {
			t.Fatal("the section must be entirely hidden against a peer-less CLI")
		}
	}
	if got := h.drain(); len(got) != 0 {
		t.Fatalf("a peer-less CLI must be silent, got %v", got)
	}
	// And it must not have poisoned the edges: a later upgrade that reports one question notifies once.
	h.poll(one("p", "q"))
	if got := h.drain(); len(got) != 1 {
		t.Fatalf("after a nil snapshot, a first real question should notify once, got %v", got)
	}
}

// TestFailedReadKeepsEdges is the readTooOld path: `ptln state` failed, so we do NOT know what's
// waiting. Blanking the rows is right; recording zero is not, because the next successful read would
// re-announce every question that was already there.
func TestFailedReadBlanksRowsWithoutReAnnouncing(t *testing.T) {
	h := newHarness()
	h.poll(&Snapshot{})
	h.poll(&Snapshot{Inbound: 2, Consults: []Consult{{ID: "a", Project: "p"}, {ID: "b", Project: "p"}}})
	if got := h.drain(); len(got) != 1 {
		t.Fatalf("setup: want one banner, got %v", got)
	}

	h.sec.Poll(false, nil) // the CLI went missing for a tick
	if h.hdr.shown || h.rows[0].shown {
		t.Fatal("a failed read must blank the section")
	}
	if got := h.drain(); len(got) != 0 {
		t.Fatalf("a failed read must be silent, got %v", got)
	}

	// It came back, reporting exactly what it reported before. Silence.
	h.poll(&Snapshot{Inbound: 2, Consults: []Consult{{ID: "a", Project: "p"}, {ID: "b", Project: "p"}}})
	if got := h.drain(); len(got) != 0 {
		t.Fatalf("the same questions must not be re-announced after a failed read, got %v", got)
	}
	if !h.hdr.shown || !h.rows[0].shown {
		t.Fatal("the section must repaint once the read succeeds again")
	}
}

// ---- badge

func TestBlockedCountsOnlyQuestionsWaitingOnYou(t *testing.T) {
	if got := (&Snapshot{Inbound: 2, Answered: 5, AutoAnswered: 9}).Blocked(); got != 2 {
		t.Fatalf("Blocked() = %d, want 2 — FYIs must not pad the badge", got)
	}
}

// ---- truncation

func TestWrapAlwaysReturnsExactlyNLines(t *testing.T) {
	for _, q := range []string{"", "short", strings.Repeat("word ", 200)} {
		if got := Wrap(q, QWidth, QLines); len(got) != QLines {
			t.Fatalf("Wrap(%q) returned %d lines, want %d", q, len(got), QLines)
		}
	}
}

func TestWrapEmptyQuestionSaysSo(t *testing.T) {
	got := Wrap("   ", QWidth, QLines)
	if !strings.Contains(got[0], "(empty question)") {
		t.Fatalf("empty question rendered as %q", got[0])
	}
}

func TestWrapBoundaries(t *testing.T) {
	// Exactly the width: fits on one line, untouched, no ellipsis.
	line := strings.Repeat("x", QWidth)
	got := Wrap(line, QWidth, QLines)
	if strings.TrimPrefix(got[0], Indent) != line {
		t.Fatalf("a question of exactly the width should be untouched, got %q", got[0])
	}
	if strings.Contains(got[0], "…") {
		t.Fatalf("no truncation expected at exactly the width, got %q", got[0])
	}

	// One rune over: it breaks onto a second line rather than overflowing.
	got = Wrap(strings.Repeat("x", QWidth+1), QWidth, QLines)
	for i, l := range got {
		if n := utf8.RuneCountInString(strings.TrimPrefix(l, Indent)); n > QWidth {
			t.Fatalf("line %d is %d runes wide, over the %d limit: %q", i, n, QWidth, l)
		}
	}
	if strings.TrimPrefix(got[1], Indent) == "" {
		t.Fatal("the overflowing rune should land on line 2")
	}

	// Everything that fits in n lines is kept; nothing is silently dropped without a marker.
	long := strings.Repeat("alpha bravo charlie delta ", 20)
	got = Wrap(long, QWidth, QLines)
	if !strings.HasSuffix(got[QLines-1], "…") {
		t.Fatalf("a truncated question must be marked, last line = %q", got[QLines-1])
	}
	for i, l := range got {
		if n := utf8.RuneCountInString(strings.TrimPrefix(l, Indent)); n > QWidth {
			t.Fatalf("line %d is %d runes wide, over the %d limit: %q", i, n, QWidth, l)
		}
	}
}

func TestWrapNeverSplitsARune(t *testing.T) {
	cases := []string{
		strings.Repeat("これはとても長い日本語の質問です", 20), // no spaces at all: forces a hard break
		strings.Repeat("naïve café — résumé ", 30),
		strings.Repeat("🙂🙃", 200),
	}
	for _, q := range cases {
		got := Wrap(q, QWidth, QLines)
		for i, l := range got {
			if !utf8.ValidString(l) {
				t.Fatalf("case %.12q line %d is not valid UTF-8: %q", q, i, l)
			}
			if strings.ContainsRune(l, '�') {
				t.Fatalf("case %.12q line %d contains a replacement char (a rune was split): %q", q, i, l)
			}
			if n := utf8.RuneCountInString(strings.TrimPrefix(l, Indent)); n > QWidth {
				t.Fatalf("case %.12q line %d is %d runes wide, over %d", q, i, n, QWidth)
			}
		}
	}
}

func TestWrapCountsWidthInRunesNotBytes(t *testing.T) {
	// 20 two-byte runes = 40 bytes but only 20 columns; separated by spaces they must share one line.
	q := strings.Repeat("é ", 10) // 10 words, 19 runes with separators
	got := Wrap(strings.TrimSpace(q), QWidth, QLines)
	if got[1] != "" {
		t.Fatalf("19 runes should fit on one %d-wide line, spilled to %q", QWidth, got[1])
	}
}

func TestWrapDegenerateArgs(t *testing.T) {
	if got := Wrap("anything", 0, QLines); len(got) != QLines || got[0] != "" {
		t.Fatalf("zero width should produce blank lines, got %q", got)
	}
	if got := Wrap("anything", QWidth, 0); len(got) != 0 {
		t.Fatalf("zero lines should produce no lines, got %q", got)
	}
}

func TestShortWaitAndPluralize(t *testing.T) {
	for _, c := range []struct {
		sec  int
		want string
	}{{0, "0s"}, {59, "59s"}, {60, "1m"}, {61, "1m"}, {3600, "60m"}} {
		if got := ShortWait(c.sec); got != c.want {
			t.Fatalf("ShortWait(%d) = %q, want %q", c.sec, got, c.want)
		}
	}
	if got := Pluralize(1, "reply waiting", "replies waiting"); got != "1 reply waiting" {
		t.Fatalf("Pluralize(1) = %q", got)
	}
	if got := Pluralize(0, "reply waiting", "replies waiting"); got != "0 replies waiting" {
		t.Fatalf("Pluralize(0) = %q", got)
	}
}
