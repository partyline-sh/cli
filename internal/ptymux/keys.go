package ptymux

import "bytes"

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

// visLen counts the visible columns of s, skipping ANSI SGR escape sequences (byte-based;
// adequate for the ASCII + box text the mux renders).
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
		n++
		i++
	}
	return n
}

// ctrlOAt reports whether b starts with a Ctrl-O keypress (the direct "back to launcher"
// hotkey) and how many bytes the keypress spans. kind: 0 = not ctrl-o (caller forwards the
// byte to the child); 1 = ctrl-o press → jump home; 2 = ctrl-o release/repeat → suppress
// (so the release doesn't leak to the child). Plain 'o' (no ctrl) is NOT matched.
func ctrlOAt(b []byte) (kind, n int) {
	if len(b) == 0 {
		return 0, 0
	}
	if b[0] == 0x0f { // raw Ctrl-O (legacy terminals); no event info → treat as a press
		return 1, 1
	}
	code, mods, evt, sz, ok := parseCSIu(b)
	if !ok || (code != 'o' && code != 'O') {
		return 0, 0
	}
	if mods < 1 || (mods-1)&4 == 0 { // ctrl not held → it's a plain 'o' for the child
		return 0, 0
	}
	if evt == 2 || evt == 3 { // repeat / release → suppress
		return 2, sz
	}
	return 1, sz
}

// parseCSIu parses a kitty CSI-u (`ESC [ <code> ; <mods>[:evt] u`) or xterm modifyOtherKeys
// (`ESC [ 27 ; <mods> ; <code> ~`) sequence at the start of b, returning the base keycode,
// modifier field, kitty event type (1=press, 2=repeat, 3=release; default 1), bytes
// consumed, and ok=false if b doesn't begin with such a sequence.
func parseCSIu(b []byte) (code, mods, evt, n int, ok bool) {
	if len(b) < 3 || b[0] != 0x1b || b[1] != '[' {
		return 0, 0, 0, 0, false
	}
	j := 2
	for j < len(b) && (b[j] == ';' || b[j] == ':' || (b[j] >= '0' && b[j] <= '9')) {
		j++
	}
	if j >= len(b) || (b[j] != 'u' && b[j] != '~') {
		return 0, 0, 0, 0, false
	}
	final := b[j]
	fields := bytes.Split(b[2:j], []byte{';'})
	lead := func(f []byte) int {
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
	eventOf := func(f []byte) int {
		k := bytes.IndexByte(f, ':')
		if k < 0 {
			return 1
		}
		if e := lead(f[k+1:]); e > 0 {
			return e
		}
		return 1
	}
	n = j + 1
	mods, evt = 1, 1
	switch final {
	case 'u':
		if len(fields) < 1 {
			return 0, 0, 0, 0, false
		}
		code = lead(fields[0])
		if code < 0 {
			return 0, 0, 0, 0, false
		}
		if len(fields) >= 2 {
			if m := lead(fields[1]); m > 0 {
				mods = m
			}
			evt = eventOf(fields[1])
		}
	case '~':
		if len(fields) < 3 || lead(fields[0]) != 27 {
			return 0, 0, 0, 0, false
		}
		code = lead(fields[2])
		if code < 0 {
			return 0, 0, 0, 0, false
		}
		if m := lead(fields[1]); m > 0 {
			mods = m
		}
	}
	return code, mods, evt, n, true
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
