package ptysess

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"partyline.sh/partyline/internal/brand"
)

// gateWriter wraps the host's terminal writer so the grant pop-up can take over
// the screen without the live program output painting over it: while the gate is
// open, normal writes are buffered; the modal draws via overlay (straight to the
// real terminal); on close we leave the alt screen and flush the buffer so the
// host's screen catches up exactly.
type gateWriter struct {
	mu      sync.Mutex
	dst     io.Writer
	open    bool
	buf     bytes.Buffer
	dropped int // bytes discarded by the ring cap while the gate was open
}

// gateMaxBuf caps what the gate holds while the modal is up. The buffer is a RING: past this many
// bytes the OLDEST are dropped. It used to be unbounded, so a chatty program behind an unanswered
// pop-up (a build, a test run, a `yes` loop) could hold arbitrarily much memory. Dropping is safe
// here because restore repaints from the vt snapshot — the vt is fed independently and saw every
// byte — so the buffer is a backstop, not the source of truth. dropped is kept for the record.
const gateMaxBuf = 256 << 10 // 256 KB

func (g *gateWriter) Write(p []byte) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.open {
		return g.dst.Write(p)
	}
	n, err := g.buf.Write(p)
	if over := g.buf.Len() - gateMaxBuf; over > 0 {
		g.buf.Next(over) // drop the oldest bytes — never grow past the cap
		g.dropped += over
	}
	return n, err
}

// droppedBytes reports how much output the ring cap discarded while the gate was open.
func (g *gateWriter) droppedBytes() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.dropped
}

// overlay writes straight to the terminal, bypassing the buffer — for the modal.
func (g *gateWriter) overlay(p []byte) {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, _ = g.dst.Write(p)
}

func (g *gateWriter) openGate() {
	g.mu.Lock()
	g.open, g.dropped = true, 0
	g.mu.Unlock()
}

// restore ends the overlay and repaints the screen from `snapshot` (the vt's
// current render). It discards the output buffered while the overlay was up —
// the snapshot already reflects those bytes (the vt is fed independently), so
// replaying them would only double-paint. This is reliable over a full-screen
// app (vim/claude), where leaving a nested alt-screen and hoping the program
// redraws is not.
func (g *gateWriter) restore(snapshot []byte) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.open {
		return
	}
	g.open = false
	g.buf.Reset()
	_, _ = g.dst.Write([]byte("\x1b[?25h")) // show cursor
	_, _ = g.dst.Write(snapshot)
}

var grantChoices = []string{"grant control", "deny"}

// grantAnswerTimeout bounds how long the pop-up may hold the host's keyboard. The modal is sticky —
// until it's answered the host cannot type at all — so an ignored request (host walked away, request
// arrived mid-build) used to lock the terminal indefinitely.
//
// It expires to DECLINE, never to grant. This is a permission prompt: the only safe reading of an
// unanswered question is "no", and declining costs the requester one more ask.
// A var, not a const, so tests can shorten it.
var grantAnswerTimeout = 60 * time.Second

// openGrantModal pops the sticky grant pop-up on the host when `req` asks for
// control. Falls back to a plain notice if there's no host terminal to draw to.
func (s *Session) openGrantModal(req *Participant) {
	s.mu.Lock()
	gate := s.hostGate
	if gate == nil {
		s.mu.Unlock()
		s.Notice(nil, req.Name+" requests control")
		return
	}
	s.granting, s.grantIdx, s.pendingReq = true, 0, req
	s.mu.Unlock()
	gate.openGate()
	gate.overlay([]byte("\a")) // one bell when the request arrives — not on every redraw
	s.renderGrantModal()
	go s.expireGrant(req)
}

// expireGrant auto-DECLINES req if the modal is still showing that same request after the timeout,
// so the host's keyboard always comes back. Identity, not a counter, decides staleness: if the host
// answered (or a newer request replaced this one) pendingReq no longer points at req and we're done.
func (s *Session) expireGrant(req *Participant) {
	time.Sleep(grantAnswerTimeout)
	s.mu.Lock()
	stale := !s.granting || s.pendingReq != req
	s.mu.Unlock()
	if stale {
		return
	}
	s.resolveGrant(false)
}

// handleGrantKey interprets the host's keystrokes while the modal is up:
// ↑↓/jk move, ⏎ act on the highlighted choice, g/y grant, n/d/esc deny. Other
// keys are ignored (sticky — the host must answer).
func (s *Session) handleGrantKey(b []byte) {
	for i := 0; i < len(b); i++ {
		c := b[i]
		if c == 0x1b { // arrow sequence or bare-esc (= deny/dismiss)
			// Arrows arrive as CSI (ESC [ A/B) or, in application-cursor mode —
			// which zsh's line editor turns on — as SS3 (ESC O A/B). Accept both.
			if i+2 < len(b) && (b[i+1] == '[' || b[i+1] == 'O') {
				if b[i+2] == 'A' || b[i+2] == 'B' {
					s.moveGrant()
				}
				i += 2
				continue
			}
			s.resolveGrant(false)
			return
		}
		switch c {
		case '\r', '\n':
			s.mu.Lock()
			grant := s.grantIdx == 0
			s.mu.Unlock()
			s.resolveGrant(grant)
			return
		case 'g', 'y', 'Y':
			s.resolveGrant(true)
			return
		case 'n', 'N', 'd':
			s.resolveGrant(false)
			return
		case 'j', 'k':
			s.moveGrant()
		}
	}
}

func (s *Session) moveGrant() {
	s.mu.Lock()
	s.grantIdx = (s.grantIdx + 1) % len(grantChoices)
	s.mu.Unlock()
	s.renderGrantModal()
}

// resolveGrant closes the modal and either grants the requester typing or tells
// them the host kept control.
func (s *Session) resolveGrant(grant bool) {
	s.mu.Lock()
	req := s.pendingReq
	gate := s.hostGate
	s.granting, s.pendingReq = false, nil
	s.mu.Unlock()
	if gate != nil {
		gate.restore(s.Snapshot()) // repaint the exact screen from the vt
	}
	if req == nil {
		return
	}
	if !grant {
		s.Notice(req, "the host kept control for now")
		s.Notice(s.hostPart(), "you kept control")
		return
	}
	switch _, on, res := s.GrantToggle(req.Name); res {
	case "ok":
		if on {
			s.Notice(nil, "host gave "+req.Name+" the keyboard")
		} else {
			s.Notice(nil, "host took the keyboard back from "+req.Name)
		}
	case "viewer":
		s.Notice(s.hostPart(), req.Name+" is view-only — promote to Full access (partyline team access <handle> full), then they rejoin")
	}
	s.refreshHUDs()
}

// hostPart returns the host participant (for host-only notices), or nil.
func (s *Session) hostPart() *Participant {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.parts {
		if p.IsHost {
			return p
		}
	}
	return nil
}

// renderGrantModal draws the centered pop-up to the host's terminal (via the
// gate's overlay, so it sits on top of the buffered program screen).
func (s *Session) renderGrantModal() {
	s.mu.Lock()
	gate := s.hostGate
	idx := s.grantIdx
	name := ""
	if s.pendingReq != nil {
		name = s.pendingReq.Name
	}
	cols, rows := 80, 24
	for _, p := range s.parts {
		if p.IsHost && p.Cols > 0 && p.Rows > 0 {
			cols, rows = p.Cols, p.Rows
			break
		}
	}
	s.mu.Unlock()
	if gate == nil || name == "" {
		return
	}

	const boxW = 34
	left := (cols - boxW) / 2
	if left < 0 {
		left = 0
	}
	top := (rows - 8) / 2
	if top < 1 {
		top = 1
	}
	pad := strings.Repeat(" ", left)
	var sb strings.Builder
	sb.WriteString("\x1b[?25l\x1b[2J") // hide cursor, clear (no alt-screen — we restore via snapshot)
	at := func(r int) { fmt.Fprintf(&sb, "\x1b[%d;1H", r) }
	at(top)
	sb.WriteString(pad + brand.Wordmark())
	at(top + 1)
	sb.WriteString(pad + fmt.Sprintf("\x1b[1;38;5;215m✋ %s wants control\x1b[0m", brand.ClipEllipsis(name, 18)))
	for i, ch := range grantChoices {
		at(top + 3 + i)
		marker := "   "
		style := "\x1b[38;5;250m"
		if i == idx {
			marker = " ▸ "
			style = "\x1b[1;48;5;236m\x1b[38;5;231m"
			if i == 1 { // deny highlighted
				style = "\x1b[1;48;5;52m\x1b[38;5;203m"
			}
		}
		fmt.Fprintf(&sb, "%s%s%s%s \x1b[0m", pad, style, marker, ch)
	}
	at(top + 6)
	// Naming the timeout is the point of having one: the host can see the keyboard comes back —
	// so it is ordered ahead of the y/n aliases, which HintBar will drop first on a narrow host.
	sb.WriteString(brand.IndentedHintBar("GRANT", []brand.Hint{
		{Key: "↑↓ jk", Label: "move"}, {Key: "⏎", Label: "select"},
		{Key: "esc", Label: fmt.Sprintf("declines in %ds", int(grantAnswerTimeout.Seconds()))},
		{Key: "y n", Label: "grant / deny"},
	}, left, cols))
	gate.overlay([]byte(sb.String()))
}
