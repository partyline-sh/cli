package ptymux

import (
	"strings"
	"testing"
)

// The plan's one real gap: Snapshot() restores content+cursor+alt-screen but NOT the
// other DEC private modes, so the mux must sniff them from each child's output and
// re-assert on switch. These tests pin that sniff-and-restore contract.

func TestModeStateObserveRestore(t *testing.T) {
	var m modeState
	// A child hides the cursor, turns on SGR mouse + bracketed paste + app-cursor-keys.
	m.observe([]byte("hello\x1b[?25l\x1b[?1h\x1b[?2004h\x1b[?1006hworld"))
	got := string(m.restore())
	for _, want := range []string{"\x1b[?25l", "\x1b[?1h", "\x1b[?2004h", "\x1b[?1006h"} {
		if !strings.Contains(got, want) {
			t.Errorf("restore() = %q, missing %q", got, want)
		}
	}
}

func TestModeStateClearedModeNotRestored(t *testing.T) {
	var m modeState
	// Set bracketed paste then clear it; cursor shown (default) — restore should emit neither.
	m.observe([]byte("\x1b[?2004h\x1b[?2004l\x1b[?25h"))
	got := string(m.restore())
	if strings.Contains(got, "2004") {
		t.Errorf("restore() = %q, should not re-assert a cleared mode", got)
	}
	if strings.Contains(got, "?25l") {
		t.Errorf("restore() = %q, cursor was shown — should not re-hide", got)
	}
}

func TestModeStateMultiParam(t *testing.T) {
	var m modeState
	// One escape can set several modes: `\x1b[?1000;1006h`.
	m.observe([]byte("\x1b[?1000;1006h"))
	got := string(m.restore())
	if !strings.Contains(got, "?1000h") || !strings.Contains(got, "?1006h") {
		t.Errorf("restore() = %q, want both 1000h and 1006h", got)
	}
}

func TestModeStateIgnoresUntracked(t *testing.T) {
	var m modeState
	// ?1049 (alt-screen) is Snapshot's job, not ours — must not appear in restore().
	m.observe([]byte("\x1b[?1049h\x1b[?7h"))
	if got := string(m.restore()); got != "" {
		t.Errorf("restore() = %q, want empty (only untracked modes seen)", got)
	}
}

func TestDecodeCmdKey(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		key  byte
		n    int
	}{
		{"plain letter", []byte("l"), 'l', 1},
		{"plain digit", []byte("3"), '3', 1},
		{"ctrl-letter byte", []byte{0x0c}, 'l', 1},        // ctrl-l held through the chord
		{"ctrl-p byte", []byte{0x10}, 'p', 1},             // ctrl-p
		{"literal ctrl-backslash", []byte{0x1c}, 0x1c, 1}, // ctrl-\ ctrl-\ → literal
		{"csi-u ctrl-p", []byte("\x1b[112;5u"), 'p', 8},   // from the log: holding ctrl
		{"csi-u ctrl-l", []byte("\x1b[108;5u"), 'l', 8},
		{"csi-u plain l", []byte("\x1b[108u"), 'l', 6},
		{"csi-u uppercase", []byte("\x1b[76;5u"), 'l', 7},             // code 76 = 'L' → lowercased
		{"modifyOtherKeys ctrl-q", []byte("\x1b[27;5;113~"), 'q', 11}, // from the log
		{"modifyOtherKeys ctrl-n", []byte("\x1b[27;5;110~"), 'n', 11},
		{"kitty sub-param", []byte("\x1b[112;5:1u"), 'p', 10}, // event-type sub-param ignored
	}
	for _, c := range cases {
		k, n := decodeCmdKey(c.in)
		if k != c.key || n != c.n {
			t.Errorf("%s: decodeCmdKey(%q) = (%q,%d), want (%q,%d)", c.name, c.in, k, n, c.key, c.n)
		}
	}
}

func TestLegacyKeys(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain passthrough", "abc", "abc"},
		{"raw enter passthrough", "\r", "\r"},
		{"legacy arrow passthrough", "\x1b[A", "\x1b[A"},
		{"legacy pageup passthrough", "\x1b[5~", "\x1b[5~"},
		{"bracketed paste marker passthrough", "\x1b[200~", "\x1b[200~"},
		{"csi-u enter", "\x1b[13;1u", "\r"},
		{"csi-u letter j", "\x1b[106;1u", "j"},
		{"csi-u uppercase stays char", "\x1b[74u", "J"},
		{"csi-u ctrl-c", "\x1b[99;5u", "\x03"},
		{"release dropped", "\x1b[116;1:3u", ""},
		{"press kept, release dropped", "\x1b[116;1u\x1b[116;1:3u", "t"},
		{"modifyOtherKeys enter", "\x1b[27;1;13~", "\r"},
		{"esc", "\x1b[27;1u", "\x1b"},
	}
	for _, c := range cases {
		if got := string(legacyKeys([]byte(c.in))); got != c.want {
			t.Errorf("%s: legacyKeys(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestArrowAt(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		dir  int
		n    int
	}{
		{"csi right", []byte("\x1b[C"), 1, 3},
		{"csi left", []byte("\x1b[D"), -1, 3},
		{"ss3 right", []byte("\x1bOC"), 1, 3},
		{"ss3 left", []byte("\x1bOD"), -1, 3},
		{"ctrl-right with mods", []byte("\x1b[1;5C"), 1, 6},
		{"shift-left with mods", []byte("\x1b[1;2D"), -1, 6},
		{"up consumed, no move", []byte("\x1b[A"), 0, 3},
		{"down consumed, no move", []byte("\x1b[B"), 0, 3},
		{"home key is NOT an arrow", []byte("\x1b[H"), 0, 0},
		{"plain byte NOT", []byte("x"), 0, 0},
		{"bare esc NOT", []byte("\x1b"), 0, 0},
	}
	for _, c := range cases {
		dir, n := arrowAt(c.in)
		if dir != c.dir || n != c.n {
			t.Errorf("%s: arrowAt(%q) = (%d,%d), want (%d,%d)", c.name, c.in, dir, n, c.dir, c.n)
		}
	}
}

func TestClipANSIVisibleWidth(t *testing.T) {
	// Escape sequences must not count toward the visible width budget.
	s := "\x1b[7m AB \x1b[0m"
	got := clipANSI(s, 2)
	if want := "\x1b[7m A"; got != want {
		t.Errorf("clipANSI(%q, 2) = %q, want %q", s, got, want)
	}
	if clipANSI("abcdef", 0) != "" {
		t.Error("clipANSI with width 0 should be empty")
	}
	// Fits entirely → unchanged.
	if clipANSI("abc", 10) != "abc" {
		t.Error("clipANSI should pass through strings narrower than the width")
	}
}
