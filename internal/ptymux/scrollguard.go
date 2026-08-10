package ptymux

import (
	"bytes"
	"fmt"
)

// scrollguard.go — keeping the status bar on screen.
//
// THE BUG THIS EXISTS FOR. The mux reserves the bottom row for its bar and tells the child the
// terminal is one row shorter (bodySize → pty.Setsize). That controls where the child AIMS, but it
// does not stop the PHYSICAL terminal from scrolling: when the child prints a newline at the bottom
// of its screen, the real terminal scrolls every row up by one — including our reserved row. The bar
// scrolls away, and nothing paints it again until the next ctrl-\ or tab switch. Which is exactly
// what you see: summon the ribbon, type one line, it's gone.
//
// The only thing that actually pins a row is a SCROLL REGION (DECSTBM). Set the region to
// 1..bodyRows and the terminal will not scroll the last row, whatever the child emits.
//
// The complication is that the child sets DECSTBM too — Claude Code pins its own input box with it,
// which is what made bug #238 subtle. So we can't just set ours once and forget: the child's writes
// pass straight through to the terminal and the LAST write wins.
//
// So we MEDIATE rather than observe. Every child sequence that would hand the last row back to the
// scrolling area is rewritten on its way to the terminal:
//
//   - ESC[r          full reset — would restore the full-height region. Rewritten to 1..bodyRows.
//   - ESC[{t};{b}r   an explicit region whose bottom margin reaches past bodyRows (a child that
//                    ignored the winsize, or a stale value from before a resize). Bottom clamped.
//
// A child that respects its size emits ESC[1;{bodyRows}r or something inside it, which is already
// safe and passes through untouched. This is a narrow rewrite of two shapes, not a filter on the
// stream.

// clampScrollRegion rewrites DECSTBM sequences in b so the bottom margin never exceeds bodyRows,
// returning the original slice when nothing needed changing (the overwhelmingly common case — no
// allocation on a normal write).
//
// KNOWN LIMIT, deliberate: a sequence split across two writes is not recognised, the same limitation
// modeState.observe() carries. A torn DECSTBM would slip through and un-pin the bar until the next
// one arrives — which the bar's own re-assert (see barKeepalive) then repairs. Buffering partial
// sequences would mean holding child output back, and latency on a live terminal is a worse trade
// than a rare one-frame slip.
func clampScrollRegion(b []byte, bodyRows int) []byte {
	if bodyRows < 1 || !bytes.Contains(b, []byte{0x1b, '['}) {
		return b
	}
	var out []byte // nil until the first rewrite — untouched input keeps its original slice
	last := 0
	for i := 0; i+1 < len(b); i++ {
		if b[i] != 0x1b || b[i+1] != '[' {
			continue
		}
		// Scan the parameter run: digits and ';' only. Anything else ends it.
		j := i + 2
		for j < len(b) && (b[j] == ';' || (b[j] >= '0' && b[j] <= '9')) {
			j++
		}
		if j >= len(b) || b[j] != 'r' {
			continue // not DECSTBM (and a '?' private mode never reaches here — it isn't a digit)
		}
		params := string(b[i+2 : j])
		top, bot, ok := parseRegion(params)
		if !ok {
			continue
		}
		if bot > 0 && bot <= bodyRows {
			continue // already inside the body — the child is behaving, leave it alone
		}
		if out == nil {
			out = make([]byte, 0, len(b)+8)
		}
		out = append(out, b[last:i]...)
		out = append(out, []byte(fmt.Sprintf("\x1b[%d;%dr", top, bodyRows))...)
		last = j + 1
		i = j
	}
	if out == nil {
		return b
	}
	return append(out, b[last:]...)
}

// parseRegion reads DECSTBM parameters. "" (ESC[r) is the full-reset form and reports bottom 0,
// which the caller treats as "reaches the last row" — that reset is the main way the bar gets
// un-pinned. A malformed parameter run is reported not-ok and passes through untouched: guessing at
// a sequence we don't understand is how you corrupt a terminal.
func parseRegion(params string) (top, bot int, ok bool) {
	if params == "" {
		return 1, 0, true // ESC[r — reset to the whole screen
	}
	parts := bytes.Split([]byte(params), []byte{';'})
	if len(parts) != 2 {
		return 0, 0, false
	}
	num := func(p []byte) (int, bool) {
		if len(p) == 0 {
			return 0, true // an omitted parameter means "default"
		}
		n := 0
		for _, c := range p {
			if c < '0' || c > '9' {
				return 0, false
			}
			n = n*10 + int(c-'0')
			if n > 1<<20 { // absurd value — refuse rather than overflow
				return 0, false
			}
		}
		return n, true
	}
	t, okT := num(parts[0])
	b, okB := num(parts[1])
	if !okT || !okB {
		return 0, 0, false
	}
	if t < 1 {
		t = 1
	}
	return t, b, true
}

// scrollRegionFor is the region that protects the bar: the whole body, last row excluded.
func scrollRegionFor(bodyRows int) []byte {
	if bodyRows < 1 {
		bodyRows = 1
	}
	return []byte(fmt.Sprintf("\x1b[1;%dr", bodyRows))
}
