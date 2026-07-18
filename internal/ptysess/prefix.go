package ptysess

import "bytes"

// Terminals in CSI-u (fixterms/kitty) or xterm modifyOtherKeys mode — which vim,
// claude, codex, gemini etc. turn on when the terminal allows it (e.g. iTerm2's
// "Apps can change how keys are reported", or the kitty keyboard protocol) —
// report ctrl-\ as an escape sequence instead of the legacy control byte 0x1c.
// Apple Terminal, which lacks that capability, still sends 0x1c. So the same
// keypress reaches us differently per terminal/app, and our prefix detection
// (which looks for 0x1c) silently misses it inside a full-screen app.
//
// We can't rely on a fixed encoding: the modifier field varies (ctrl alone = 5,
// but apps that also report shift/alt or use kitty's flag sets emit other values),
// and kitty adds optional sub-parameters (`92;5:1u`, event types, associated
// text). So NormalizeCtrlBackslash scans CSI sequences and recognizes *any* form
// that means "backslash key (codepoint 92) with ctrl held", in either:
//
//	CSI-u:            ESC [ 92 ; <mods>[:...] u
//	modifyOtherKeys:  ESC [ 27 ; <mods> ; 92 ~
//
// matching when (mods-1) has the ctrl bit (4) set. Everything matched is rewritten
// to a single 0x1c so the rest of the input path is encoding-independent.
func NormalizeCtrlBackslash(b []byte) []byte {
	if !bytes.Contains(b, []byte{0x1b, '['}) {
		return b // no CSI at all — fast path
	}
	var out []byte // lazily allocated only if we rewrite something
	i := 0
	for i < len(b) {
		if b[i] == 0x1b && i+1 < len(b) && b[i+1] == '[' {
			if n, ev, ok := matchCtrlBackslashCSI(b[i:]); ok {
				if out == nil {
					out = make([]byte, 0, len(b))
					out = append(out, b[:i]...)
				}
				// Press → the prefix byte. Repeat/release events (kitty event types
				// 2/3) are DROPPED: the child never needs ctrl-\ in any form (it's our
				// prefix key), and emitting the release as a second 0x1c made it look
				// like a literal ctrl-\, swallowing the prefix before the next key.
				if ev == 1 {
					out = append(out, PrefixKey)
				}
				i += n
				continue
			}
		}
		if out != nil {
			out = append(out, b[i])
		}
		i++
	}
	if out == nil {
		return b
	}
	return out
}

// matchCtrlBackslashCSI checks whether b STARTS with a CSI sequence that encodes
// ctrl-\, returning the sequence length, the kitty event type (1=press, 2=repeat,
// 3=release; defaults to 1 when absent), and true on a match. It parses a CSI of
// the form `ESC [ <params> <final>` where params are digits, ';' and ':'.
func matchCtrlBackslashCSI(b []byte) (length, event int, ok bool) {
	if len(b) < 4 || b[0] != 0x1b || b[1] != '[' {
		return 0, 0, false
	}
	j := 2
	for j < len(b) && (b[j] == ';' || b[j] == ':' || (b[j] >= '0' && b[j] <= '9')) {
		j++
	}
	if j >= len(b) {
		return 0, 0, false // sequence not yet complete in this buffer
	}
	final := b[j]
	if final != 'u' && final != '~' {
		return 0, 0, false
	}
	params := bytes.Split(b[2:j], []byte{';'})
	// leadNum returns a param's leading number (before any ':' sub-param).
	leadNum := func(p []byte) (int, bool) {
		if k := bytes.IndexByte(p, ':'); k >= 0 {
			p = p[:k]
		}
		return atoiBytes(p)
	}
	// eventOf extracts the event type from a param's ':' sub-param (the part AFTER
	// the modifier), defaulting to 1 (press) when absent.
	eventOf := func(p []byte) int {
		k := bytes.IndexByte(p, ':')
		if k < 0 {
			return 1
		}
		if n, ok := atoiBytes(p[k+1:]); ok {
			return n
		}
		return 1
	}
	ctrlHeld := func(modField int) bool { return modField >= 1 && (modField-1)&4 != 0 }

	switch final {
	case 'u': // ESC [ <code> ; <mods>[:<event>] u
		if len(params) < 2 {
			return 0, 0, false
		}
		code, ok1 := leadNum(params[0])
		mods, ok2 := leadNum(params[1])
		if ok1 && ok2 && code == 92 && ctrlHeld(mods) {
			return j + 1, eventOf(params[1]), true
		}
	case '~': // ESC [ 27 ; <mods> ; <code> ~  (modifyOtherKeys — no event type)
		if len(params) < 3 {
			return 0, 0, false
		}
		lead, ok0 := leadNum(params[0])
		mods, ok1 := leadNum(params[1])
		code, ok2 := leadNum(params[2])
		if ok0 && ok1 && ok2 && lead == 27 && code == 92 && ctrlHeld(mods) {
			return j + 1, 1, true
		}
	}
	return 0, 0, false
}

func atoiBytes(p []byte) (int, bool) {
	if len(p) == 0 {
		return 0, false
	}
	n := 0
	for _, c := range p {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}
