package ptymux

// Terminal query/reply routing.
//
// Interactive CLIs (claude, vim, htop) query the terminal at runtime — cursor position
// (DSR "ESC[6n"), device attributes ("ESC[c"), window/title reports ("ESC[21t"), DEC mode
// state ("ESC[?…$p"), and background/foreground colour ("ESC]11;?"). The terminal answers
// ASYNCHRONOUSLY on stdin. The mux forwards stdin to whatever child is active, so if the
// user switches tabs between a query and its reply, the reply is typed into the WRONG
// session (e.g. an OSC title report lands as literal text in another agent's input box).
//
// Fix: remember which child last issued a query that reached the real terminal (only the
// active child's output does), and route the next reply back to THAT child regardless of
// which tab is active now. This preserves a child's own queries (colour/theme detection,
// cursor probing) while stopping cross-switch injection.

// noteQuery records ch as the owner of the next terminal reply. The most recent query wins
// (a single outstanding reply is the norm; a superseded one is simply dropped when its reply
// arrives with no owner — better than injecting it into the wrong place).
func (mx *Mux) noteQuery(ch *child) {
	mx.queryMu.Lock()
	mx.queryOwner = ch
	mx.queryMu.Unlock()
}

// takeQueryOwner returns and clears the pending reply owner (nil if none is waiting).
func (mx *Mux) takeQueryOwner() *child {
	mx.queryMu.Lock()
	ch := mx.queryOwner
	mx.queryOwner = nil
	mx.queryMu.Unlock()
	return ch
}

// clearQueryOwner drops the pending owner if it is ch (called when a child exits so a dead
// child is never handed a reply).
func (mx *Mux) clearQueryOwner(ch *child) {
	mx.queryMu.Lock()
	if mx.queryOwner == ch {
		mx.queryOwner = nil
	}
	mx.queryMu.Unlock()
}

// containsTerminalQuery reports whether a child's output stream contains a query that makes
// the terminal answer on stdin: a CSI query ending in n (DSR), c (DA), t (window op) or p
// (only reachable here as DECRQM "…$p" — soft reset "!p" / DECSCL "\"p" stop the scan at
// their intermediate), or an OSC query (contains "?", e.g. "ESC]11;?"). Over-detection is
// harmless — it just names the active child as owner, which is the correct owner anyway.
func containsTerminalQuery(b []byte) bool {
	for i := 0; i+1 < len(b); i++ {
		if b[i] != 0x1b {
			continue
		}
		switch b[i+1] {
		case '[': // CSI
			j := i + 2
			for j < len(b) && isParamByte(b[j]) {
				j++
			}
			if j < len(b) {
				switch b[j] {
				// 'q' is XTVERSION (ESC[>0q) — "which terminal emulator are you?". Claude Code
				// sends exactly two probes at startup, DA1 and this one, and this one was not
				// recognised: no owner was registered, so its reply had nowhere to go.
				case 'n', 'c', 't', 'p', 'q':
					return true
				}
			}
		case ']': // OSC — a "?" param marks a colour/title query (reply comes back on stdin)
			for j := i + 2; j < len(b) && b[j] != 0x07 && b[j] != 0x1b; j++ {
				if b[j] == '?' {
					return true
				}
			}
		}
	}
	return false
}

// matchTerminalReport returns the byte length of a terminal reply sequence at the head of b,
// or 0 if b does not begin with one. Replies are unambiguous terminal→app: no keyboard key
// encodes as an OSC, and no key produces a CSI ending in R/n/c/t/y (arrows end A–D, function
// keys ~, CSI-u u, SGR mouse M/m — none overlap). An incomplete sequence at the end of the
// buffer returns 0 (leave it; never hold back what might be real input).
func matchTerminalReport(b []byte) int {
	if len(b) < 2 || b[0] != 0x1b {
		return 0
	}
	switch b[1] {
	case '[': // CSI reply: DSR (…R / …n), DA (…c), window op (…t), DECRPM (…$y)
		j := 2
		for j < len(b) && isParamByte(b[j]) {
			j++
		}
		if j >= len(b) {
			return 0 // incomplete
		}
		switch b[j] {
		case 'R', 'n', 'c', 't', 'y':
			return j + 1
		}
		return 0
	case 'P': // DCS report: ESC P … (ST | BEL) — this is XTVERSION's reply shape,
		// e.g. ESC P > | iTerm2 3.5.x ESC \. It was matched by NOTHING here, so it fell through
		// and was forwarded to the child as if the human had typed it: asking the terminal what
		// it is got its answer typed into the prompt.
		fallthrough
	case ']': // OSC report: ESC ] … (BEL | ST)
		for j := 2; j < len(b); j++ {
			if b[j] == 0x07 { // BEL terminator
				return j + 1
			}
			if b[j] == 0x1b && j+1 < len(b) && b[j+1] == '\\' { // ST = ESC \
				return j + 2
			}
		}
		return 0 // incomplete
	}
	return 0
}

// isParamByte reports whether c can appear in a CSI parameter/intermediate run for the
// query/report sequences we care about (digits, separators, private-mode & DEC markers,
// and a space intermediate for cursor-style reports).
func isParamByte(c byte) bool {
	return (c >= '0' && c <= '9') || c == ';' || c == ':' ||
		c == '?' || c == '>' || c == '=' || c == '$' || c == ' '
}
