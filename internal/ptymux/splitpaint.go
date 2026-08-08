package ptymux

// Split view, painting: one frame for both panes, and the ticker that keeps the UNFOCUSED
// pane streaming. The frame builder itself (splitFrame) is pure and lives in split.go.

import (
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
	"partyline.sh/partyline/internal/brand"
)

// paintSplit renders both panes and writes one frame. Session data is gathered outside outMu
// (only the write is held), so it can never nest the session lock inside the output lock.
func (mx *Mux) paintSplit() {
	mx.mu.Lock()
	st := mx.split
	cols, rows := mx.cols, mx.rows
	mx.mu.Unlock()
	if st == nil {
		return
	}
	leftW, rightW, bodyRows, ok := splitGeom(cols, rows)
	if !ok {
		return
	}
	first, second := st.pane(0), st.pane(1)
	firstSide := 0
	if st.zoomed() { // one visible pane, always the focused one
		first, second, firstSide = st.focusPane(), nil, st.focusIdx()
		leftW, rightW = cols, 0
	}
	lv := mx.paneViewOf(first, firstSide, leftW, bodyRows, first == st.focusPane())
	var rv paneView
	if second != nil {
		rv = mx.paneViewOf(second, 1, rightW, bodyRows, second == st.focusPane())
	}
	frame := splitFrame(lv, rv, leftW, rightW, bodyRows, rows)
	st.lastPaint.Store(time.Now().UnixNano())
	mx.outMu.Lock()
	if !st.dead.Load() { // serialized against every other repaint — see splitState.dead
		os.Stdout.Write(wrapSync(frame))
	}
	mx.outMu.Unlock()
}

// paneViewOf renders one pane's body: the session's own emulator at pane width, or the in-pane
// manager's position-independent lines. A manager keeps the cursor hidden (it draws its own),
// and an unfocused one is dimmed so the pane awaiting a pick is visibly the recessed one.
func (mx *Mux) paneViewOf(p *pane, side, w, h int, focused bool) paneView {
	if p == nil {
		return paneView{}
	}
	if p.ch == nil {
		var lines []string
		if p.home != nil {
			lines = p.home.RenderLines(w, h)
		}
		if !focused {
			lines = dimLines(lines)
		}
		return paneView{title: p.title(side), lines: lines, focused: focused, modes: []byte("\x1b[?25l")}
	}
	// The live screen at the pane's OWN width: the session was attached (and stays resized) to
	// pane geometry for as long as its pair is bound — parked included — so a re-entry replays
	// exactly these rows with no resize round-trip through the child and no screen-width bleed.
	lines, _ := p.ch.sess.ScrollViewport(0, h)
	col, row := p.ch.sess.CursorPos()
	return paneView{title: p.title(side), lines: lines, focused: focused, curCol: col, curRow: row,
		modes: p.ch.gate.restoreModes()}
}

// splitLoop repaints at ~15fps whenever either pane's session produced output since the last
// frame, so the UNFOCUSED pane streams live too. Split mode has no raw passthrough, so this is
// the only paint path; it stands down while an overlay (command panel, quit prompt) owns rows.
// Manager panes are event-driven (repainted on the keystroke), so they never drive a tick.
func (mx *Mux) splitLoop(st *splitState) {
	tick := time.NewTicker(66 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-st.stop:
			return
		case <-tick.C:
			mx.mu.Lock()
			gone := mx.split != st
			idle := mx.mode == modeLive && !mx.confirming && !mx.barActive && !mx.scrolling && mx.pfxCh == nil
			mx.mu.Unlock()
			if gone {
				return
			}
			last := st.lastPaint.Load()
			fresh := false
			for _, i := range [2]int{0, 1} {
				p := st.pane(i)
				if p.ch != nil && p.ch.gate.lastOut.Load() > last {
					fresh = true
				}
			}
			if !idle || !fresh {
				continue
			}
			mx.paintSplit()
		}
	}
}

// dimLines recesses a pane that is WAITING its turn: its own colours are stripped and the whole
// body is redrawn in one dim grey, so the pane the user should act in is the only bright one.
// Only ever applied to an unfilled manager pane — a live child's output is never recoloured.
func dimLines(lines []string) []string {
	out := make([]string, len(lines))
	for i, ln := range lines {
		out[i] = splitDimFg + stripSGR(ln)
	}
	return out
}

// stripSGR removes every SGR escape from s (pane rows carry nothing else — see PaneHome).
func stripSGR(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j := i + 1
			for j < len(s) && s[j] != 'm' {
				j++
			}
			if j < len(s) {
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// clipPadANSI clips s to exactly w display columns (ANSI escapes cost nothing) and space-pads
// the rest, so a pane can never bleed into its neighbour and stale glyphs are always erased.
func clipPadANSI(s string, w int) string {
	if w <= 0 {
		return ""
	}
	var b strings.Builder
	vis := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j := i + 1
			for j < len(s) && s[j] != 'm' {
				j++
			}
			if j < len(s) {
				j++
			}
			b.WriteString(s[i:j])
			i = j
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		rw := runewidth.RuneWidth(r)
		if vis+rw > w {
			break
		}
		b.WriteString(s[i : i+size])
		vis += rw
		i += size
	}
	if vis < w {
		b.WriteString("\x1b[0m" + strings.Repeat(" ", w-vis))
	}
	return b.String()
}

func clamp(v, lo, hi int) int {
	if hi < lo {
		hi = lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func lineAt(lines []string, i int) string {
	if i < len(lines) {
		return lines[i]
	}
	return ""
}

// paneTitle renders one pane's title bar segment: brand pink when focused, dim otherwise.
func paneTitle(s string, w int, focused bool) string {
	if focused {
		return brand.PillBg() + "\x1b[1;38;5;16m" + clipPadPlain("▸ "+s, w) + "\x1b[0m"
	}
	return barIdleBg + "\x1b[38;5;245m" + clipPadPlain("  "+s, w) + "\x1b[0m"
}
