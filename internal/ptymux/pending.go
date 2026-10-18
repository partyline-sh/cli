package ptymux

// pending.go — holding a terminal report that hasn't finished arriving.
//
// THE BUG THIS EXISTS FOR. matchTerminalReport answers "does b START with a complete terminal
// reply?" and returns 0 otherwise — but 0 conflates two very different situations: "this is not a
// report" and "this IS a report and the rest hasn't arrived yet". handleInput treats both the same
// way and forwards the bytes to the child, so a split report gets typed into the prompt.
//
// For short replies (DSR, DA) that was survivable: they fit in one read, so they never split. An
// OSC 52 CLIPBOARD report does not. Selecting a screenful of text and letting the terminal answer
// with it produces several KB of base64 — far past a single stdin read — so the first chunk always
// fell through as input. Select text, get a wall of base64 in your composer. Every time, guaranteed,
// because it is a function of size rather than timing.
//
// So the tail of an unterminated report is HELD and prepended to the next read.
//
// ONLY OSC AND DCS ARE HELD, deliberately. Those two are unambiguous — no keyboard key encodes as
// ESC ] or ESC P (the same reasoning matchTerminalReport already relies on), so holding them can
// never delay something the human typed. A bare trailing ESC is NOT held: that is the Escape key,
// and making Escape wait for the next read would break every "esc to cancel" in every child. CSI
// replies are not held either — they are a couple of dozen bytes and do not split in practice,
// and holding them would risk swallowing real keys for the same reason.
const maxHeldReport = 1 << 20 // 1 MiB — a clipboard can be large; past this, give up and forward

// pendingReportTail returns how many bytes at the END of b belong to an INCOMPLETE OSC/DCS report
// that must be held back until its terminator arrives. 0 means nothing to hold.
//
// It scans backward to the last ESC because a report's own ST terminator (ESC \) is itself an ESC:
// finding that one, and seeing it is not an OSC/DCS introducer, is what tells us the report already
// completed. Beyond maxHeldReport we stop holding and let the bytes through — a bounded amount of
// garbage beats an unbounded buffer and a wedged terminal.
func pendingReportTail(b []byte) int {
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] != 0x1b {
			continue
		}
		if len(b)-i > maxHeldReport {
			return 0 // runaway: stop holding rather than buffer forever
		}
		if i+1 >= len(b) {
			return 0 // a bare trailing ESC is the Escape key, not the start of a report
		}
		if b[i+1] != ']' && b[i+1] != 'P' {
			return 0 // not an OSC/DCS introducer (an ST's "ESC \" lands here, meaning: complete)
		}
		if matchTerminalReport(b[i:]) > 0 {
			return 0 // terminated — matchTerminalReport will consume it normally
		}
		return len(b) - i
	}
	return 0
}
