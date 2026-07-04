package ptymux

import (
	"bytes"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

// legacyKeys rewrites kitty CSI-u and xterm modifyOtherKeys key encodings into the
// legacy bytes the home/launcher menu expects, so the launcher stays drivable even while
// a child has the terminal in an enhanced keyboard mode. Key RELEASE/repeat-suppressed
// events are dropped (so they don't double-type), and legacy sequences (arrows, page
// up/down, bracketed-paste markers, raw bytes) pass through untouched.
//
// Only applied to HOME-mode input. Live-mode input is forwarded to the child verbatim
// (the child wants its own encoding); the ctrl-\ prefix + command key are handled
// separately by decodeCmdKey.
func legacyKeys(b []byte) []byte {
	if !bytes.Contains(b, []byte{0x1b, '['}) {
		return b // no CSI — fast path
	}
	var out []byte
	i := 0
	for i < len(b) {
		if b[i] == 0x1b && i+1 < len(b) && b[i+1] == '[' {
			if lb, emit, n, matched := csiToLegacy(b[i:]); matched {
				if emit {
					out = append(out, lb)
				}
				i += n
				continue
			}
		}
		out = append(out, b[i])
		i++
	}
	return out
}

// visLen counts the visible terminal columns of s, skipping ANSI SGR escape sequences.
// Width is measured per RUNE by display width (not bytes), so wide/emoji glyphs (⏳ = 2,
// · = 1) and multi-byte markers (▸ = 1) are counted as the terminal renders them. Getting
// this wrong frays the centered box's right border and makes it jump as the selection moves.
func visLen(s string) int {
	n := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			if i < len(s) {
				i++
			}
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		n += runewidth.RuneWidth(r)
		i += size
	}
	return n
}

// arrowAt reports an arrow key at the start of b (used to switch tabs after the ctrl-\
// prefix). dir = +1 (right / next) · -1 (left / prev) · 0 (up/down — recognized but no
// horizontal move). n>0 means an arrow sequence was recognized and consumed n bytes (so the
// caller advances past it even for up/down, instead of leaking the bytes to the child);
// n==0 means b does not start with an arrow. Handles CSI (ESC [ … A/B/C/D) and SS3
// (ESC O A/B/C/D), tolerating an optional "1;<mods>" parameter block (e.g. ctrl/shift held).
func arrowAt(b []byte) (dir, n int) {
	if len(b) < 3 || b[0] != 0x1b || (b[1] != '[' && b[1] != 'O') {
		return 0, 0
	}
	j := 2
	for j < len(b) && (b[j] == ';' || (b[j] >= '0' && b[j] <= '9')) {
		j++
	}
	if j >= len(b) {
		return 0, 0
	}
	switch b[j] {
	case 'C': // right
		return 1, j + 1
	case 'D': // left
		return -1, j + 1
	case 'A', 'B': // up/down — consume so they don't leak, but don't move
		return 0, j + 1
	}
	return 0, 0
}

// csiToLegacy decodes a kitty CSI-u (`ESC [ <code> ; <mods>[:evt] u`) or xterm
// modifyOtherKeys (`ESC [ 27 ; <mods> ; <code> ~`) sequence at the start of b.
//   - matched=false → not one of those forms; caller passes the bytes through unchanged
//     (this is how legacy `ESC[A`, `ESC[5~`, `ESC[200~` etc. survive).
//   - emit=false → matched but suppressed: a key release (event 3), or a functional key
//     we don't map to a legacy byte.
func csiToLegacy(b []byte) (lb byte, emit bool, n int, matched bool) {
	j := 2
	for j < len(b) && (b[j] == ';' || b[j] == ':' || (b[j] >= '0' && b[j] <= '9')) {
		j++
	}
	if j >= len(b) {
		return 0, false, 0, false
	}
	final := b[j]
	if final != 'u' && final != '~' {
		return 0, false, 0, false
	}
	fields := bytes.Split(b[2:j], []byte{';'})
	n = j + 1

	lead := func(f []byte) int { // leading integer of a field, before any ':' sub-param
		if k := bytes.IndexByte(f, ':'); k >= 0 {
			f = f[:k]
		}
		v := -1
		for _, c := range f {
			if c < '0' || c > '9' {
				return -1
			}
			if v < 0 {
				v = 0
			}
			v = v*10 + int(c-'0')
		}
		return v
	}
	event := func(f []byte) int { // kitty event type from the mods field's ':' sub-param
		k := bytes.IndexByte(f, ':')
		if k < 0 {
			return 1
		}
		if e := lead(f[k+1:]); e > 0 {
			return e
		}
		return 1
	}

	var code, mods, evt int
	switch final {
	case 'u': // ESC [ <code> ; <mods>[:evt] u
		if len(fields) < 1 {
			return 0, false, 0, false
		}
		code = lead(fields[0])
		if code < 0 {
			return 0, false, n, true // matched shape but garbage → drop
		}
		mods, evt = 1, 1
		if len(fields) >= 2 {
			if m := lead(fields[1]); m > 0 {
				mods = m
			}
			evt = event(fields[1])
		}
	case '~': // ESC [ 27 ; <mods> ; <code> ~  (modifyOtherKeys only)
		if len(fields) < 3 || lead(fields[0]) != 27 {
			return 0, false, 0, false // a legacy `ESC[<n>~` (PgUp/Del/paste) — pass through
		}
		code = lead(fields[2])
		if code < 0 {
			return 0, false, n, true
		}
		mods, evt = 1, 1
		if m := lead(fields[1]); m > 0 {
			mods = m
		}
	}

	if evt == 3 { // key release → drop (don't double-type in the menu)
		return 0, false, n, true
	}
	ctrl := mods >= 1 && (mods-1)&4 != 0
	switch code {
	case 13:
		return '\r', true, n, true // Enter
	case 27:
		return 0x1b, true, n, true // Esc
	case 9:
		return '\t', true, n, true // Tab
	case 8, 127:
		return 0x7f, true, n, true // Backspace
	}
	if code >= 32 && code < 127 { // printable
		c := byte(code)
		if ctrl { // ctrl-<letter> → control byte
			if c >= 'a' && c <= 'z' {
				return c & 0x1f, true, n, true
			}
			if c >= 'A' && c <= 'Z' {
				return (c + 32) & 0x1f, true, n, true
			}
		}
		return c, true, n, true
	}
	return 0, false, n, true // unmapped functional key → drop
}

// clipPadPlain truncates or space-pads plain text (no embedded ANSI) to exactly cols
// display columns, measured per-rune so arrows/wide glyphs count as the terminal renders
// them. Used for the scrollback status line — keeping it ≤ cols stops it wrapping (autowrap
// is on for children) and corrupting the row above.
func clipPadPlain(s string, cols int) string {
	if cols < 0 {
		cols = 0
	}
	w := 0
	var out []rune
	for _, r := range s {
		rw := runewidth.RuneWidth(r)
		if w+rw > cols {
			break
		}
		out = append(out, r)
		w += rw
	}
	for w < cols {
		out = append(out, ' ')
		w++
	}
	return string(out)
}

// scroll-mode key actions, decoded by scrollKeyAt.
const (
	scrNone = iota
	scrUp   // one line older
	scrDown // one line newer
	scrPgUp // a page older
	scrPgDn // a page newer
	scrTop  // jump to the oldest line
	scrExit // leave scroll mode, back to live
)

// scrollKeyAt decodes the next key while in scroll mode into an action + bytes consumed.
// Recognizes arrows (incl. app-cursor ESC O), PageUp/Down, Home, plus vi-style j/k/g and
// q/esc/enter to leave. Anything else returns scrExit so a stray keystroke dismisses
// scroll mode (and resumes live) rather than being silently eaten.
func scrollKeyAt(b []byte) (action, n int) {
	if len(b) == 0 {
		return scrNone, 0
	}
	if b[0] == 0x1b && len(b) >= 2 && (b[1] == '[' || b[1] == 'O') {
		j := 2
		for j < len(b) && (b[j] == ';' || (b[j] >= '0' && b[j] <= '9')) {
			j++
		}
		if j >= len(b) {
			return scrExit, 1 // incomplete escape — treat the lone ESC as exit
		}
		params := b[2:j]
		switch b[j] {
		case 'A':
			return scrUp, j + 1
		case 'B':
			return scrDown, j + 1
		case 'H':
			return scrTop, j + 1
		case 'F':
			return scrExit, j + 1 // End → jump to live
		case '~':
			switch string(params) {
			case "5":
				return scrPgUp, j + 1
			case "6":
				return scrPgDn, j + 1
			case "1", "7":
				return scrTop, j + 1
			case "4", "8":
				return scrExit, j + 1 // End
			}
			return scrExit, j + 1
		}
		return scrExit, j + 1
	}
	switch b[0] {
	case 'k':
		return scrUp, 1
	case 'j':
		return scrDown, 1
	case 'g':
		return scrTop, 1
	case ' ':
		return scrPgDn, 1
	case 'q', 'Q', 0x1b, '\r', '\n':
		return scrExit, 1
	}
	return scrExit, 1 // any other key dismisses scroll mode
}
