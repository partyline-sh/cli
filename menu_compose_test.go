package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// THE REGRESSION THIS FILE PINS. cgAsk treats '\r' as SUBMIT, so pasting a multi-line excerpt into the
// peer-question field sent the FIRST LINE and silently discarded the rest. Everything here exists to
// make that impossible in cgCompose: a paste is text, a paste arrives in pieces, and a newline in it is
// a newline.

// ---- the paste assembler, driven deterministically -------------------------

// chunkReader is cgRaw's read() with a scripted sequence of reads, so a "paste split across four
// reads" is exact rather than a matter of timing.
func chunkReader(chunks ...string) func() ([]byte, bool) {
	i := 0
	return func() ([]byte, bool) {
		if i >= len(chunks) {
			return nil, false
		}
		i++
		return []byte(chunks[i-1]), true
	}
}

// A paste that arrives across several reads is assembled INTACT — every byte, every newline, in order.
func TestPasteSplitAcrossReadsIsAssembledIntact(t *testing.T) {
	body := "first line\nsecond line\n\n  indented tail with a tab\there"
	// The introducer lands with the head of the text (cgRaw hands an escape-prefixed chunk over whole),
	// the middle arrives naked, and the terminator comes with the last piece.
	head, mid, tail := body[:11], body[11:30], body[30:]
	first, ok := cgPasteBegins([]byte(cgPasteStart + head))
	if !ok {
		t.Fatal("cgPasteBegins did not recognise the introducer")
	}
	got, rest, complete := cgReadPaste(first, chunkReader(mid, tail+cgPasteEnd), time.Now)
	if !complete {
		t.Error("the terminator was present, so the paste is complete")
	}
	if got != body {
		t.Errorf("assembled %q, want %q", got, body)
	}
	if len(rest) != 0 {
		t.Errorf("nothing followed the terminator, but rest = %q", rest)
	}
}

// Bytes AFTER the terminator are keystrokes, not text — dropping them would eat a keypress (the
// ctrl-d that sends, for instance, if the terminal batched it with the paste end).
func TestPasteHandsBackWhatFollowedTheTerminator(t *testing.T) {
	got, rest, complete := cgReadPaste([]byte("hello"+cgPasteEnd+"\x04"), chunkReader(), time.Now)
	if got != "hello" || !complete {
		t.Fatalf("got (%q, %v)", got, complete)
	}
	if string(rest) != "\x04" {
		t.Errorf("rest = %q, want the ctrl-d", rest)
	}
}

// A paste with NO terminator cannot wedge the field. Two bounds do it: the byte cap, and the wall clock
// (checked when a read returns — so the next key the human presses closes the runaway paste and is then
// handled as a key).
func TestAPasteWithNoTerminatorIsBounded(t *testing.T) {
	// Bound 1 — the cap. A reader that keeps producing bytes forever must still return.
	forever := func() ([]byte, bool) { return []byte(strings.Repeat("x", 4096)), true }
	got, _, complete := cgReadPaste(nil, forever, time.Now)
	if complete {
		t.Error("there was no terminator, so the paste is not complete")
	}
	if len(got) != cgPasteCap {
		t.Errorf("assembled %d bytes, want the cap %d", len(got), cgPasteCap)
	}

	// Bound 2 — the idle gap. A read that sat blocked for a second means the burst is over and the
	// terminator isn't coming, so the paste ends and the bytes that woke it come back as keystrokes.
	clock := time.Now()
	now := func() time.Time { return clock }
	reads := 0
	late := func() ([]byte, bool) {
		reads++
		if reads == 1 {
			return []byte("early"), true
		}
		clock = clock.Add(2 * cgPasteGap) // this read blocked: the human finally pressed a key
		return []byte("\x04"), true
	}
	got, rest, complete := cgReadPaste([]byte("head "), late, now)
	if complete {
		t.Error("no terminator ⇒ not complete")
	}
	if got != "head early" {
		t.Errorf("kept %q, want the bytes that did arrive in the burst", got)
	}
	if string(rest) != "\x04" {
		t.Errorf("rest = %q — the key that ended the runaway paste must be handled as a key", rest)
	}

	// Bound 3 — the terminal went away mid-paste.
	got, _, complete = cgReadPaste([]byte("partial"), chunkReader(), time.Now)
	if complete || got != "partial" {
		t.Errorf("EOF mid-paste = (%q, %v), want (\"partial\", false)", got, complete)
	}
}

// ---- the field, over a real pty --------------------------------------------

// paste wraps text in the bracketed-paste markers the terminal would.
func paste(s string) string { return cgPasteStart + s + cgPasteEnd }

// A PASTE CONTAINING NEWLINES DOES NOT SUBMIT. This is the bug. The field must still be open after it,
// and the newlines must still be in the text.
func TestAPasteWithNewlinesDoesNotSubmit(t *testing.T) {
	excerpt := "The retry policy is the problem:\n\n- 429s are retried forever\n- 500s are not retried at all\n\nDoes that break your callers?"
	var got string
	var ok bool
	// No ctrl-d anywhere: the paste alone must not send. esc is what ends the test, and it abandons.
	cgDrive(t, paste(excerpt)+"\x1b", func() { got, ok = cgCompose("Ask air", nil, "") })
	if ok {
		t.Fatalf("a paste with newlines SUBMITTED %q — that is the whole bug", got)
	}
	// …and with a ctrl-d after it, the WHOLE excerpt comes back, newlines intact.
	cgDrive(t, paste(excerpt)+"\x04", func() { got, ok = cgCompose("Ask air", nil, "") })
	if !ok {
		t.Fatal("ctrl-d after a paste must send")
	}
	if got != excerpt {
		t.Errorf("sent %q,\nwant %q", got, excerpt)
	}
}

// The same thing with the paste split across writes, which is what a multi-KB clipboard actually does.
func TestAChunkedPasteArrivesWholeInTheField(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 60; i++ {
		fmt.Fprintf(&b, "line %d of a long discussion excerpt that has to survive the trip\n", i)
	}
	excerpt := strings.TrimRight(b.String(), "\n")
	chunks := []string{cgPasteStart + excerpt[:200], excerpt[200:900], excerpt[900:], cgPasteEnd, "\x04"}

	var got string
	var ok bool
	cgDriveChunks(t, chunks, func() { got, ok = cgCompose("Ask air", nil, "") })
	if !ok {
		t.Fatal("the field abandoned instead of sending")
	}
	if got != excerpt {
		t.Errorf("assembled %d chars, want %d\nfirst difference around: %q",
			len(got), len(excerpt), firstDiff(got, excerpt))
	}
}

func firstDiff(a, b string) string {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			lo := max(0, i-20)
			return a[lo:min(len(a), i+20)] + " ≠ " + b[lo:min(len(b), i+20)]
		}
	}
	return "(one is a prefix of the other)"
}

// Enter INSERTS, ctrl-d SENDS, esc ABANDONS — the contract in the hint bar, over a real tty.
func TestComposeKeyContract(t *testing.T) {
	var got string
	var ok bool
	paint := cgDrive(t, "one\rtwo\rthree\x04", func() { got, ok = cgCompose("Ask air", nil, "") })
	if !ok || got != "one\ntwo\nthree" {
		t.Fatalf("cgCompose = (%q, %v), want (\"one\\ntwo\\nthree\", true)", got, ok)
	}
	if !strings.Contains(cgPlain(paint), "two") {
		t.Error("typed text must be echoed INSIDE the frame")
	}
	if !strings.Contains(cgPlain(paint), "ctrl-d") || !strings.Contains(cgPlain(paint), "$EDITOR") {
		t.Error("the send key and the editor key must be in the hint bar — an unguessable key doesn't exist")
	}
	for _, keys := range []string{"\x1b", "\x03", "\x1c", "typed then esc\x1b"} {
		cgDrive(t, keys, func() { got, ok = cgCompose("Ask air", nil, "") })
		if ok {
			t.Errorf("keys %q returned %q instead of abandoning", keys, got)
		}
	}
	// ctrl-u clears, backspace removes one rune (including a newline), and an empty field won't send.
	cgDrive(t, "gone\x15kept\x04", func() { got, ok = cgCompose("Ask air", nil, "") })
	if !ok || got != "kept" {
		t.Errorf("ctrl-u then text = (%q, %v)", got, ok)
	}
	cgDrive(t, "ab\r\x7f\x7fc\x04", func() { got, ok = cgCompose("Ask air", nil, "") })
	if !ok || got != "ac" {
		t.Errorf("backspace over a newline = (%q, %v), want (\"ac\", true)", got, ok)
	}
	cgDrive(t, "\x04\x1b", func() { got, ok = cgCompose("Ask air", nil, "") })
	if ok {
		t.Errorf("ctrl-d on an empty field sent %q", got)
	}
}

// OVER THE LIMIT, LOCALLY, WITH THE NUMBERS. ctrl-d must refuse and say what to cut — the server's 400
// says none of this, and arrives after you've composed the thing.
func TestComposeRefusesAnOverLongQuestionLocally(t *testing.T) {
	over := strings.Repeat("x", maxQuestionChars+37)
	note := questionTooLongNote(over)
	for _, want := range []string{commaNum(maxQuestionChars + 37), commaNum(maxQuestionChars), "37"} {
		if !strings.Contains(note, want) {
			t.Errorf("the over-limit note must name %s: %q", want, note)
		}
	}
	if questionTooLongNote(strings.Repeat("x", maxQuestionChars)) != "" {
		t.Error("exactly at the limit is not over it")
	}
	// The field refuses to send it, stays open, and paints the note.
	//
	// Fed in pieces on purpose. A single 32 KB write into a pty master outruns the tty's input queue and
	// the tail is DISCARDED at the terminal layer — the harness's limit, not the field's, but it would eat
	// the trailing keys and read as a hang. Chunks are also what a real clipboard does.
	var got string
	var ok bool
	chunks := []string{cgPasteStart}
	for i := 0; i < len(over); i += 4000 {
		chunks = append(chunks, over[i:min(i+4000, len(over))])
	}
	chunks = append(chunks, cgPasteEnd, "\x04", "\x1b")
	paint := cgDriveChunks(t, chunks, func() { got, ok = cgCompose("Ask air", nil, "") })
	if ok {
		t.Fatalf("ctrl-d sent %d over-limit characters", len(got))
	}
	if !strings.Contains(cgPlain(paint), commaNum(maxQuestionChars)) {
		t.Error("the field must show the limit it is refusing against")
	}
}

// ---- layout: the frame contains it, at every size --------------------------

// A long multi-line question SCROLLS INSIDE the box. Nothing lands outside the border, and the frame
// never grows past the terminal — a taller-than-terminal frame scrolls the screen and tears the box.
func TestComposeFrameHoldsALongMultiLineQuestion(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&b, "paragraph %d — %s\n\n", i, strings.Repeat("words that wrap ", 6))
	}
	long := b.String()
	body := []string{dim("about partyline — one message, sent to their agent"), dim("read-only and untrusted")}

	for _, size := range [][2]int{{200, 50}, {120, 40}, {80, 24}, {50, 14}, {40, 12}} {
		cols, rows := size[0], size[1]
		for _, text := range []string{"", "one line", "a\nb\nc", long} {
			lines, prompt := cgComposeLines(body, text, "", cols-8, rows)
			m := cgModal{Title: "Ask air", Body: lines, Prompt: prompt, Hints: cgComposeHints()}
			all, cl, cv, _ := m.lines(rows)
			if len(all)+2 > rows {
				t.Errorf("%dx%d (%d chars): frame is %d rows tall", cols, rows, len(text), len(all)+2)
			}
			if cl < 0 {
				t.Errorf("%dx%d: the compose field must place the cursor on its last line", cols, rows)
			}
			for _, bad := range cgSpills(t, cgPaintLines(all, cl, cv, cols, rows), rows) {
				t.Errorf("%dx%d (%d chars): %s", cols, rows, len(text), bad)
			}
		}
	}
	// The window keeps the END of the text (where the cursor is) and says how much is above it.
	lines, prompt := cgComposeLines(body, long, "", 72, 40)
	if !strings.Contains(cgPlain(strings.Join(lines, "\n")), "more line(s)") {
		t.Error("a scrolled region must say how much it is holding back")
	}
	// The window holds the END of the text — where the cursor is — not the beginning.
	shown := cgPlain(strings.Join(append(lines, prompt), "\n"))
	if !strings.Contains(shown, "paragraph 399") {
		t.Errorf("the region must show the tail of the text:\n%s", shown)
	}
	if strings.Contains(shown, "paragraph 0 ") {
		t.Error("the head of a 400-paragraph question is scrolled off, but was painted")
	}
}

// The live count is on screen with its limit, because a count without its bound says nothing.
func TestComposeShowsALiveCountAndTheLimit(t *testing.T) {
	lines, _ := cgComposeLines(nil, strings.Repeat("x", 1234), "", 72, 40)
	plain := cgPlain(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "1,234") || !strings.Contains(plain, commaNum(maxQuestionChars)) {
		t.Errorf("want the count and the limit:\n%s", plain)
	}
	over := cgPlain(cgComposeCount(strings.Repeat("x", maxQuestionChars+5)))
	if !strings.Contains(over, "5 over") {
		t.Errorf("over the limit the count must say by how much: %q", over)
	}
}

// Tabs and CRLF: the buffer keeps the text as pasted apart from newline folding, which it has to do —
// a bare CR paints over the frame's left border.
func TestComposeNormalisesOnlyNewlines(t *testing.T) {
	if got := cgNormalizeNewlines("a\r\nb\rc\nd"); got != "a\nb\nc\nd" {
		t.Errorf("cgNormalizeNewlines = %q", got)
	}
	if got := cgPrintable([]byte("ok\x07\x00fine")); got != "okfine" {
		t.Errorf("control bytes must never enter the buffer: %q", got)
	}
	// A tab survives in the text but is expanded for LAYOUT, so the cursor column is honest.
	if w := cgWrapText("a\tb", 40); len(w) != 1 || w[0] != "a    b" {
		t.Errorf("tab expansion for display = %q", w)
	}
}

// $EDITOR unset is SAID, not silently ignored, and it never costs you the text you composed.
func TestComposeSaysWhenEditorIsUnset(t *testing.T) {
	t.Setenv("EDITOR", "")
	got, note := cgEditorText("my question")
	if got != "my question" {
		t.Errorf("the text must survive: %q", got)
	}
	if !strings.Contains(note, "$EDITOR") {
		t.Errorf("an unset $EDITOR must be reported: %q", note)
	}
}

// $EDITOR round trip: whatever the child leaves in the file is what comes back.
func TestComposeReadsBackWhatEditorWrote(t *testing.T) {
	t.Setenv("EDITOR", `printf 'edited\nby the editor\n' >`)
	got, note := cgEditorText("before")
	if note != "" {
		t.Fatalf("note = %q", note)
	}
	if got != "edited\nby the editor" {
		t.Errorf("read back %q", got)
	}
}
