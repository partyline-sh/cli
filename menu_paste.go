package main

import (
	"bytes"
	"os"
	"time"
)

// BRACKETED PASTE, READ AS TEXT.
//
// THE BUG THIS EXISTS TO KILL. cgAsk reads keys in raw mode and treats '\r' and '\n' as SUBMIT. Paste
// a multi-line excerpt from an LLM discussion into it and the field submits at the FIRST newline and
// silently discards everything after it — you send one line and are never told. The single-line
// screens can live with that (a worktree name has no newlines); a question field cannot, because
// large pasted text is exactly what it is for.
//
// The fix is to stop guessing. DECSET 2004 makes the terminal wrap a paste in \x1b[200~ … \x1b[201~,
// so the field can know that what arrived is TEXT rather than a sequence of keypresses, and take it
// verbatim — newlines, tabs, ctrl characters and all. Nothing between the markers is ever dispatched
// as a key.
//
// A PASTE ARRIVES IN PIECES. That is the whole point: a multi-KB clipboard does not fit in one read
// (cgRaw reads 64 bytes at a time, and the tty itself chunks at its own buffer size), so the
// terminator routinely lands several reads later. So this accumulates until it sees \x1b[201~ —
// bounded three ways, because an accumulator that only ends on a byte pattern is an accumulator a
// missing terminator can wedge:
//
//  1. cgPasteCap bytes. A terminal that never sends the terminator cannot make us grow without limit.
//  2. cgPasteGap — the IDLE time one read spent blocked. A paste is a continuous burst, so mid-paste
//     gaps are microseconds; a gap of a second means the burst ended without a terminator, and the bytes
//     that finally arrived are keystrokes, not paste. Deliberately NOT a total-duration budget: a total
//     budget makes the field's correctness depend on how big your clipboard is, which is precisely the
//     class of bug this file exists to remove.
//  3. A read that fails or reports EOF (the terminal went away) ends it with what we have.
//
// (3) is why this can't hang forever even with (2) unable to fire: the only way to block is inside
// read(), which is exactly where a modal always blocks waiting for a keypress. The next key the human
// presses returns from that read, trips the idle bound, and closes the runaway paste — the field is
// responsive again, with the text it did receive intact.

const (
	cgPasteOn  = "\x1b[?2004h" // DECSET 2004 — ask the terminal to bracket pastes
	cgPasteOff = "\x1b[?2004l"

	cgPasteStart = "\x1b[200~"
	cgPasteEnd   = "\x1b[201~"

	// Generous next to any question bound (maxQuestionChars) so the field can hold an over-limit paste
	// and TELL you it's over, rather than truncating it behind your back — but finite, so a terminal
	// that never terminates the paste can't grow the buffer without limit.
	cgPasteCap = 1 << 20 // 1 MiB

	// The longest a single read may block while a paste is still open. A terminal delivers a paste as fast
	// as we drain it, so a real mid-paste gap is microseconds — a whole second of silence means the
	// terminator is not coming, and whatever arrives next is a keypress.
	cgPasteGap = time.Second
)

// cgPasteMode enables/disables bracketed paste around a field. Paired with a defer, always: leaving
// 2004 set would make every later paste in the session arrive wrapped in markers the next reader
// doesn't understand.
func cgPasteMode(on bool) {
	if on {
		os.Stdout.WriteString(cgPasteOn)
		return
	}
	os.Stdout.WriteString(cgPasteOff)
}

// cgPasteBegins reports the bytes AFTER the \x1b[200~ introducer when a keystroke chunk starts one.
// cgRaw hands an escape-prefixed chunk over whole (its "an escape sequence is one keystroke" rule), so
// the first read of a paste normally carries the introducer AND the start of the text.
func cgPasteBegins(b []byte) ([]byte, bool) {
	if !bytes.HasPrefix(b, []byte(cgPasteStart)) {
		return nil, false
	}
	return b[len(cgPasteStart):], true
}

// cgReadPaste assembles one bracketed paste. `first` is whatever followed the introducer in the read
// that began it; read is cgRaw's one-keystroke reader, which mid-paste simply yields the next bytes.
//
// Returns the pasted TEXT, any bytes that followed the terminator (or that ended a runaway paste —
// the caller must process these as keystrokes, or a keypress would be eaten), and whether the
// terminator was actually seen. complete=false means a bound closed it, which is worth saying on
// screen: the text is real but may be short.
func cgReadPaste(first []byte, read func() ([]byte, bool), now func() time.Time) (text string, rest []byte, complete bool) {
	buf := append([]byte(nil), first...)
	// cgRaw hands back ONE keystroke per call, which mid-paste is one byte, so this loop runs once per
	// pasted byte. Re-scanning the whole buffer each time would be O(n²) — at 32,000 characters that is
	// a billion comparisons and the field visibly hangs. So the scan only ever looks at what is NEW plus
	// the few bytes a split terminator could straddle.
	scanned := 0
	for {
		from := max(0, scanned-(len(cgPasteEnd)-1))
		if i := bytes.Index(buf[from:], []byte(cgPasteEnd)); i >= 0 {
			at := from + i
			return string(buf[:at]), buf[at+len(cgPasteEnd):], true
		}
		scanned = len(buf)
		if len(buf) >= cgPasteCap {
			return string(buf[:cgPasteCap]), nil, false // bound 1: never grow without limit
		}
		before := now()
		b, got := read()
		if !got {
			return string(buf), nil, false // bound 3: the terminal went away
		}
		if now().Sub(before) > cgPasteGap {
			// Bound 2: that read sat idle too long, so the burst is over and the terminator isn't coming.
			// End the paste and give the bytes back as keys — the human pressing a key is what gets a field
			// out of a terminator-less paste.
			return string(buf), b, false
		}
		buf = append(buf, b...)
	}
}
