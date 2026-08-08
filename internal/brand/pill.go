package brand

// Pill is the reverse mode badge — brand-pink background, dark bold text — that labels which
// surface (and which key set) you are looking at: LIVE, SPLIT, SCROLLBACK, QUIT, SESSION…
// It merges package main's hintPill with internal/ptymux's barActiveBg, which had drifted into
// two spellings of the same colour.
//
// THEME DECISION — the pill is deliberately THEME-INDEPENDENT, and constructed so that it
// stays that way even under package main's themed() re-skin (llms_theme.go), which rewrites
// ANSI-256 indexes in the finished frame. Two reasons:
//
//  1. A pill carries its OWN background, so unlike coloured foreground text its contrast does
//     not depend on the terminal background. One brand colour is legible on every theme, and
//     the mode badge is the last thing that should change meaning when you switch themes.
//  2. Letting themed() touch it was an active bug: the old 256 fallback used bg 205, which
//     Paperwhite remaps to 16 — the same index as the pill's own dark TEXT. Black on black.
//
// So the fallback uses 206 (hot magenta) and the text uses 16: neither is a remap SOURCE in any
// theme table, so the pill renders identically in all five themes, truecolor or not. If a stop
// is ever changed, check it against the remap keys in llms_theme.go first.
const (
	pillBg256 = "\x1b[48;5;206m"
	pillBgRGB = "\x1b[48;2;255;94;138m"
	pillFg    = "\x1b[1;38;5;16m"
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
