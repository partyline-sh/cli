package brand

import (
	"strings"
	"testing"
)

// The wordmark must carry the letters in order (colour codes interleaved) and be ANSI-styled.
func TestWordmarkLettersInOrder(t *testing.T) {
	t.Setenv("COLORTERM", "truecolor")
	out := Wordmark()
	pos := 0
	for _, l := range []string{"☎", "P", "A", "R", "T", "Y", "L", "I", "N", "E"} {
		i := strings.Index(out[pos:], l)
		if i < 0 {
			t.Fatalf("wordmark missing %q after byte %d: %q", l, pos, out)
		}
		pos += i + len(l)
	}
	if !strings.Contains(out, "\x1b[") {
		t.Errorf("wordmark has no ANSI escapes: %q", out)
	}
	if !strings.Contains(out, "38;2;") {
		t.Errorf("COLORTERM=truecolor should use truecolor sequences: %q", out)
	}
	if !strings.HasSuffix(out, "\x1b[0m") {
		t.Errorf("wordmark must end with a reset: %q", out)
	}
}

// Without COLORTERM the helpers must fall back to 256-colour approximations.
func TestFallback256(t *testing.T) {
	t.Setenv("COLORTERM", "")
	for name, out := range map[string]string{
		"wordmark": Wordmark(),
		"gradient": GradientText("hi there"),
		"phone":    Phone(),
		"phase":    WordmarkPhase(3),
		"pill":     Pill("LIVE"),
	} {
		if strings.Contains(out, ";2;") {
			t.Errorf("%s: truecolor sequence emitted without COLORTERM: %q", name, out)
		}
		if !strings.Contains(out, ";5;") {
			t.Errorf("%s: expected a 256-colour fallback sequence: %q", name, out)
		}
	}
	// The fallback indexes stay within the curated brand set.
	if got := nearest256(GradFrom); got != 214 {
		t.Errorf("gradient start should approximate amber 214, got %d", got)
	}
	if got := nearest256(GradTo); got != 171 && got != 177 {
		t.Errorf("gradient end should approximate magenta 171/177, got %d", got)
	}
}

// The sweep must colour the same glyphs in the same order at every phase — only the colours
// move. (The boot splash advances phase per tick; a phase that reordered or dropped glyphs
// would make the wordmark flicker rather than shimmer.)
func TestGradientPhaseKeepsGlyphs(t *testing.T) {
	strip := func(s string) string {
		var b strings.Builder
		for i := 0; i < len(s); {
			if s[i] == 0x1b {
				i += escLen(s[i:])
				continue
			}
			b.WriteByte(s[i])
			i++
		}
		return b.String()
	}
	want := strip(GradientPhase("P A R T Y L I N E", 0))
	if want != "P A R T Y L I N E" {
		t.Fatalf("phase 0 glyphs = %q", want)
	}
	for _, phase := range []int{-7, 0, 1, 5, 23, 24, 25, 1000} {
		if got := strip(GradientPhase("P A R T Y L I N E", phase)); got != want {
			t.Errorf("phase %d glyphs = %q, want %q", phase, got, want)
		}
	}
}
