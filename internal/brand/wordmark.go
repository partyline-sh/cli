// Package brand is partyline's ONE source of terminal chrome: the gradient wordmark, the
// accent palette, the width/clip metric every frame is measured with, the mode pill, and the
// hint bar. It lives under internal/ (not package main) for a structural reason: ptymux and
// ptysess paint their own surfaces and could not import package main, so before this package
// existed each of them grew its own wordmark, its own gradient ramp and its own width metric —
// four of each. Nothing could converge because nothing could import.
//
// Truecolor is used when the terminal advertises it (COLORTERM); otherwise the nearest of five
// ANSI-256 stops. Every helper ends with a reset so callers can concatenate freely.
package brand

import (
	"fmt"
	"os"
	"strings"
)

// Brand palette: amber phone; wordmark gradient amber → pink → magenta.
var (
	AmberRGB = [3]int{0xff, 0x98, 0x38}
	GradFrom = [3]int{0xff, 0xaa, 0x3c}
	GradMid  = [3]int{0xff, 0x5e, 0x8a}
	GradTo   = [3]int{0xdf, 0x5b, 0xff}
)

// Truecolor reports whether the terminal advertises 24-bit colour support.
func Truecolor() bool {
	ct := strings.ToLower(os.Getenv("COLORTERM"))
	return strings.Contains(ct, "truecolor") || strings.Contains(ct, "24bit")
}

// stops256 are the 256-colour stops nearest the gradient (amber, pinks, magentas), with
// approximate RGB for nearest-colour matching when truecolor isn't available.
var stops256 = []struct {
	idx int
	rgb [3]int
}{
	{214, [3]int{255, 175, 0}},   // amber / orange
	{211, [3]int{255, 135, 175}}, // light pink
	{205, [3]int{255, 95, 175}},  // pink
	{177, [3]int{215, 135, 255}}, // light magenta
	{171, [3]int{215, 95, 255}},  // magenta
}

func nearest256(c [3]int) int {
	best, bestD := stops256[0].idx, 1<<62
	for _, s := range stops256 {
		dr, dg, db := c[0]-s.rgb[0], c[1]-s.rgb[1], c[2]-s.rgb[2]
		if d := dr*dr + dg*dg + db*db; d < bestD {
			best, bestD = s.idx, d
		}
	}
	return best
}

// Fg returns the SGR foreground sequence for c (truecolor or 256 fallback), no reset.
func Fg(c [3]int) string {
	if Truecolor() {
		return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", c[0], c[1], c[2])
	}
	return fmt.Sprintf("\x1b[38;5;%dm", nearest256(c))
}

// GradAt interpolates the 3-stop gradient at t ∈ [0,1].
func GradAt(t float64) [3]int {
	a, b := GradFrom, GradMid
	if t > 0.5 {
		a, b, t = GradMid, GradTo, t-0.5
	}
	t *= 2
	var c [3]int
	for i := range c {
		c[i] = a[i] + int(t*float64(b[i]-a[i]))
	}
	return c
}

// GradientText colours s with the brand gradient (bold), one step per non-space rune.
func GradientText(s string) string { return gradient(s, 0, 0) }

// GradientPhase is GradientText with the gradient SWEEPING: the colour of each glyph is taken
// `phase` steps further along a gradient that wraps, so advancing phase per tick makes the mark
// shimmer. Replaces the mux's separate 28-stop rainbow ramp — same animation, one palette.
func GradientPhase(s string, phase int) string { return gradient(s, phase, 12) }

// gradient colours the non-space runes of s along the brand gradient. With cycle == 0 the
// gradient spans the string exactly once; with cycle > 0 it wraps every `cycle` glyphs and
// `phase` offsets the start, which is what produces the sweep.
func gradient(s string, phase, cycle int) string {
	runes := []rune(s)
	n := 0
	for _, r := range runes {
		if r != ' ' {
			n++
		}
	}
	if n == 0 {
		return s
	}
	var b strings.Builder
	i := 0
	for _, r := range runes {
		if r == ' ' {
			b.WriteRune(r)
			continue
		}
		t := 0.0
		switch {
		case cycle > 0:
			// Triangle wave over [0,1] so the wrap is seamless (no hard jump at the seam).
			k := ((i+phase)%(2*cycle) + 2*cycle) % (2 * cycle)
			if k >= cycle {
				k = 2*cycle - k
			}
			t = float64(k) / float64(cycle)
		case n > 1:
			t = float64(i) / float64(n-1)
		}
		b.WriteString("\x1b[1m" + Fg(GradAt(t)))
		b.WriteRune(r)
		i++
	}
	b.WriteString("\x1b[0m")
	return b.String()
}

// Phone is the amber ☎ glyph, reset-terminated.
func Phone() string { return Fg(AmberRGB) + "☎\x1b[0m" }

// Wordmark renders the full "☎ P A R T Y L I N E" mark: amber phone + gradient letters.
func Wordmark() string { return Phone() + " " + GradientText("P A R T Y L I N E") }

// WordmarkPhase is Wordmark with the letters swept by phase — the boot-splash animation.
func WordmarkPhase(phase int) string {
	return Phone() + "  " + GradientPhase("P A R T Y L I N E", phase)
}
