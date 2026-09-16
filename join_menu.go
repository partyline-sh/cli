package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"sync"

	"partyline.sh/partyline/internal/brand"
	"partyline.sh/partyline/internal/ptysess"
	"partyline.sh/partyline/internal/wormhole"
)

// joinMenu is the joiner's local command palette. Pressing ctrl-\ pops a small
// menu of the actions a viewer can take; the choice is sent to the host as the
// SAME ctrl-\<letter> chord the host already understands — so there are zero
// host-side changes and the viewer-can't-drive boundary is untouched.
//
// It renders LOCALLY (instant, no relay round-trip) in the alternate screen, so
// it overlays cleanly over a full-screen app (vim/claude) and restores it on
// close. While it's open, incoming host output is buffered (out) instead of
// painted over the menu, then flushed when the menu closes — no lost output.
type joinMenu struct {
	mu      sync.Mutex
	open    bool
	idx     int
	buf     bytes.Buffer // host output captured while the menu is up
	size    func() (cols, rows int)
	repaint func() // nudge the host to redraw (resize) so a full-screen app is clean on close
}

const joinPrefixKey = 0x1c // ctrl-\

type menuItem struct {
	label string
	chord byte
}

// The commands a joiner can invoke. (Host-only actions — grant/lock/end — aren't
// shown; the host gates them anyway.) Each maps to the host's existing chord.
var joinMenuItems = []menuItem{
	{"who's on the line", 'w'},
	{"request control", 'r'},
	{"toggle session HUD", 'h'},
	{"leave the session", 'd'},
}

// out is what the output goroutine calls instead of os.Stdout.Write: it buffers
// while the menu is open (so host output can't clobber the menu) and writes
// through otherwise.
func (m *joinMenu) out(p []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.open {
		m.buf.Write(p)
		return
	}
	os.Stdout.Write(p)
}

// feed processes a chunk of stdin. Outside the menu it forwards keystrokes to the
// host, opening the menu when it sees ctrl-\. Inside the menu it interprets
// navigation locally and never forwards. send is the host-input writer.
func (m *joinMenu) feed(b []byte, send func(wormhole.FrameType, []byte)) {
	// In CSI-u / modifyOtherKeys mode (vim on a capable terminal) ctrl-\ arrives
	// as an escape sequence, not 0x1c — normalize so the palette still opens.
	b = ptysess.NormalizeCtrlBackslash(b)
	m.mu.Lock()
	open := m.open
	m.mu.Unlock()
	if open {
		m.menuKeys(b, send)
		return
	}
	i := bytes.IndexByte(b, joinPrefixKey)
	if i < 0 {
		send(wormhole.FrameInput, b)
		return
	}
	if i > 0 {
		send(wormhole.FrameInput, b[:i]) // forward anything typed before the prefix
	}
	m.openMenu()
	if rest := b[i+1:]; len(rest) > 0 {
		m.menuKeys(rest, send) // keys batched with the prefix (e.g. fast ctrl-\ w)
	}
}

// menuKeys interprets navigation while the menu is open: ↑↓/jk move, ⏎ select,
// a matching letter selects directly, esc / ctrl-\ / q close.
func (m *joinMenu) menuKeys(b []byte, send func(wormhole.FrameType, []byte)) {
	for i := 0; i < len(b); i++ {
		c := b[i]
		if c == 0x1b { // ESC: arrow sequence or bare-esc close
			// CSI (ESC [ A/B) or SS3 (ESC O A/B, application-cursor mode) — accept both.
			if i+2 < len(b) && (b[i+1] == '[' || b[i+1] == 'O') {
				switch b[i+2] {
				case 'A':
					m.move(-1)
				case 'B':
					m.move(1)
				}
				i += 2
				continue
			}
			m.closeMenu()
			return
		}
		switch c {
		case '\r', '\n':
			m.choose(joinMenuItems[m.curIdx()].chord, send)
			return
		case 'k':
			m.move(-1)
		case 'j':
			m.move(1)
		case joinPrefixKey: // ctrl-\ ctrl-\ → send a literal ctrl-\ to the app
			// The host collapses a doubled prefix into one literal ctrl-\ byte to
			// the pty, so a contributor driving vim can still reach ctrl-\ chords.
			m.closeMenu()
			send(wormhole.FrameInput, []byte{joinPrefixKey, joinPrefixKey})
			return
		case 'q':
			m.closeMenu()
			return
		default:
			for _, it := range joinMenuItems {
				if c == it.chord {
					m.choose(it.chord, send)
					return
				}
			}
		}
	}
}

func (m *joinMenu) curIdx() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.idx
}

func (m *joinMenu) move(d int) {
	m.mu.Lock()
	m.idx = (m.idx + d + len(joinMenuItems)) % len(joinMenuItems)
	open := m.open
	m.mu.Unlock()
	if open {
		m.render()
	}
}

func (m *joinMenu) openMenu() {
	m.mu.Lock()
	m.open, m.idx = true, 0
	m.mu.Unlock()
	m.render()
}

// closeMenu leaves the alt screen and flushes any host output captured while it
// was open, so the live screen catches up exactly.
func (m *joinMenu) closeMenu() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.open {
		return
	}
	m.open = false
	os.Stdout.WriteString("\x1b[?25h") // show cursor
	// Discard output buffered while the menu was up and ask the host for a fresh
	// full-screen snapshot — it already reflects those bytes, and a snapshot
	// repaints cleanly over a full-screen app (vim/claude) where a nested
	// alt-screen flip does not.
	m.buf.Reset()
	if m.repaint != nil {
		m.repaint()
	}
}

// choose closes the menu, then emits the host chord (ctrl-\<letter>) for the
// picked command — reusing the host's existing command handling.
func (m *joinMenu) choose(chord byte, send func(wormhole.FrameType, []byte)) {
	m.closeMenu()
	send(wormhole.FrameInput, []byte{joinPrefixKey, chord})
}

// render draws the menu centered in the alternate screen.
func (m *joinMenu) render() {
	cols, rows := 80, 24
	if m.size != nil {
		if c, r := m.size(); c > 0 && r > 0 {
			cols, rows = c, r
		}
	}
	m.mu.Lock()
	idx := m.idx
	m.mu.Unlock()

	const boxW = 36
	left := (cols - boxW) / 2
	if left < 0 {
		left = 0
	}
	top := (rows - (len(joinMenuItems) + 4)) / 2
	if top < 1 {
		top = 1
	}
	pad := strings.Repeat(" ", left)
	var sb strings.Builder
	sb.WriteString("\x1b[?25l\x1b[2J") // hide cursor, clear (no alt-screen — host repaints via snapshot)
	at := func(row int) { fmt.Fprintf(&sb, "\x1b[%d;1H", row) }

	at(top)
	sb.WriteString(pad + brand.Wordmark())
	for i, it := range joinMenuItems {
		at(top + 2 + i)
		if i == idx {
			sb.WriteString(pad + "\x1b[1;48;5;236m\x1b[38;5;231m ▸ " + it.label + " \x1b[0m")
		} else {
			sb.WriteString(pad + "\x1b[38;5;250m   " + it.label + "\x1b[0m")
		}
	}
	at(top + 3 + len(joinMenuItems))
	sb.WriteString(brand.IndentedHintBar("JOIN", brand.PickerHints(), left, cols))
	os.Stdout.WriteString(sb.String())
}
