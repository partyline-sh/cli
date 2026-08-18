package ptymux

import "testing"

// Captured from a real Claude Code startup under a PTY: it probes the terminal with exactly two
// queries, and only one of them used to be recognised.
//
//	ESC[c    DA1       — "what are your capabilities?"   (was recognised)
//	ESC[>0q  XTVERSION — "which emulator are you?"       (was NOT)
//
// XTVERSION is how a TUI identifies the emulator, which is what gates emulator-specific features
// like inline images. Two separate gaps made its reply unusable:
//
//  1. containsTerminalQuery didn't match the query, so no owner was registered to receive a reply.
//  2. matchTerminalReport handled CSI and OSC but not DCS — and XTVERSION answers with a DCS.
//
// Together that meant the answer was not recognised as a report at all, so it was forwarded to the
// child as input: asking the terminal what it is got its name typed into the prompt.

func TestClaudesTwoStartupProbesAreBothRecognised(t *testing.T) {
	for _, q := range []struct{ name, seq string }{
		{"DA1", "\x1b[c"},
		{"XTVERSION", "\x1b[>0q"},
		{"XTVERSION (no param)", "\x1b[>q"},
	} {
		if !containsTerminalQuery([]byte(q.seq)) {
			t.Errorf("%s (%q) not recognised as a query — its reply would have no owner", q.name, q.seq)
		}
	}
}

func TestTheTerminalsAnswerIsConsumedNotTyped(t *testing.T) {
	// The real shapes. If matchTerminalReport returns 0 for any of these, the bytes fall through
	// to the child and appear as if the human typed them.
	cases := []struct {
		name, seq string
	}{
		{"XTVERSION reply (DCS, ST-terminated)", "\x1bP>|iTerm2 3.5.11\x1b\\"},
		{"XTVERSION reply (DCS, BEL-terminated)", "\x1bP>|WezTerm 20240203\x07"},
		{"XTGETTCAP reply (DCS)", "\x1bP1+r544e=787465726d2d323536636f6c6f72\x1b\\"},
		{"DA1 reply (CSI)", "\x1b[?62;1;2;6;9;15;22c"},
		{"cursor position (CSI R)", "\x1b[36;3R"},
	}
	for _, c := range cases {
		n := matchTerminalReport([]byte(c.seq))
		if n != len(c.seq) {
			t.Errorf("%s: consumed %d of %d bytes — the remainder reaches the child as keystrokes",
				c.name, n, len(c.seq))
		}
	}
}

// An unterminated DCS must NOT be consumed: holding back real input would be worse than a late
// report. The scanner returns 0 and leaves the bytes for the next read.
func TestAnIncompleteReplyIsLeftAlone(t *testing.T) {
	if n := matchTerminalReport([]byte("\x1bP>|iTerm2 3.5")); n != 0 {
		t.Fatalf("an unterminated DCS was consumed (%d bytes); real input could be swallowed", n)
	}
}

// Keys must never be mistaken for a terminal report — that would eat the user's input.
func TestOrdinaryKeysAreNotMistakenForReports(t *testing.T) {
	for _, k := range []string{"\x1b[A", "\x1b[B", "\x1b[3~", "\x1b[200~", "\x1bOP", "a", "\x1b"} {
		if n := matchTerminalReport([]byte(k)); n != 0 {
			t.Errorf("keystroke %q was swallowed as a report (%d bytes)", k, n)
		}
	}
}
