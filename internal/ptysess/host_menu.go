package ptysess

import (
	"fmt"
	"strings"

	"partyline.sh/partyline/internal/brand"
)

// hostMenuItem is one row of the host command menu: a label and the chord key
// it dispatches through (so the menu and ctrl-\ <key> share one code path).
type hostMenuItem struct {
	label string
	chord byte
}

// The host's command menu (ctrl-\ ?). Each item maps to the SAME action as the
// host's fast chord (ctrl-\ <key>) so the menu and the muscle-memory accelerators
// can't drift — both dispatch through hostCommand. It renders as an alt-screen
// overlay through hostGate (like the grant pop-up), buffering program output
// while it's up, so it sits cleanly over a full-screen app underneath.
var hostMenuItems = []hostMenuItem{
	{"who's on the line", 'w'},
	{"open / close guest typing", 'g'},
	{"lock / unlock new joiners", 'l'},
	{"toggle session HUD", 'h'},
	{"end the session", 'q'},
}

// openHostMenu pops the command menu on the host. No-op (falls back to a notice)
// if there's no host terminal to draw to.
func (s *Session) openHostMenu() {
	s.mu.Lock()
	gate := s.hostGate
	dbg("openHostMenu gate=%v", gate != nil)
	if gate == nil {
		s.mu.Unlock()
		s.Notice(s.hostPart(), "ctrl-\\ then: w who · g guests · l lock · h HUD · q end · ctrl-\\ sends a literal ctrl-\\")
		return
	}
	s.hostMenu, s.hostMenuIdx = true, 0
	s.mu.Unlock()
	gate.openGate()
	s.renderHostMenu()
}

// handleHostMenuKey drives the menu while it's open: ↑↓/jk move, ⏎ run the
// highlighted item, a matching letter runs it directly, esc/q close. ctrl-\
// closes and sends a literal ctrl-\ to the app (so the host can still reach
// vim's ctrl-\ chords without leaving the menu in the way).
func (s *Session) handleHostMenuKey(b []byte) {
	for i := 0; i < len(b); i++ {
		c := b[i]
		if c == 0x1b { // CSI (ESC [ A/B) or SS3 (ESC O A/B) arrows, else bare-esc close
			if i+2 < len(b) && (b[i+1] == '[' || b[i+1] == 'O') {
				switch b[i+2] {
				case 'A':
					s.moveHostMenu(-1)
				case 'B':
					s.moveHostMenu(1)
				}
				i += 2
				continue
			}
			s.closeHostMenu()
			return
		}
		switch c {
		case '\r', '\n':
			s.mu.Lock()
			key := hostMenuItems[s.hostMenuIdx].chord
			s.mu.Unlock()
			s.chooseHostMenu(key)
			return
		case 'j':
			s.moveHostMenu(1)
		case 'k':
			s.moveHostMenu(-1)
		case PrefixKey: // ctrl-\ ctrl-\ → literal ctrl-\ to the program
			s.closeHostMenu()
			if s.cmd != nil {
				_, _ = s.ptmx.Write([]byte{PrefixKey})
			}
			return
		case 'q', 0x1b:
			s.closeHostMenu()
			return
		default:
			for _, it := range hostMenuItems {
				if c == it.chord {
					s.chooseHostMenu(it.chord)
					return
				}
			}
		}
	}
}

func (s *Session) moveHostMenu(d int) {
	s.mu.Lock()
	s.hostMenuIdx = (s.hostMenuIdx + d + len(hostMenuItems)) % len(hostMenuItems)
	s.mu.Unlock()
	s.renderHostMenu()
}

// chooseHostMenu closes the menu, then runs the picked command. The close (and
// repaint nudge) happen first so the program's screen is restored before any
// notice the command emits.
func (s *Session) chooseHostMenu(key byte) {
	s.closeHostMenu()
	s.hostCommand(key)
}

// closeHostMenu ends the overlay and repaints the exact screen from the vt
// snapshot — reliable over a full-screen app (vim/claude), where leaving a
// nested alt-screen and hoping the program redraws is not.
func (s *Session) closeHostMenu() {
	s.mu.Lock()
	gate := s.hostGate
	open := s.hostMenu
	s.hostMenu = false
	s.mu.Unlock()
	if !open || gate == nil {
		return
	}
	gate.restore(s.Snapshot())
}

// hostCommand runs a host action by its chord key. This is the single source of
// truth for what each host command does — both the fast chord (HandleInput's
// prefix switch) and the menu route here, so they can't drift.
func (s *Session) hostCommand(key byte) {
	host := s.hostPart()
	switch key {
	case 'w':
		s.Notice(host, "on the session: "+s.Who())
	case 'g':
		if s.ToggleGuests() {
			s.Notice(nil, "host opened the session — guests can type")
		} else {
			s.Notice(nil, "host locked the session — guests are view-only")
		}
	case 'l':
		if s.ToggleLock() {
			s.Notice(nil, "host locked the session — no new joiners")
		} else {
			s.Notice(nil, "host unlocked the session — joiners welcome")
		}
	case 'h':
		if host == nil {
			return
		}
		s.mu.Lock()
		host.hud = !host.hud
		on := host.hud
		s.mu.Unlock()
		if on {
			s.showHUD(host)
			s.Notice(host, "session HUD on (ctrl-\\ h to stop)")
		} else {
			s.Notice(host, "session HUD off")
		}
	case 'q':
		s.Notice(nil, "host ended the session")
		s.End()
	}
}

// renderHostMenu draws the centered command menu to the host terminal via the
// gate overlay (on top of the buffered program screen).
func (s *Session) renderHostMenu() {
	s.mu.Lock()
	gate := s.hostGate
	idx := s.hostMenuIdx
	cols, rows := 80, 24
	for _, p := range s.parts {
		if p.IsHost && p.Cols > 0 && p.Rows > 0 {
			cols, rows = p.Cols, p.Rows
			break
		}
	}
	s.mu.Unlock()
	if gate == nil {
		return
	}

	const boxW = 34
	left := (cols - boxW) / 2
	if left < 0 {
		left = 0
	}
	top := (rows - (len(hostMenuItems) + 5)) / 2
	if top < 1 {
		top = 1
	}
	pad := strings.Repeat(" ", left)
	var sb strings.Builder
	sb.WriteString("\x1b[?25l\x1b[2J") // hide cursor, clear (no alt-screen — we restore via snapshot)
	at := func(r int) { fmt.Fprintf(&sb, "\x1b[%d;1H", r) }
	at(top)
	sb.WriteString(pad + brand.Wordmark() + "  \x1b[38;5;245m· host\x1b[0m")
	for i, it := range hostMenuItems {
		at(top + 2 + i)
		if i == idx {
			sb.WriteString(pad + "\x1b[1;48;5;236m\x1b[38;5;231m ▸ " + it.label + " \x1b[0m")
		} else {
			sb.WriteString(pad + "\x1b[38;5;250m   " + it.label + "\x1b[0m")
		}
	}
	at(top + 3 + len(hostMenuItems))
	// Same hint bar every other picker in the app wears — it also names the two exits (esc, q)
	// and the mnemonic letters that handleHostMenuKey has always accepted but never advertised.
	sb.WriteString(brand.IndentedHintBar("HOST", brand.PickerHints(), left, cols))
	dbg("renderHostMenu cols=%d rows=%d top=%d bytes=%d", cols, rows, top, sb.Len())
	gate.overlay([]byte(sb.String()))
}
