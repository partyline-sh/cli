// Package brand renders terminal chrome: the gradient wordmark, width/clip metrics, mode pill,
// and hint bar. Colours come from internal/theme. It lives under internal/ so terminal renderers
// can share it without importing package main.
//
// Truecolor is used when the terminal advertises it (COLORTERM); otherwise the nearest of five
// ANSI-256 stops. Every helper ends with a reset so callers can concatenate freely.
package brand

import (
	"os"
	"strings"

	"partyline.sh/partyline/internal/theme"
)

var (
	AmberRGB = theme.AmberRGB
	GradFrom = theme.BrandGradFrom
	GradMid  = theme.BrandGradMid
	GradTo   = theme.BrandGradTo
)

// Truecolor reports whether the terminal advertises 24-bit colour support.
func Truecolor() bool {
	ct := strings.ToLower(os.Getenv("COLORTERM"))
	return strings.Contains(ct, "truecolor") || strings.Contains(ct, "24bit")
}

func nearest256(c [3]int) int { return theme.NearestBrand256(c) }

// Fg returns the SGR foreground sequence for c (truecolor or 256 fallback), no reset.
func Fg(c [3]int) string {
	if Truecolor() {
		return theme.RGBForeground(c)
	}
	return theme.IndexedForeground(nearest256(c))
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

// EnvBadge marks a NON-PRODUCTION control plane, and returns "" for production so the common case
// stays unadorned. Rendered next to the wordmark.
//
// This exists because the failure it prevents is silent and expensive: with credentials now scoped
// per environment, the same binary can be signed into staging and production at once, and nothing
// on screen would otherwise distinguish them. Believing you are on staging while acting on prod is
// the kind of mistake you only make once, memorably.
func EnvBadge(label string) string {
	if label == "" {
		return ""
	}
	return Fg(AmberRGB) + "[" + label + "]\x1b[0m"
}

// WordmarkPhase is Wordmark with the letters swept by phase — the boot-splash animation.
func WordmarkPhase(phase int) string {
	return Phone() + "  " + GradientPhase("P A R T Y L I N E", phase)
}

// PillRGB is the focused-pill background — the pink behind the active tab segment
// (the RGB behind pill.go's SGR strings, exported for renderers that need the raw value).
var PillRGB = theme.PillRGB

// Hex renders an RGB triple as a #rrggbb string, for surfaces that take hex colors
// (the tmux backend's generated conf) rather than SGR sequences.
func Hex(rgb [3]int) string { return theme.HexRGB(rgb) }
