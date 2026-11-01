package brand

import (
	"strings"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

// The ONE width metric. Four near-identical copies used to live in package main (visWidth,
// cgVisLen), internal/ptymux (visLen) and internal/ptysess (a rune-counting clip); they
// disagreed on escape-sequence scanning, which is exactly the kind of one-column drift that
// frays a box border on one surface and not another. These are pure functions and unit-tested,
// because every frame in the app is laid out with them.
//
// NOTE: internal/ptymux.visBytes is deliberately NOT one of these. It budgets the status bar
// in visible BYTES to match clipANSI, its paired clipper — see the comment at its definition.

const reset = "\x1b[0m"

// escLen returns the byte length of the escape sequence starting at s[0] (which must be ESC),
// or 1 if it can't be parsed. Handles CSI (ESC [ … final byte 0x40–0x7E) — so \x1b[K and
// \x1b[H terminate correctly, not just SGR's 'm' — OSC (ESC ] … BEL or ST) and two-byte
// sequences like DECSC (ESC 7).
func escLen(s string) int {
	if len(s) < 2 {
		return 1
	}
	switch s[1] {
	case '[':
		for i := 2; i < len(s); i++ {
			if s[i] >= 0x40 && s[i] <= 0x7e {
				return i + 1
			}
		}
		return len(s)
	case ']':
		for i := 2; i < len(s); i++ {
			if s[i] == 0x07 {
				return i + 1
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
		}
		return len(s)
	default:
		return 2
	}
}

// VisWidth counts the visible DISPLAY COLUMNS of s: escape sequences cost nothing, and each
// rune is measured by terminal width (CJK/emoji = 2, combining marks = 0). Rune-counting here
// was the original bug that made rows "bounce" — a ✅ rendered one column wider than counted,
// overflowed the line, and auto-wrap shifted everything below.
func VisWidth(s string) int {
	n := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			i += escLen(s[i:])
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		n += runewidth.RuneWidth(r)
		i += size
	}
	return n
}

// Clip truncates s to at most w display columns, copying escape sequences verbatim (so it
// never counts a colour code toward width nor severs one) and appending a reset when it cuts.
// A wide rune that would straddle the edge is DROPPED, so the result can land one column short
// of w — always re-measure (or use PadTo) rather than assuming exactly w.
func Clip(s string, w int) string {
	if w <= 0 {
		return ""
	}
	var b strings.Builder
	vis := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			n := escLen(s[i:])
			b.WriteString(s[i : i+n])
			i += n
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		rw := runewidth.RuneWidth(r)
		if vis+rw > w {
			b.WriteString(reset)
			return b.String()
		}
		b.WriteString(s[i : i+size])
		vis += rw
		i += size
	}
	return b.String()
}

// ClipEllipsis is Clip with an "…" marking the cut — used by surfaces where a silent
// truncation would read as data loss (a session name, a repo path) rather than as layout.
// Returns s untouched when it already fits.
func ClipEllipsis(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if VisWidth(s) <= w {
		return s
	}
	return Clip(s, w-1) + "…" + reset
}

// PadTo right-pads (or clips) s to exactly w display columns.
func PadTo(s string, w int) string {
	if w < 0 {
		w = 0
	}
	if VisWidth(s) > w {
		s = Clip(s, w)
	}
	if pad := w - VisWidth(s); pad > 0 {
		return s + strings.Repeat(" ", pad)
	}
	return s
}
