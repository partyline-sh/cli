package ptymux

import "testing"

func TestContainsTerminalQuery(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"dsr cursor position", "\x1b[6n", true},
		{"dsr status", "\x1b[5n", true},
		{"device attributes", "\x1b[c", true},
		{"secondary DA", "\x1b[>c", true},
		{"window title report request", "\x1b[21t", true},
		{"window size request", "\x1b[18t", true},
		{"decrqm mode query", "\x1b[?2026$p", true},
		{"osc bg colour query", "\x1b]11;?\x07", true},
		{"osc palette query", "\x1b]4;1;?\x07", true},
		{"query embedded in a render burst", "hello\x1b[32mworld\x1b[6n\x1b[0m", true},

		{"plain text", "just some output", false},
		{"sgr colour (render, not a query)", "\x1b[1;32mgreen\x1b[0m", false},
		{"cursor move (render)", "\x1b[10;5H", false},
		{"erase line (render)", "\x1b[2K", false},
		{"soft reset is not a query", "\x1b[!p", false},
		{"decscl is not a query", "\x1b[61\"p", false},
		{"osc set title (no reply expected)", "\x1b]0;my title\x07", false},
	}
	for _, c := range cases {
		if got := containsTerminalQuery([]byte(c.in)); got != c.want {
			t.Errorf("%s: containsTerminalQuery(%q) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}

func TestMatchTerminalReport(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int // matched length; 0 = not a report
	}{
		{"dsr cursor reply", "\x1b[24;80R", len("\x1b[24;80R")},
		{"da reply", "\x1b[?1;2c", len("\x1b[?1;2c")},
		{"secondary da reply", "\x1b[>0;95;0c", len("\x1b[>0;95;0c")},
		{"window position report", "\x1b[3;0;0t", len("\x1b[3;0;0t")},
		{"decrpm reply", "\x1b[?2026;1$y", len("\x1b[?2026;1$y")},
		{"osc title report (BEL)", "\x1b]l local-llm-multiplexer\x07", len("\x1b]l local-llm-multiplexer\x07")},
		{"osc colour report (ST)", "\x1b]11;rgb:1c1c/1c1c/1c1c\x1b\\", len("\x1b]11;rgb:1c1c/1c1c/1c1c\x1b\\")},

		{"up arrow is NOT a report", "\x1b[A", 0},
		{"down arrow", "\x1b[B", 0},
		{"modified arrow", "\x1b[1;5C", 0},
		{"home key", "\x1b[H", 0},
		{"function key (tilde)", "\x1b[15~", 0},
		{"csi-u key", "\x1b[97;5u", 0},
		{"sgr mouse press", "\x1b[<0;10;20M", 0},
		{"sgr mouse release", "\x1b[<0;10;20m", 0},
		{"plain byte", "a", 0},
		{"bare esc", "\x1b", 0},
		{"incomplete csi at boundary", "\x1b[24;80", 0},
		{"incomplete osc at boundary", "\x1b]11;rgb:1c", 0},
	}
	for _, c := range cases {
		if got := matchTerminalReport([]byte(c.in)); got != c.want {
			t.Errorf("%s: matchTerminalReport(%q) = %d, want %d", c.name, c.in, got, c.want)
		}
	}
}

// TestMatchTerminalReport_OnlyHead confirms a report is matched only at the head of the
// buffer (the caller scans byte-by-byte and calls this when it sees an ESC), and that a
// trailing report after real input is left for the next scan step — so ordinary keystrokes
// preceding a report are never mis-consumed.
func TestMatchTerminalReport_OnlyHead(t *testing.T) {
	// "ab" then a DSR reply: at index 0 ('a') no match; the caller forwards a,b and only
	// matches the report once it reaches the ESC.
	buf := []byte("ab\x1b[24;80R")
	if n := matchTerminalReport(buf); n != 0 {
		t.Fatalf("expected no match at head with leading text, got %d", n)
	}
	if n := matchTerminalReport(buf[2:]); n != len("\x1b[24;80R") {
		t.Fatalf("expected report match once positioned at ESC, got %d", n)
	}
}

// TestQueryOwnerRouting pins the core invariant: the child whose output issued a query owns
// the reply, and takeQueryOwner hands it over exactly once — so a reply arriving after the
// user switched tabs goes back to the querier, not the now-active child.
func TestQueryOwnerRouting(t *testing.T) {
	mx := &Mux{}
	a, b := &child{label: "A"}, &child{label: "B"}
	ga := &gate{out: &mx.outMu, mx: mx, ch: a, active: true}

	// A (active) queries the terminal → A becomes the reply owner.
	_, _ = ga.Write([]byte("\x1b[6n"))
	if got := mx.takeQueryOwner(); got != a {
		t.Fatalf("owner after A's query = %v, want A", got)
	}
	// Taken exactly once.
	if got := mx.takeQueryOwner(); got != nil {
		t.Fatalf("owner should be cleared after take, got %v", got)
	}

	// A backgrounded child's query never reaches the terminal, so it must NOT claim ownership.
	gbBackground := &gate{out: &mx.outMu, mx: mx, ch: b, active: false}
	_, _ = gbBackground.Write([]byte("\x1b[6n"))
	if got := mx.takeQueryOwner(); got != nil {
		t.Fatalf("backgrounded child must not become owner, got %v", got)
	}

	// clearQueryOwner drops a dead owner but leaves an unrelated one alone.
	mx.noteQuery(a)
	mx.clearQueryOwner(b)
	if mx.takeQueryOwner() != a {
		t.Fatalf("clearing a different child must not drop A's ownership")
	}
	mx.noteQuery(a)
	mx.clearQueryOwner(a)
	if mx.takeQueryOwner() != nil {
		t.Fatalf("clearing the owner must drop it")
	}
}
