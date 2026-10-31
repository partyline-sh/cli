package brand

import uitheme "partyline.sh/partyline/internal/theme"

// Pill is the reverse mode badge: brand-pink background and dark bold text. It carries its own
// background, so it keeps the same contrast in every palette. The theme package owns both its
// truecolor value and the ANSI fallback.
const (
	pillBg256 = uitheme.PillBackground256
	pillBgRGB = uitheme.PillBackgroundRGB
	pillFg    = uitheme.PillForeground
)

// PillBg returns just the pill's background SGR — for callers that paint their own foreground
// over it (the mux's focused tab segment, which carries its own activity glyphs).
func PillBg() string {
	if Truecolor() {
		return pillBgRGB
	}
	return pillBg256
}

// Pill renders label as a padded reverse badge, reset-terminated.
func Pill(label string) string { return PillBg() + pillFg + " " + label + " " + reset }
