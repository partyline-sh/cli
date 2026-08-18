package ptymux

import (
	"encoding/base64"
	"strings"
	"testing"
)

// The reported bug: selecting a screenful of text made the terminal answer with an OSC 52 clipboard
// report, which is far too big for one stdin read — so its first chunk fell through handleInput and
// was typed into the child's composer as a wall of base64.
func TestPendingReportTailHoldsASplitClipboardReport(t *testing.T) {
	selection := strings.Repeat("the quick brown fox jumps over the lazy dog. ", 200)
	report := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(selection)) + "\x07"
	if len(report) < 4096 {
		t.Fatalf("test payload too small to model a split read (%d bytes)", len(report))
	}

	// A PTY read is bounded; the report arrives in pieces. Feed it 4096 bytes at a time, exactly as
	// the input loop would see it, and assert nothing escapes to the child until it completes.
	var held []byte
	var leaked []byte
	for off := 0; off < len(report); off += 4096 {
		end := off + 4096
		if end > len(report) {
			end = len(report)
		}
		buf := append(append([]byte(nil), held...), report[off:end]...)
		if n := pendingReportTail(buf); n > 0 {
			held = append([]byte(nil), buf[len(buf)-n:]...)
			buf = buf[:len(buf)-n]
		} else {
			held = nil
		}
		leaked = append(leaked, buf...) // whatever the loop would have processed this round
	}

	// The completed report should surface exactly once, whole, at the end — and nothing before it.
	if len(held) != 0 {
		t.Errorf("report never completed: %d bytes still held", len(held))
	}
	if string(leaked) != report {
		t.Errorf("report did not arrive intact:\n got %d bytes\nwant %d bytes", len(leaked), len(report))
	}
	if n := matchTerminalReport(leaked); n != len(report) {
		t.Errorf("assembled bytes are not a single complete report: matched %d of %d", n, len(report))
	}
}

func TestPendingReportTail(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"an unterminated OSC is held whole", "\x1b]52;c;QUJD", 11},
		{"a BEL-terminated OSC is complete", "\x1b]52;c;QUJD\x07", 0},
		{"an ST-terminated OSC is complete", "\x1b]52;c;QUJD\x1b\\", 0},
		{"an unterminated DCS is held", "\x1bP>|iTerm2 3.5", 14},
		{"a terminated DCS is complete", "\x1bP>|iTerm2\x1b\\", 0},
		{"typed text is never held", "hello world", 0},
		{"a bare trailing ESC is the Escape key, not a report", "\x1b", 0},
		{"Escape after real input is still not held", "abc\x1b", 0},
		{"an arrow key is not held", "\x1b[A", 0},
		{"a complete CSI reply is not held", "\x1b[24;80R", 0},
		{"text before an unterminated OSC holds only the OSC", "hi\x1b]11;rgb:", 9},
		{"a complete OSC followed by an incomplete one holds the second", "\x1b]11;x\x07\x1b]52;c;AA", 9},
		{"an incomplete CSI is not held (short, and could be a key)", "\x1b[24;", 0},
	}
	for _, c := range cases {
		if got := pendingReportTail([]byte(c.in)); got != c.want {
			t.Errorf("%s:\n  in   %q\n  got  %d\n  want %d", c.name, c.in, got, c.want)
		}
	}
}

// A report that never terminates must not buffer without bound — past the cap we give up and let
// the bytes through, which is the old behaviour but bounded.
func TestPendingReportTailGivesUpPastTheCap(t *testing.T) {
	runaway := "\x1b]52;c;" + strings.Repeat("A", maxHeldReport+10)
	if got := pendingReportTail([]byte(runaway)); got != 0 {
		t.Errorf("a runaway report should stop being held, got %d", got)
	}
}
