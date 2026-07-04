// Package ptysess: the shared-shell core. One real pty, one real program
// (your $SHELL, or anything), mirrored byte-for-byte to every participant.
// partyline interprets nothing except the prefix key (ctrl-\).
package ptysess

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"

	"partyline.sh/partyline/internal/obs"
)

// emailRE is a deliberately conservative check — the server validates strictly;
// this just stops obviously-bad input (control chars, overlong strings) from
// leaving the engine layer. (audit M2)
var emailRE = regexp.MustCompile(`^[^\s@]{1,64}@[^\s@]{1,255}\.[^\s@]{2,}$`)

const (
	PrefixKey  = 0x1C // ctrl-\
	outBufSize = 256
)

type Participant struct {
	ID         int64
	Name       string
	IsHost     bool
	FullAccess bool // verified full-access user — only these may ever be granted typing
	CanType    bool
	Cols       int
	Rows       int
	out        chan []byte
	w          io.Writer
	sawPfx     bool      // prefix-key state machine
	lineBuf    []byte    // chars typed since last newline — for /p command detection
	hud        bool      // /phud on: auto-resend the session HUD on every change
	lastBell   time.Time // rate-limit the view-only "you can't type" bell
}

type Session struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	ptmx   *os.File
	parts  map[int64]*Participant
	nextID int64
	// vt is the authoritative server-side screen, fed by every byte of program
	// output. A late joiner gets a clean render of it (see snapshotLocked) instead
	// of a raw byte replay — no garble, no repaint hack. Also the basis for a
	// future web "watch this session" viewer.
	vt          *vt.SafeEmulator
	open        bool                // open line: full-access guests type freely
	locked      bool                // locked: reject new joiners (host /plock)
	driver      string              // who's currently typing (the "driver") — for the HUD
	driverAt    time.Time           // last keystroke time from the driver; drives idle-revert
	seenDrivers map[string]struct{} // every distinct person who took the keyboard (→ driver_count, #21)
	hostName    string
	Done        chan struct{}
	// Grant pop-up: when a guest requests control, the host gets a sticky
	// alt-screen modal (rendered through hostGate, which buffers normal output
	// while it's up). granting/grantIdx/pendingReq are guarded by s.mu.
	hostGate   *gateWriter
	granting   bool
	grantIdx   int          // 0 = grant, 1 = deny
	pendingReq *Participant // who asked for control
	// Host command menu (ctrl-\ ?): the navigable overlay of host actions,
	// rendered through hostGate like the grant pop-up. Guarded by s.mu.
	hostMenu    bool
	hostMenuIdx int
	// OnPresence fires (async) on every join/leave — wired by shell.go to push
	// presence to the control plane. Set once, before guests can join.
	OnPresence func()
	// OnInvite sends a control-plane invite; returns a status line for the Notice.
	// nil = line not registered.
	OnInvite func(target string) string
}

func New(argv []string, hostName string, openLine bool) (*Session, error) {
	return NewIn("", argv, hostName, openLine, nil)
}

// NewIn is New but starts the program in dir (its working directory). dir=="" inherits
// the current process's cwd. env, if non-nil, is the child's FULL environment (the caller
// builds it per-child — e.g. the mux gives each session its own PARTYLINE_THREAD_ID rather
// than a shared global); nil inherits os.Environ()+PARTYLINE=1.
func NewIn(dir string, argv []string, hostName string, openLine bool, env []string) (*Session, error) {
	c := exec.Command(argv[0], argv[1:]...)
	if env != nil {
		c.Env = env
	} else {
		c.Env = append(os.Environ(), "PARTYLINE=1")
	}
	if dir != "" {
		c.Dir = dir
	}
	ptmx, err := pty.Start(c)
	if err != nil {
		return nil, err
	}
	s := &Session{
		cmd:         c,
		ptmx:        ptmx,
		parts:       make(map[int64]*Participant),
		vt:          vt.NewSafeEmulator(80, 24), // resized to the clamped size on first attach
		open:        openLine,
		hostName:    hostName,
		seenDrivers: make(map[string]struct{}),
		Done:        make(chan struct{}),
	}
	go s.readLoop()
	go s.driverLoop()
	// The vt screen-model emulator AUTO-ANSWERS terminal queries the hosted program
	// emits (cursor-position / device-status — e.g. vim/claude/htop send "\033[6n")
	// by writing the reply into an internal, unbuffered io.Pipe. partyline doesn't
	// use those replies (the user's real terminal answers the program), but if that
	// pipe is never drained the emulator BLOCKS inside Write — and Write runs under
	// s.mu in broadcast(), so the whole session deadlocks: full-screen apps freeze
	// and input dies (HandleInput→markDriver also needs s.mu). Drain + discard so
	// Write can never block. Root cause: real freeze dump, 2026-06-06.
	go func() {
		defer obs.Guard("ptysess.vtdrain")
		_, _ = io.Copy(io.Discard, s.vt)
	}()
	go func() {
		_ = c.Wait()
		close(s.Done)
	}()
	return s, nil
}

func (s *Session) readLoop() {
	defer obs.Guard("ptysess.readLoop")
	buf := make([]byte, 32*1024)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			b := make([]byte, n)
			copy(b, buf[:n])
			s.broadcast(b)
		}
		if err != nil {
			return
		}
	}
}

// broadcast feeds program output to the server-side screen model and every
// participant. Feeding vt and fanning out under the same lock that Attach takes
// makes snapshot-then-subscribe atomic: a joiner's snapshot reflects exactly the
// bytes broadcast before it attached, and live bytes queue strictly after.
func (s *Session) broadcast(b []byte) {
	s.mu.Lock()
	_, _ = s.vt.Write(b)
	for _, p := range s.parts {
		select {
		case p.out <- b:
		default: // never let one slow viewer stall the session
		}
	}
	s.mu.Unlock()
}

// snapshotLocked renders the current screen as a self-contained repaint a fresh
// terminal can apply: enter alt-screen if the program is in it, clear+home, paint
// the grid (vt.Render uses LF-only line ends, so rewrite to CRLF or rows stair-
// step in raw mode), then restore the cursor. Caller holds s.mu.
func (s *Session) snapshotLocked() []byte {
	var b strings.Builder
	if s.vt.IsAltScreen() {
		b.WriteString("\x1b[?1049h")
	}
	b.WriteString("\x1b[2J\x1b[H")
	b.WriteString(strings.ReplaceAll(s.vt.Render(), "\n", "\r\n"))
	b.WriteString("\x1b[0m")
	pos := s.vt.CursorPosition()
	fmt.Fprintf(&b, "\x1b[%d;%dH", pos.Y+1, pos.X+1)
	return []byte(b.String())
}

// Snapshot returns the current screen as a repaint (the same render joiners get).
// Exposed for the future web viewer / WS stream.
func (s *Session) Snapshot() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

// IsAltScreen reports whether the program is currently in the alternate screen (vim/htop):
// alt-screen has no scrollback, so the mux paints it plainly, while a main-screen program
// (claude, a shell) gets its scrollback replayed into the terminal's native buffer.
func (s *Session) IsAltScreen() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.vt.IsAltScreen()
}

// SnapshotHistory renders the program's scrollback (up to the maxLines most-recent lines)
// followed by the current screen as a SINGLE CONTINUOUS stream — no interior screen-clear.
// A caller that has switched to the main screen and homed the cursor on a cleared screen can
// write this and end up with the scrollback in the terminal's NATIVE buffer (scrolled above)
// and the current screen visible — so native mouse-scroll + drag-copy show exactly THIS
// session's history, per session, with no cross-session bleed. Ends by positioning the cursor.
//
// The screen is padded to the full grid height so it fills the visible area (scrollback sits
// strictly above it) and the cursor address stays screen-relative. For an alt-screen program
// (no scrollback) this degrades to the normal Snapshot. maxLines<=0 means "no scrollback".
func (s *Session) SnapshotHistory(maxLines int) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.vt.IsAltScreen() {
		return s.snapshotLocked()
	}
	var b strings.Builder
	// Scrollback (capped to the most-recent maxLines), each line CRLF-terminated so it scrolls
	// up into the native buffer as we write.
	sbLen := s.vt.ScrollbackLen()
	start := 0
	if maxLines > 0 && sbLen > maxLines {
		start = sbLen - maxLines
	}
	if sb := s.vt.Scrollback(); sb != nil && maxLines > 0 {
		for v := start; v < sbLen; v++ {
			if ln := sb.Line(v); ln != nil {
				b.WriteString(ln.Render())
			}
			b.WriteString("\r\n")
		}
	}
	// The screen: exactly `height` rows, continuous (CRLF BETWEEN lines, none after the last so
	// no extra scroll), so it occupies the whole visible area with the scrollback above it.
	screen := strings.Split(s.vt.Render(), "\n")
	h := s.vt.Height()
	for len(screen) < h {
		screen = append(screen, "")
	}
	if len(screen) > h {
		screen = screen[:h]
	}
	for i, row := range screen {
		if i > 0 {
			b.WriteString("\r\n")
		}
		b.WriteString(row)
	}
	b.WriteString("\x1b[0m")
	pos := s.vt.CursorPosition()
	fmt.Fprintf(&b, "\x1b[%d;%dH", pos.Y+1, pos.X+1)
	return []byte(b.String())
}

// ScrollbackLen returns the number of lines the emulator has captured above the top
// of the current screen. 0 means there's nothing to scroll back to. Read under s.mu
// (the same lock broadcast feeds the vt under) so it can't race the live stream.
func (s *Session) ScrollbackLen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.vt.ScrollbackLen()
}

// ScrollViewport renders a height-row window of the virtual buffer
// [scrollback…  ++  current screen], `off` lines up from the live bottom (off=0 is
// the live screen). Returns the rendered rows (ANSI, one per visible row) plus the
// current scrollback length so the caller can clamp its offset. Done under s.mu so
// the scrollback can't be mutated mid-read (the lib's *Scrollback is shared state).
func (s *Session) ScrollViewport(off, height int) (rows []string, sbLen int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sbLen = s.vt.ScrollbackLen()
	if off > sbLen {
		off = sbLen
	}
	if off < 0 {
		off = 0
	}
	screen := strings.Split(s.vt.Render(), "\n")
	sb := s.vt.Scrollback()
	top := sbLen - off // first virtual line index shown
	rows = make([]string, height)
	for r := 0; r < height; r++ {
		v := top + r
		switch {
		case v < 0 || v >= sbLen+len(screen):
			// past the end (only when off==0 and the screen is shorter than height)
		case v < sbLen:
			if sb != nil {
				if ln := sb.Line(v); ln != nil {
					rows[r] = ln.Render()
				}
			}
		default:
			rows[r] = screen[v-sbLen]
		}
	}
	return rows, sbLen
}

// sanitizeNotice strips control chars (incl. ESC 0x1b) so participant-supplied
// strings — ssh usernames, /p args — can't inject terminal escape sequences
// into other participants' screens. (audit M3)
func sanitizeNotice(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 0x20 && r != 0x7f {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Notice writes a partyline status line to participants' terminals (never to
// the program). to==nil means everyone.
func (s *Session) Notice(to *Participant, msg string) {
	b := []byte("\r\n\x1b[2m[partyline] " + sanitizeNotice(msg) + "\x1b[0m\r\n")
	if to != nil {
		select {
		case to.out <- b:
		default:
		}
		return
	}
	s.mu.Lock()
	for _, p := range s.parts {
		select {
		case p.out <- b:
		default:
		}
	}
	s.mu.Unlock()
}

// noticeOthers sends a partyline status line to everyone EXCEPT `except` — so a
// joiner gets a single self-addressed onboarding line instead of also seeing the
// "X joined" broadcast about themselves.
func (s *Session) noticeOthers(except *Participant, msg string) {
	b := []byte("\r\n\x1b[2m[partyline] " + sanitizeNotice(msg) + "\x1b[0m\r\n")
	s.mu.Lock()
	for _, p := range s.parts {
		if p == except {
			continue
		}
		select {
		case p.out <- b:
		default:
		}
	}
	s.mu.Unlock()
}

// bellHost sends a BEL to the host's terminal — an unobtrusive nudge for events
// the host shouldn't miss (e.g. a guest requesting control).
func (s *Session) bellHost() {
	s.mu.Lock()
	for _, p := range s.parts {
		if p.IsHost {
			select {
			case p.out <- []byte("\a"):
			default:
			}
		}
	}
	s.mu.Unlock()
}

// updateTitles sets EVERY participant's window title via OSC — an ambient
// "who's typing" indicator visible to the whole line. It rides each participant's
// output channel (serialized with program output, no torn writes), costs no screen
// rows, and — unlike an on-screen notice — never corrupts full-screen apps
// (vim/claude), since the title bar is separate from the grid. The inner program
// may set its own title too, so we refresh on driver change, idle, and join/leave.
func (s *Session) updateTitles() {
	type target struct {
		out  chan []byte
		name string
		host bool
	}
	s.mu.Lock()
	driver := sanitizeNotice(s.driver)
	driving := s.driver != "" && time.Since(s.driverAt) < 3*time.Second
	watchers := 0
	for _, p := range s.parts {
		if !p.IsHost {
			watchers++
		}
	}
	targets := make([]target, 0, len(s.parts))
	for _, p := range s.parts {
		targets = append(targets, target{p.out, p.Name, p.IsHost})
	}
	s.mu.Unlock()

	for _, t := range targets {
		title := "☎ partyline"
		switch {
		case driving && driver == sanitizeNotice(t.name):
			title += " · ✎ you're typing"
		case driving:
			title += " · ✎ " + driver + " typing"
		case t.host && watchers == 1:
			title += " · 1 watching"
		case t.host && watchers > 1:
			title += fmt.Sprintf(" · %d watching", watchers)
		}
		select {
		case t.out <- []byte("\x1b]0;" + title + "\x07"):
		default:
		}
	}
}

// markDriver records who just typed. On a *change* of driver it refreshes the
// ambient title and — when a GUEST takes the keyboard — posts a one-line
// "✎ <name> is driving" notice (debounced to control handovers, not keystrokes).
func (s *Session) markDriver(p *Participant) {
	s.mu.Lock()
	changed := s.driver != p.Name
	s.driver = p.Name
	s.driverAt = time.Now()
	s.seenDrivers[p.Name] = struct{}{}
	s.mu.Unlock()
	if changed {
		// Everyone's title bar now shows "✎ <name> typing" — a safe, always-visible
		// who's-driving signal that (unlike an inline notice) can't corrupt a
		// full-screen app's screen.
		s.updateTitles()
		s.refreshHUDs()
	}
}

// DriverCount is the number of DISTINCT people who took the keyboard this session
// (→ session_ended.driver_count, #21). Safe to call from another goroutine.
func (s *Session) DriverCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.seenDrivers)
}

// driverLoop clears the driver after a short idle and refreshes the title.
func (s *Session) driverLoop() {
	defer obs.Guard("ptysess.driverLoop")
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.Done:
			return
		case <-t.C:
			s.mu.Lock()
			expired := s.driver != "" && time.Since(s.driverAt) > 3*time.Second
			if expired {
				s.driver = ""
			}
			s.mu.Unlock()
			if expired {
				s.updateTitles()
			}
		}
	}
}

func (s *Session) Attach(name string, w io.Writer, cols, rows int, isHost, fullAccess bool) *Participant {
	s.mu.Lock()
	s.nextID++
	name = s.uniqueNameLocked(name)
	// The host's terminal is where the grant pop-up renders; wrap its writer in a
	// gate so we can buffer normal output while the modal is up (then flush).
	if isHost {
		gw := &gateWriter{dst: w}
		s.hostGate = gw
		w = gw
	}
	p := &Participant{
		ID: s.nextID, Name: name, IsHost: isHost, FullAccess: isHost || fullAccess,
		// Only the host or a full-access user may ever type. Viewers are watch-only
		// — even an "open" session never makes a viewer typeable.
		CanType: isHost || (s.open && fullAccess),
		Cols:    cols, Rows: rows,
		out: make(chan []byte, outBufSize), w: w,
	}
	// Queue the clean screen snapshot BEFORE registering, so it's first in the
	// joiner's channel and every subsequent broadcast (which can only fire once we
	// release s.mu) lands strictly after it. The host owns the real terminal — no
	// snapshot needed. The buffered channel makes this send non-blocking.
	if !isHost {
		p.out <- s.snapshotLocked()
	}
	s.parts[p.ID] = p
	s.mu.Unlock()

	go func() { // per-participant writer
		defer obs.Guard("ptysess.writer")
		for b := range p.out {
			if _, err := p.w.Write(b); err != nil {
				return
			}
		}
	}()
	if !isHost {
		role := "view-only — ctrl-\\ r to request control"
		if p.CanType {
			role = "can type"
		}
		// tell everyone else who joined…
		s.noticeOthers(p, fmt.Sprintf("%s joined the session (%s)", name, role))
		// …and give the joiner a self-addressed line — who they are, their rights,
		// and the keys that matter — so they don't drop into raw output blind.
		you := "watch-only"
		if p.CanType {
			you = "you can type"
		}
		s.Notice(p, fmt.Sprintf("connected as %s · %s · /phelp for commands · ctrl-\\ d to leave", name, you))
	}
	s.recalcSize()
	s.firePresence()
	s.refreshHUDs()
	return p
}

func (s *Session) Detach(p *Participant) {
	s.mu.Lock()
	if _, ok := s.parts[p.ID]; ok {
		delete(s.parts, p.ID)
		close(p.out)
	}
	s.mu.Unlock()
	if !p.IsHost {
		s.Notice(nil, p.Name+" left the session")
		s.recalcSize()
	}
	s.firePresence()
	s.refreshHUDs()
}

func (s *Session) firePresence() {
	s.mu.Lock()
	f := s.OnPresence
	s.mu.Unlock()
	if f != nil {
		go f()
	}
	s.updateTitles()
}

// Names returns current participant names (host first when present).
func (s *Session) Names() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var host, rest []string
	for _, p := range s.parts {
		if p.IsHost {
			host = append(host, p.Name)
		} else {
			rest = append(rest, p.Name)
		}
	}
	return append(host, rest...)
}

// WriteInput writes raw bytes straight to the program's PTY, bypassing the sharing /
// grant / prefix logic in HandleInput. For local single-owner use (the multiplexer),
// where the caller IS the driver and owns its own prefix key — never for shared sessions.
func (s *Session) WriteInput(b []byte) {
	s.mu.Lock()
	ptmx := s.ptmx
	s.mu.Unlock()
	if ptmx != nil {
		_, _ = ptmx.Write(b)
	}
}

func (s *Session) Resize(p *Participant, cols, rows int) {
	s.mu.Lock()
	p.Cols, p.Rows = cols, rows
	s.mu.Unlock()
	s.recalcSize()
}

// recalcSize clamps the pty to the smallest attached terminal (tmux rule).
func (s *Session) recalcSize() {
	s.mu.Lock()
	cols, rows := 0, 0
	for _, p := range s.parts {
		if p.Cols <= 0 || p.Rows <= 0 {
			continue
		}
		if cols == 0 || p.Cols < cols {
			cols = p.Cols
		}
		if rows == 0 || p.Rows < rows {
			rows = p.Rows
		}
	}
	s.mu.Unlock()
	if cols > 0 && rows > 0 {
		_ = pty.Setsize(s.ptmx, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
		s.vt.Resize(cols, rows) // keep the screen model in lockstep with the pty
	}
}

// ToggleGuests flips typing rights for every guest on the session.
func (s *Session) ToggleGuests() bool {
	s.mu.Lock()
	s.open = !s.open
	now := s.open
	for _, p := range s.parts {
		if !p.IsHost {
			p.CanType = now && p.FullAccess // viewers stay watch-only even when "open"
		}
	}
	s.mu.Unlock()
	s.refreshHUDs()
	return now
}

// uniqueNameLocked returns name, or name-2/name-3/… if already taken. Caller holds mu.
func (s *Session) uniqueNameLocked(name string) string {
	if name == "" {
		name = "guest"
	}
	taken := func(n string) bool {
		for _, p := range s.parts {
			if p.Name == n {
				return true
			}
		}
		return false
	}
	if !taken(name) {
		return name
	}
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s-%d", name, i)
		if !taken(cand) {
			return cand
		}
	}
}

// GrantToggle flips typing rights for a single named guest. Returns the
// resolved name, the new state, and whether a matching guest was found.
// ToggleLock flips whether new joiners are accepted; returns the new locked state.
func (s *Session) ToggleLock() bool {
	s.mu.Lock()
	s.locked = !s.locked
	v := s.locked
	s.mu.Unlock()
	s.refreshHUDs()
	return v
}

// Locked reports whether new joiners should be rejected.
func (s *Session) Locked() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.locked
}

// GrantToggle flips a named guest's typing. result: "ok" | "notfound" | "viewer"
// — viewers can never be granted (watch is free, driving is paid).
func (s *Session) GrantToggle(name string) (matched string, nowCanType bool, result string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.parts {
		if !p.IsHost && strings.EqualFold(p.Name, name) {
			if !p.FullAccess {
				return p.Name, false, "viewer"
			}
			p.CanType = !p.CanType
			return p.Name, p.CanType, "ok"
		}
	}
	return "", false, "notfound"
}

func (s *Session) Who() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := ""
	for _, p := range s.parts {
		role := "view"
		if p.IsHost {
			role = "host"
		} else if p.CanType {
			role = "type"
		}
		if out != "" {
			out += ", "
		}
		out += fmt.Sprintf("%s(%s)", p.Name, role)
	}
	return out
}

// hudLinesLocked renders the session HUD (who's on the line + their rights + who's
// driving + line/lock state) as a dim, CRLF-joined block a raw terminal can print
// inline. Caller holds s.mu. Participant names are sanitized (no escape injection).
func (s *Session) hudLinesLocked() string {
	guests := "view-only"
	if s.open {
		guests = "open"
	}
	lock := "no"
	if s.locked {
		lock = "yes"
	}
	driving := s.driver != "" && time.Since(s.driverAt) < 3*time.Second
	var b strings.Builder
	fmt.Fprintf(&b, "\x1b[2m[partyline] ☎ %d on the line · guests: %s · locked: %s\x1b[0m\r\n",
		len(s.parts), guests, lock)
	row := func(p *Participant) {
		mark, role := " ", "viewer · watching"
		switch {
		case p.IsHost:
			mark, role = "★", "host"
		case p.FullAccess && p.CanType:
			role = "full · can type"
		case p.FullAccess:
			role = "full · watching"
		}
		drv := ""
		if driving && p.Name == s.driver {
			drv = "  ✎ driving"
		}
		fmt.Fprintf(&b, "\x1b[2m  %s %-16s %s%s\x1b[0m\r\n", mark, sanitizeNotice(p.Name), role, drv)
	}
	for _, p := range s.parts { // host first
		if p.IsHost {
			row(p)
		}
	}
	for _, p := range s.parts {
		if !p.IsHost {
			row(p)
		}
	}
	return b.String()
}

// showHUD sends the HUD once to a single participant (Tier 1: on-demand /phud).
func (s *Session) showHUD(to *Participant) {
	s.mu.Lock()
	card := s.hudLinesLocked()
	s.mu.Unlock()
	b := []byte("\r\n" + card)
	select {
	case to.out <- b:
	default:
	}
}

// refreshHUDs re-sends the HUD to everyone who turned it on (Tier 2: auto-surface
// on join/leave/grant/lock/driver changes). Builds the card under the lock, sends
// outside it.
func (s *Session) refreshHUDs() {
	s.mu.Lock()
	var targets []*Participant
	for _, p := range s.parts {
		if p.hud {
			targets = append(targets, p)
		}
	}
	if len(targets) == 0 {
		s.mu.Unlock()
		return
	}
	card := []byte("\r\n" + s.hudLinesLocked())
	s.mu.Unlock()
	for _, p := range targets {
		select {
		case p.out <- card:
		default:
		}
	}
}

func (s *Session) End() {
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
}

// HandleInput processes participant keystrokes: prefix commands are consumed,
// everything else forwards to the program (if allowed). Returns false when the
// participant asked to leave.
func (s *Session) HandleInput(p *Participant, b []byte) bool {
	dbg("HandleInput host=%v sawPfx=%v bytes=%v", p.IsHost, p.sawPfx, b)
	// A terminal in CSI-u / modifyOtherKeys mode (vim on iTerm2, etc.) reports
	// ctrl-\ as an escape sequence rather than 0x1c — map it back so the prefix
	// works the same everywhere.
	b = NormalizeCtrlBackslash(b)
	// While the grant pop-up is up, the host's keystrokes drive the modal (not the
	// program). Handled first so nothing leaks to the PTY underneath.
	if p.IsHost {
		s.mu.Lock()
		granting, menuing := s.granting, s.hostMenu
		s.mu.Unlock()
		if granting {
			s.handleGrantKey(b)
			return true
		}
		if menuing {
			s.handleHostMenuKey(b)
			return true
		}
	}
	fwd := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		c := b[i]
		if p.sawPfx {
			p.sawPfx = false
			switch c {
			case PrefixKey: // ctrl-\ ctrl-\ -> literal
				fwd = append(fwd, c)
			case 'w': // who's on the session
				s.Notice(p, "on the session: "+s.Who())
			case 'g': // host: grant/revoke guest typing
				if p.IsHost {
					if s.ToggleGuests() {
						s.Notice(nil, "host opened the session — guests can type")
					} else {
						s.Notice(nil, "host locked the session — guests are view-only")
					}
				}
			case 'r': // guest: request control → sticky grant pop-up on the host
				if !p.IsHost {
					s.openGrantModal(p)
				}
			case 'd': // disconnect (guest) / detach notice (host ends with q)
				if !p.IsHost {
					return false
				}
			case 'q': // host: end the session
				if p.IsHost {
					s.Notice(nil, "host ended the session")
					s.End()
				}
			case 'h': // toggle the session HUD for me (works inside full-screen apps)
				s.mu.Lock()
				p.hud = !p.hud
				on := p.hud
				s.mu.Unlock()
				if on {
					s.showHUD(p)
					s.Notice(p, "session HUD on (ctrl-\\ h to stop)")
				} else {
					s.Notice(p, "session HUD off")
				}
			case 'l': // host: lock/unlock new joiners
				if p.IsHost {
					if s.ToggleLock() {
						s.Notice(nil, "host locked the session — no new joiners")
					} else {
						s.Notice(nil, "host unlocked the session — joiners welcome")
					}
				}
			case '?': // command menu (host) / cheat-sheet (guest)
				if p.IsHost {
					s.openHostMenu()
				} else {
					s.Notice(p, "ctrl-\\ then: w who · r request control · h HUD · d leave · ctrl-\\ again sends a literal ctrl-\\ to the app")
				}
			default: // unknown command: swallow
			}
			continue
		}
		if c == PrefixKey {
			// Host: bare ctrl-\ opens the command menu, and the rest of this chunk
			// drives it (so a quick ctrl-\ g still selects "grant"). Only when
			// nothing's been typed before it this chunk — otherwise fall back to the
			// chord path. Guests never open the host menu; their join-side palette
			// sends {ctrl-\, <letter>}, handled by the sawPfx switch above.
			if p.IsHost && len(fwd) == 0 {
				s.openHostMenu()
				if rest := b[i+1:]; len(rest) > 0 {
					s.handleHostMenuKey(rest)
				}
				return true
			}
			p.sawPfx = true
			continue
		}

		// /p command detection: track the current input line; on Enter, if the
		// line is a partyline command, clear the shell's pending input (ctrl-u),
		// swallow the Enter, and run the command instead.
		switch {
		case c == '\r' || c == '\n':
			cmd := string(p.lineBuf)
			p.lineBuf = p.lineBuf[:0]
			if isPCommand(cmd) {
				if p.CanType {
					_, _ = s.ptmx.Write([]byte{0x15}) // ctrl-u: erase the typed command
				}
				if !s.runPCommand(p, cmd) {
					return false
				}
				continue // swallow the Enter
			}
		case c == 0x7F || c == 0x08: // backspace
			if len(p.lineBuf) > 0 {
				p.lineBuf = p.lineBuf[:len(p.lineBuf)-1]
			}
		case c == 0x15 || c == 0x03: // ctrl-u / ctrl-c reset the session
			p.lineBuf = p.lineBuf[:0]
		case c >= 0x20 && c < 0x7F:
			if len(p.lineBuf) < 64 {
				p.lineBuf = append(p.lineBuf, c)
			}
		default: // other control chars: not a plain command line anymore
			p.lineBuf = p.lineBuf[:0]
		}

		fwd = append(fwd, c)
	}
	if len(fwd) > 0 {
		// Hard gate: only the host or a verified full-access user can ever drive,
		// regardless of CanType state. Defense-in-depth for the paid/security wall.
		if p.CanType && (p.IsHost || p.FullAccess) {
			_, _ = s.ptmx.Write(fwd)
			s.markDriver(p) // ambient "who's driving" HUD
		} else {
			// view-only. Bell ONLY for a genuine typing attempt (a printable
			// character key), and at most once a second. Mouse reports, arrows,
			// and other escape sequences start with ESC and never bell — so
			// scrolling the mouse no longer produces an endless beep storm.
			if isTypingAttempt(fwd) && time.Since(p.lastBell) > time.Second {
				select {
				case p.out <- []byte("\a"):
					p.lastBell = time.Now()
				default:
				}
			}
		}
	}
	return true
}

// isTypingAttempt reports whether blocked input looks like a real attempt to type
// text (a printable character key) — as opposed to a mouse report, arrow/function
// key, or other escape sequence (all of which start with ESC). Only a real typing
// attempt earns the view-only bell; mouse scrolling stays silent.
func isTypingAttempt(b []byte) bool {
	if len(b) == 0 || b[0] == 0x1b {
		return false
	}
	for _, c := range b {
		if c >= 0x20 && c < 0x7f {
			return true
		}
	}
	return false
}

func isPCommand(line string) bool {
	f := strings.Fields(line)
	if len(f) == 0 {
		return false
	}
	switch f[0] {
	case "/pexit", "/pwho", "/pgrant", "/phelp", "/pinvite", "/plock", "/phud":
		return true
	}
	return false
}

// runPCommand executes a typed partyline command. Returns false if the
// participant should be disconnected.
func (s *Session) runPCommand(p *Participant, cmd string) bool {
	f := strings.Fields(cmd)
	switch f[0] {
	case "/pinvite":
		if len(f) < 2 {
			s.Notice(p, "usage: /pinvite someone@example.com")
			break
		}
		if s.OnInvite == nil {
			s.Notice(p, "session isn't registered with the control plane — run `ptln login` first")
			break
		}
		target := f[1]
		if len(target) > 320 || !emailRE.MatchString(target) {
			s.Notice(p, "that doesn't look like an email — usage: /pinvite someone@example.com")
			break
		}
		go func() { defer obs.Guard("ptysess.onInvite"); s.Notice(nil, s.OnInvite(target)) }() // HTTP — never block the input loop
	case "/pwho":
		s.Notice(p, "on the session: "+s.Who())
	case "/phud":
		// Tier 1: bare /phud prints the session card once. Tier 2: on/off toggles
		// auto-resending it to this participant on every change.
		switch {
		case len(f) >= 2 && f[1] == "on":
			s.mu.Lock()
			p.hud = true
			s.mu.Unlock()
			s.showHUD(p)
			s.Notice(p, "session HUD on — it'll refresh as people join, leave, or take the keyboard (/phud off to stop)")
		case len(f) >= 2 && f[1] == "off":
			s.mu.Lock()
			p.hud = false
			s.mu.Unlock()
			s.Notice(p, "session HUD off")
		default:
			s.showHUD(p)
		}
	case "/pgrant":
		if !p.IsHost {
			s.Notice(p, "only the host can /pgrant — use /pexit to leave, ctrl-\\ r to request control")
			break
		}
		if len(f) >= 2 { // per-guest: /pgrant <name> toggles that one guest
			name, now, res := s.GrantToggle(f[1])
			switch res {
			case "ok":
				if now {
					s.Notice(nil, "host granted "+name+" typing")
				} else {
					s.Notice(nil, "host revoked "+name+"'s typing")
				}
			case "viewer":
				s.Notice(p, name+" is view-only. Promote them to Full access: `ptln team access <handle> full` (or the team page), have them rejoin, then /pgrant them.")
			default:
				s.Notice(p, "no guest named "+f[1]+" on the session (try /pwho)")
			}
			s.refreshHUDs()
			break
		}
		if s.ToggleGuests() { // bare /pgrant: whole-line toggle
			s.Notice(nil, "host opened the session — all guests can type")
		} else {
			s.Notice(nil, "host locked the session — guests are view-only")
		}
	case "/plock":
		if !p.IsHost {
			s.Notice(p, "only the host can /plock")
			break
		}
		if s.ToggleLock() {
			s.Notice(nil, "host locked the session — no new joiners")
		} else {
			s.Notice(nil, "host unlocked the session — joiners welcome")
		}
	case "/pexit":
		if p.IsHost {
			s.Notice(nil, "host ended the session")
			s.End()
		} else {
			return false
		}
	case "/phelp":
		s.Notice(p, "/pwho · /phud [on|off] · /pinvite <email> · /pgrant [name] (host) · /plock (host) · /pexit · in full-screen apps (vim/claude) use the ctrl-\\ prefix: w g r h l d q ?")
	}
	return true
}
