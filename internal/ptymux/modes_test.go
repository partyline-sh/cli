package ptymux

import (
	"strings"
	"testing"
)

// #238: the mux must recapture a child's scroll region (DECSTBM) from its output and re-emit it on
// a switch — otherwise Claude Code's input box misrenders after a tab switch.
func TestModeStateScrollRegion(t *testing.T) {
	t.Run("captures + restores DECSTBM", func(t *testing.T) {
		var m modeState
		m.observe([]byte("some output\x1b[1;52rmore output"))
		if got := string(m.restore()); !strings.Contains(got, "\x1b[1;52r") {
			t.Errorf("restore() = %q, want it to re-emit \\x1b[1;52r", got)
		}
	})

	t.Run("a reset (ESC[r) clears the region — nothing to restore", func(t *testing.T) {
		var m modeState
		m.observe([]byte("\x1b[1;52r")) // set
		m.observe([]byte("\x1b[r"))     // then reset to full
		if got := string(m.restore()); strings.Contains(got, "r") && strings.Contains(got, ";") {
			t.Errorf("restore() = %q, want no scroll-region after reset", got)
		}
	})

	t.Run("still restores the private modes alongside the region", func(t *testing.T) {
		var m modeState
		m.observe([]byte("\x1b[?2004h\x1b[?1000h\x1b[2;40r"))
		got := string(m.restore())
		for _, want := range []string{"\x1b[?2004h", "\x1b[?1000h", "\x1b[2;40r"} {
			if !strings.Contains(got, want) {
				t.Errorf("restore() = %q, missing %q", got, want)
			}
		}
	})

	t.Run("ignores private-mode CSIs that happen to sit near an r", func(t *testing.T) {
		var m modeState
		m.observe([]byte("\x1b[?25l")) // cursor hide — must not be read as a region
		if got := string(m.restore()); strings.Contains(got, ";") {
			t.Errorf("restore() = %q, should have no DECSTBM", got)
		}
	})
}
