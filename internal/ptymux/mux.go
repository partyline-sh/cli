// Package ptymux is a local, single-terminal multiplexer for LLM CLI sessions: it
// hosts N PTY-backed children (each a ptysess.Session) inside the CURRENT terminal and
// cycles between them — one full-screen at a time, switched with a ctrl-\ prefix. NOT a
// pane-splitting terminal multiplexer (that's tmux's job); this is "windows". It composes
// ptysess for the PTY + VT emulator + Snapshot() and owns its own raw-mode input loop +
// prefix, since a local mux is single-owner (no grant/who/lock sharing semantics).
//
// The mux also hosts a "home" view (the `ptln llms` launcher) as a first-class screen:
// home is where you pick/launch sessions; launching spawns an in-process child and
// switches to it; ctrl-o drops back to home with the session still running. When the
// last child exits, the mux returns to home rather than quitting — the launcher persists.
package ptymux

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/term"

	"partyline.sh/partyline/internal/ptysess"
)

// Home is the launcher screen the mux hosts (implemented by the llms browser in package
// main). The mux owns the terminal and drives the home view: it renders it and feeds it
// raw key chunks, acting on the HomeAction returned.
type Home interface {
	// Render paints the home screen into the full cols×rows terminal (the mux draws no
	// status bar over home — home owns the whole screen).
	Render(cols, rows int)
	// HandleKey processes one raw input chunk and returns what the mux should do next.
	HandleKey(b []byte) HomeAction
}

// HomeAction is the home view's response to a key. At most one of the verbs is set; an
// all-zero action means "stayed in home, re-render".
type HomeAction struct {
	Spawn     *Spec  // launch this session as a new in-process child, then switch to it
	SwitchKey string // jump to an already-live child by key (skip launching a duplicate)
	Suspend   func() // shell out (e.g. a pager): mux restores the terminal, runs this, re-enters
	Quit      bool   // quit the whole app
}

type uiMode int

const (
	modeHome uiMode = iota // the launcher is on screen; keys go to Home
	modeLive               // a child is full-screen; keys forward to it (ctrl-\ = prefix)
)

// Spec describes a child session to launch in the mux.
type Spec struct {
	Label string   // shown in the status bar (e.g. "claude · payments")
	Key   string   // stable id (the llms session id) — dedupes "open" vs "switch"
	Argv  []string // program + args (e.g. ["claude","--resume",id]); resumeArgv
	Dir   string   // working dir ("" inherits)
}

type child struct {
	label string
	key   string
	sess  *ptysess.Session
	part  *ptysess.Participant
	gate  *gate
}

// Mux owns the terminal and the set of children.
type Mux struct {
	mu         sync.Mutex
	home       Home
	mode       uiMode
	children   []*child
	active     int
	cols, rows int
	fd         int
	old        *term.State
	sawPfx     bool
	confirming bool // a quit-confirmation prompt is on screen

	// StatusFn resolves a live child's key to "waiting" (your move) / "active"
	// (still working) / "" (unknown), for the quit-confirmation counts. Optional.
	StatusFn func(key string) string
}

// New builds a mux. With a home and no specs it opens at the launcher; with specs it
// spawns them and opens live on the first. A mux with neither home nor children is an
// error (nothing to show).
func New(home Home, specs []Spec) (*Mux, error) {
	mx := &Mux{home: home}
	for _, sp := range specs {
		if err := mx.spawn(sp); err != nil {
			// best-effort: a bad child shouldn't sink the whole mux
			fmt.Fprintf(os.Stderr, "ptln mux: couldn't start %q: %v\n", sp.Label, err)
		}
	}
	if home == nil && len(mx.children) == 0 {
		return nil, fmt.Errorf("no sessions to open")
	}
	if len(mx.children) > 0 {
		mx.mode = modeLive
	} else {
		mx.mode = modeHome
	}
	return mx, nil
}

// spawn starts one child and attaches the mux as its (gated) host.
func (mx *Mux) spawn(sp Spec) error {
	sess, err := ptysess.NewIn(sp.Dir, sp.Argv, "you", false)
	if err != nil {
		return err
	}
	g := &gate{}
	cols, rows := mx.bodySize()
	part := sess.Attach(sp.Label, g, cols, rows, true, true)
	ch := &child{label: sp.Label, key: sp.Key, sess: sess, part: part, gate: g}
	mx.mu.Lock()
	mx.children = append(mx.children, ch)
	mx.mu.Unlock()
	go mx.watchExit(ch)
	return nil
}

// LiveKeys returns the set of currently-live child keys, for the home view to mark which
// sessions are already running in the mux.
func (mx *Mux) LiveKeys() map[string]bool {
	mx.mu.Lock()
	defer mx.mu.Unlock()
	s := make(map[string]bool, len(mx.children))
	for _, c := range mx.children {
		if c.key != "" {
			s[c.key] = true
		}
	}
	return s
}

// ---- gate: per-child output writer. Writes to stdout only when active; ALWAYS sniffs
// the stream for DEC private modes so a switch can restore them (no bleed between apps).
type gate struct {
	mu     sync.Mutex
	active bool
	modes  modeState
}

func (g *gate) Write(b []byte) (int, error) {
	g.mu.Lock()
	g.modes.observe(b)
	active := g.active
	g.mu.Unlock()
	if active {
		return os.Stdout.Write(b)
	}
	return len(b), nil
}

func (g *gate) setActive(a bool) {
	g.mu.Lock()
	g.active = a
	g.mu.Unlock()
}

func (g *gate) restoreModes() []byte {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.modes.restore()
}

// ---- modeState: tracks the DEC private modes a child set (cursor visibility, mouse,
// bracketed paste, app-cursor-keys), parsed from its output, so we re-assert them on
// switch. Snapshot() restores content+cursor+alt-screen but not these.
type modeState struct {
	cursorHidden bool         // ?25l set it hidden
	bracketPaste bool         // ?2004
	appCursor    bool         // ?1
	mouse        map[int]bool // 1000/1002/1003/1006…
}

// the private modes we care to restore (others are left at terminal default)
func trackedMode(n int) bool {
	switch n {
	case 25, 1, 2004, 1000, 1002, 1003, 1005, 1006, 1015:
		return true
	}
	return false
}

// observe scans b for `ESC [ ? <nums> (h|l)` and updates tracked modes.
func (m *modeState) observe(b []byte) {
	for i := 0; i+3 < len(b); i++ {
		if b[i] != 0x1b || b[i+1] != '[' || b[i+2] != '?' {
			continue
		}
		j := i + 3
		for j < len(b) && (b[j] == ';' || (b[j] >= '0' && b[j] <= '9')) {
			j++
		}
		if j >= len(b) || (b[j] != 'h' && b[j] != 'l') {
			continue
		}
		set := b[j] == 'h'
		for _, part := range strings.Split(string(b[i+3:j]), ";") {
			n, err := atoiSafe(part)
			if err != nil || !trackedMode(n) {
				continue
			}
			m.apply(n, set)
		}
		i = j
	}
}

func atoiSafe(s string) (int, error) {
	n := 0
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("nan")
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

func (m *modeState) apply(n int, set bool) {
	switch n {
	case 25:
		m.cursorHidden = !set // ?25h shows, ?25l hides
	case 2004:
		m.bracketPaste = set
	case 1:
		m.appCursor = set
	default: // mouse families
		if m.mouse == nil {
			m.mouse = map[int]bool{}
		}
		m.mouse[n] = set
	}
}

// restore returns the bytes to re-assert this child's modes after a switch (emitted
// AFTER the snapshot). Defaults are reset separately (see modeDefaults).
func (m *modeState) restore() []byte {
	var b strings.Builder
	if m.cursorHidden {
		b.WriteString("\x1b[?25l")
	}
	if m.appCursor {
		b.WriteString("\x1b[?1h")
	}
	if m.bracketPaste {
		b.WriteString("\x1b[?2004h")
	}
	for n, on := range m.mouse {
		if on {
			fmt.Fprintf(&b, "\x1b[?%dh", n)
		}
	}
	return []byte(b.String())
}

// modeDefaults resets the private modes to terminal default, clearing any bleed from the
// previously-focused child before we re-assert the new one's modes.
func modeDefaults() []byte {
	return []byte("\x1b[?25h\x1b[?1l\x1b[?2004l\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l\x1b[?1015l")
}

// ---- run loop ----

func (mx *Mux) bodySize() (cols, rows int) {
	if mx.cols == 0 || mx.rows == 0 {
		return 80, 23
	}
	r := mx.rows - 1 // reserve the bottom row for the status bar
	if r < 1 {
		r = 1
	}
	return mx.cols, r
}

// Run takes over the terminal (raw + alt-screen), paints the initial screen (home or the
// first child), and drives the input loop until the user quits or — if there's no home —
// the last child exits. Restores on return.
func (mx *Mux) Run() error {
	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	mx.fd, mx.old = fd, old
	defer mx.restore()

	os.Stdout.WriteString("\x1b[?1049h\x1b[?25l") // alt-screen, hide cursor
	if c, r, e := term.GetSize(int(os.Stdout.Fd())); e == nil && c > 0 && r > 0 {
		mx.cols, mx.rows = c, r
	} else {
		mx.cols, mx.rows = 80, 24
	}
	mx.resizeAll()

	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	go func() {
		for range winch {
			mx.onResize()
		}
	}()

	mx.mu.Lock()
	md := mx.mode
	active := mx.active
	mx.mu.Unlock()
	if md == modeHome {
		mx.gotoHome()
	} else {
		mx.gotoLive(active)
	}

	dbg := newInputDebug() // PTLN_MUX_DEBUG=<file> logs raw + normalized input

	buf := make([]byte, 4096)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil || n == 0 {
			return nil
		}
		mx.mu.Lock()
		md := mx.mode
		confirming := mx.confirming
		mx.mu.Unlock()
		if confirming {
			if mx.handleConfirm(buf[:n]) {
				return nil // confirmed quit
			}
			continue
		}
		if md == modeHome {
			norm := legacyKeys(buf[:n]) // menu wants legacy keys even if a child left kitty mode on
			dbg.log("home", buf[:n], norm)
			if len(norm) == 0 {
				continue // chunk was entirely dropped (e.g. a key-release event) — nothing for the menu
			}
			if mx.handleHome(norm) {
				return nil
			}
		} else {
			norm := ptysess.NormalizeCtrlBackslash(buf[:n])
			dbg.log("live", buf[:n], norm)
			if mx.handleInput(norm) {
				return nil
			}
		}
	}
}

// handleHome feeds a key chunk to the home view and acts on the result. Returns true to
// quit the app.
func (mx *Mux) handleHome(b []byte) bool {
	if mx.home == nil {
		return false
	}
	act := mx.home.HandleKey(b)
	switch {
	case act.Quit:
		return mx.requestQuit() // confirm if sessions are running
	case act.SwitchKey != "":
		mx.switchToKey(act.SwitchKey)
	case act.Spawn != nil:
		mx.spawnOrSwitch(*act.Spawn)
	case act.Suspend != nil:
		mx.suspend(act.Suspend)
	default:
		mx.renderHome() // stayed in home; repaint for the state change
	}
	return false
}

// handleInput processes a chunk of (normalized) live-mode input; returns true to quit.
func (mx *Mux) handleInput(data []byte) bool {
	var fwd []byte
	flush := func() {
		if len(fwd) > 0 {
			mx.writeActive(fwd)
			fwd = nil
		}
	}
	i := 0
	for i < len(data) {
		if mx.sawPfx {
			mx.sawPfx = false
			// The command key may arrive as a plain byte, a ctrl-modified byte
			// (ctrl held through the chord), or a CSI-u / modifyOtherKeys escape
			// sequence (apps that report all keys as escape codes). decodeCmdKey
			// resolves all three to the base letter/digit so the chord works no
			// matter how the terminal encodes it.
			k, n := decodeCmdKey(data[i:])
			switch {
			case k == 0x1c: // literal ctrl-\ → child
				fwd = append(fwd, 0x1c)
			case k == 'n':
				flush()
				mx.cycle(1)
			case k == 'p':
				flush()
				mx.cycle(-1)
			case k == 'x':
				flush()
				mx.closeActive()
			case k == 'q':
				flush()
				if mx.requestQuit() { // confirm if sessions are running
					return true
				}
				return false
			case k >= '1' && k <= '9':
				flush()
				mx.jump(int(k - '1'))
			default: // unknown prefix key — swallow the whole sequence
			}
			i += n
			continue
		}
		// Ctrl-O — the direct "back to launcher" hotkey (single chord, no prefix).
		if kind, n := ctrlOAt(data[i:]); kind != 0 {
			if kind == 1 { // press → home (release/repeat are just suppressed)
				flush()
				mx.gotoHome()
			}
			i += n
			continue
		}
		if data[i] == 0x1c {
			flush()
			mx.sawPfx = true
			i++
			continue
		}
		fwd = append(fwd, data[i])
		i++
	}
	flush()
	return false
}

// decodeCmdKey resolves the command key that follows the ctrl-\ prefix to a single
// base byte (a lowercase letter or digit), consuming however many input bytes that
// key occupies. It handles three encodings, modifier-agnostic so holding ctrl through
// the chord still works:
//   - a plain byte ('l'), or a ctrl-letter control byte (0x0c = ctrl-l) → the letter
//   - CSI-u:           ESC [ <code> ; <mods>[:evt] u
//   - modifyOtherKeys: ESC [ 27 ; <mods> ; <code> ~
//
// Returns (0x1c, n) for a ctrl-\ (so ctrl-\ ctrl-\ still sends a literal), and
// (0x1b, n) for an unrecognized escape sequence (so it's swallowed, not leaked).
func decodeCmdKey(b []byte) (key byte, n int) {
	if len(b) == 0 {
		return 0, 0
	}
	if b[0] != 0x1b {
		c := b[0]
		if c >= 0x01 && c <= 0x1a { // ctrl-<letter> control byte → the letter
			return c | 0x60, 1
		}
		return c, 1
	}
	// ESC-led: only CSI ( ESC [ ... ) carries our encoded keys.
	if len(b) < 2 || b[1] != '[' {
		return 0x1b, 1
	}
	j := 2
	for j < len(b) && (b[j] == ';' || b[j] == ':' || (b[j] >= '0' && b[j] <= '9')) {
		j++
	}
	if j >= len(b) || (b[j] != 'u' && b[j] != '~') {
		return 0x1b, 1 // not a key sequence we decode; drop the ESC, keep scanning
	}
	final := b[j]
	parts := splitParams(b[2:j])
	code := -1
	switch final {
	case 'u': // ESC [ <code> ; <mods> u  → code is the first param
		if len(parts) >= 1 {
			code = parts[0]
		}
	case '~': // ESC [ 27 ; <mods> ; <code> ~  (modifyOtherKeys)
		if len(parts) >= 3 && parts[0] == 27 {
			code = parts[2]
		}
	}
	consumed := j + 1
	if code <= 0 {
		return 0x1b, consumed
	}
	k := byte(code)
	if k >= 'A' && k <= 'Z' {
		k += 32 // normalize to lowercase
	}
	return k, consumed
}

// splitParams parses a CSI parameter string ("92;5" or "27;5;92" or "92;5:3") into the
// leading integer of each ';'-separated field (sub-params after ':' are ignored).
func splitParams(p []byte) []int {
	var out []int
	n, has := 0, false
	for i := 0; i <= len(p); i++ {
		if i < len(p) && p[i] >= '0' && p[i] <= '9' {
			n, has = n*10+int(p[i]-'0'), true
		} else if i == len(p) || p[i] == ';' {
			if has {
				out = append(out, n)
			} else {
				out = append(out, -1)
			}
			n, has = 0, false
		} else if p[i] == ':' {
			// skip the rest of this field's sub-params
			for i+1 < len(p) && p[i+1] != ';' {
				i++
			}
		}
	}
	return out
}

func (mx *Mux) writeActive(b []byte) {
	mx.mu.Lock()
	var ch *child
	if mx.active >= 0 && mx.active < len(mx.children) {
		ch = mx.children[mx.active]
	}
	mx.mu.Unlock()
	if ch != nil {
		ch.sess.WriteInput(b)
	}
}

func (mx *Mux) cycle(d int) {
	mx.mu.Lock()
	n := len(mx.children)
	if n == 0 {
		mx.mu.Unlock()
		return
	}
	next := (mx.active + d + n) % n
	mx.mu.Unlock()
	mx.switchTo(next)
}

func (mx *Mux) jump(i int) {
	mx.mu.Lock()
	ok := i >= 0 && i < len(mx.children)
	mx.mu.Unlock()
	if ok {
		mx.switchTo(i)
	}
}

// ---- mode transitions ----

// gotoHome deactivates the focused child (it keeps running in the background) and shows
// the launcher.
func (mx *Mux) gotoHome() {
	if mx.home == nil {
		return
	}
	mx.mu.Lock()
	if mx.active >= 0 && mx.active < len(mx.children) {
		mx.children[mx.active].gate.setActive(false)
	}
	mx.mode = modeHome
	mx.mu.Unlock()
	// Home owns the whole screen. The child may have left the terminal in its own state
	// (mouse/paste modes, a scroll region, origin mode) that corrupts the launcher menu's
	// absolute-positioned rendering. Reset to a clean slate before painting. We deliberately
	// do NOT touch the child's enhanced keyboard protocol (kitty/modifyOtherKeys) — popping
	// it can't be cleanly restored on return; instead legacyKeys() normalizes home input.
	//   modeDefaults()  clear mouse/paste/app-cursor bleed
	//   ESC[r           reset the scroll region to full screen
	//   ESC[?6l         origin mode off (menu uses absolute positioning)
	//   ESC[2J          clear; then hide cursor + autowrap off (the menu's contract)
	os.Stdout.Write(modeDefaults())
	os.Stdout.WriteString("\x1b[r\x1b[?6l\x1b[2J\x1b[?25l\x1b[?7l")
	mx.renderHome()
}

func (mx *Mux) renderHome() {
	mx.mu.Lock()
	cols, rows, home := mx.cols, mx.rows, mx.home
	mx.mu.Unlock()
	if home != nil {
		home.Render(cols, rows)
	}
}

// gotoLive switches to child i full-screen. Falls back to home if i is out of range.
func (mx *Mux) gotoLive(i int) {
	mx.mu.Lock()
	ok := i >= 0 && i < len(mx.children)
	if ok {
		mx.mode = modeLive
	}
	mx.mu.Unlock()
	if !ok {
		mx.gotoHome()
		return
	}
	os.Stdout.WriteString("\x1b[?7h") // restore autowrap for the child
	mx.switchTo(i)
}

// switchTo repaints child i: deactivate old gate, reset mode bleed, write the child's
// snapshot, re-assert its modes, draw the bar, then activate its gate (live output flows).
func (mx *Mux) switchTo(i int) {
	mx.mu.Lock()
	if i < 0 || i >= len(mx.children) {
		mx.mu.Unlock()
		return
	}
	old := mx.active
	if old >= 0 && old < len(mx.children) {
		mx.children[old].gate.setActive(false)
	}
	mx.active = i
	ch := mx.children[i]
	mx.mu.Unlock()

	var out []byte
	out = append(out, modeDefaults()...)
	out = append(out, ch.sess.Snapshot()...)
	out = append(out, ch.gate.restoreModes()...)
	os.Stdout.Write(out)
	mx.drawBar()
	ch.gate.setActive(true)
}

// spawnOrSwitch launches sp as a new child and switches to it — unless a child with the
// same key is already live, in which case it just switches there (no duplicate).
func (mx *Mux) spawnOrSwitch(sp Spec) {
	if sp.Key != "" {
		mx.mu.Lock()
		for i, c := range mx.children {
			if c.key == sp.Key {
				mx.mu.Unlock()
				mx.gotoLive(i)
				return
			}
		}
		mx.mu.Unlock()
	}
	if err := mx.spawn(sp); err != nil {
		mx.renderHome()
		return
	}
	mx.mu.Lock()
	idx := len(mx.children) - 1
	mx.mu.Unlock()
	mx.gotoLive(idx)
}

func (mx *Mux) switchToKey(key string) {
	mx.mu.Lock()
	idx := -1
	for i, c := range mx.children {
		if c.key == key {
			idx = i
			break
		}
	}
	mx.mu.Unlock()
	if idx < 0 {
		mx.renderHome()
		return
	}
	mx.gotoLive(idx)
}

// suspend hands the real terminal to fn (e.g. a pager): leave alt-screen + cooked mode,
// run fn, then re-enter alt-screen + raw and repaint home.
func (mx *Mux) suspend(fn func()) {
	os.Stdout.WriteString("\x1b[?7h\x1b[?25h\x1b[?1049l")
	if mx.old != nil {
		_ = term.Restore(mx.fd, mx.old)
	}
	fn()
	_, _ = term.MakeRaw(mx.fd) // mx.old already holds the original cooked state
	os.Stdout.WriteString("\x1b[?1049h\x1b[?25l\x1b[?7l")
	mx.renderHome()
}

func (mx *Mux) closeActive() {
	mx.mu.Lock()
	if mx.active < 0 || mx.active >= len(mx.children) {
		mx.mu.Unlock()
		return
	}
	ch := mx.children[mx.active]
	mx.mu.Unlock()
	ch.sess.End() // kill the child; the exit watcher removes it + refocuses
}

// requestQuit returns true (quit now) if no sessions are running; otherwise it puts up a
// confirmation prompt and returns false (the next key is handled by handleConfirm).
func (mx *Mux) requestQuit() bool {
	mx.mu.Lock()
	n := len(mx.children)
	mx.mu.Unlock()
	if n == 0 {
		return true
	}
	mx.mu.Lock()
	mx.confirming = true
	// Mute the focused child so its output doesn't paint over the prompt; switchTo
	// re-activates it if the user cancels.
	if mx.mode == modeLive && mx.active >= 0 && mx.active < len(mx.children) {
		mx.children[mx.active].gate.setActive(false)
	}
	mx.mu.Unlock()
	mx.renderConfirm()
	return false
}

// handleConfirm processes a keypress while the quit prompt is up: y/Enter confirms (quit);
// anything else cancels and restores the previous screen. Returns true to quit.
func (mx *Mux) handleConfirm(b []byte) bool {
	kb := legacyKeys(b)
	if len(kb) == 0 {
		return false // dropped (e.g. a key-release) — keep waiting
	}
	switch kb[0] {
	case 'y', 'Y', '\r', '\n':
		return true
	}
	mx.mu.Lock()
	mx.confirming = false
	md := mx.mode
	active := mx.active
	mx.mu.Unlock()
	if md == modeHome {
		mx.gotoHome()
	} else {
		mx.switchTo(active)
	}
	return false
}

// quitCounts tallies the live children by status for the confirmation prompt.
func (mx *Mux) quitCounts() (waiting, running, total int) {
	mx.mu.Lock()
	kids := append([]*child(nil), mx.children...)
	fn := mx.StatusFn
	mx.mu.Unlock()
	total = len(kids)
	for _, c := range kids {
		st := ""
		if fn != nil {
			st = fn(c.key)
		}
		if st == "waiting" {
			waiting++
		} else {
			running++ // "active" or unknown both count as running
		}
	}
	return
}

// renderConfirm draws the centered quit-confirmation box with live-session counts.
func (mx *Mux) renderConfirm() {
	waiting, running, total := mx.quitCounts()
	noun := "session"
	if total != 1 {
		noun = "sessions"
	}
	lines := []string{
		"\x1b[1mQuit partyline?\x1b[0m",
		"",
		fmt.Sprintf("%d %s open will be closed:", total, noun),
	}
	if waiting > 0 {
		lines = append(lines, fmt.Sprintf("\x1b[38;5;214m  %d waiting on you\x1b[0m", waiting))
	}
	if running > 0 {
		lines = append(lines, fmt.Sprintf("\x1b[38;5;46m  %d still running\x1b[0m", running))
	}
	lines = append(lines, "", "\x1b[38;5;240m[y] quit   ·   [n] keep working\x1b[0m")
	mx.drawCenteredBox(lines)
}

// drawCenteredBox clears the screen and draws a centered rounded box of pre-styled lines.
func (mx *Mux) drawCenteredBox(lines []string) {
	mx.mu.Lock()
	cols, rows := mx.cols, mx.rows
	mx.mu.Unlock()
	w := 0
	for _, l := range lines {
		if v := visLen(l); v > w {
			w = v
		}
	}
	if w > cols-4 {
		w = cols - 4
	}
	top := (rows - (len(lines) + 2)) / 2
	if top < 1 {
		top = 1
	}
	left := (cols - (w + 4)) / 2
	if left < 1 {
		left = 1
	}
	clr := "\x1b[38;5;215m"
	var b strings.Builder
	b.WriteString("\x1b[2J\x1b[?25l")
	fmt.Fprintf(&b, "\x1b[%d;%dH%s╭%s╮\x1b[0m", top, left, clr, strings.Repeat("─", w+2))
	for i, l := range lines {
		pad := w - visLen(l)
		if pad < 0 {
			pad = 0
		}
		fmt.Fprintf(&b, "\x1b[%d;%dH%s│\x1b[0m %s%s %s│\x1b[0m", top+1+i, left, clr, l, strings.Repeat(" ", pad), clr)
	}
	fmt.Fprintf(&b, "\x1b[%d;%dH%s╰%s╯\x1b[0m", top+1+len(lines), left, clr, strings.Repeat("─", w+2))
	os.Stdout.WriteString(b.String())
}

// watchExit removes a child when its program exits. If we're live on it, refocus a
// neighbor; when the last child is gone, return to home (the launcher persists) — or, if
// there's no home, close stdin so Run returns and the terminal is restored.
func (mx *Mux) watchExit(ch *child) {
	<-ch.sess.Done
	mx.mu.Lock()
	idx := -1
	for k, c := range mx.children {
		if c == ch {
			idx = k
			break
		}
	}
	if idx < 0 {
		mx.mu.Unlock()
		return
	}
	mx.children = append(mx.children[:idx], mx.children[idx+1:]...)
	n := len(mx.children)
	wasActive := idx == mx.active
	if mx.active >= n {
		mx.active = n - 1
	}
	next := mx.active
	md := mx.mode
	mx.mu.Unlock()

	if n == 0 {
		if mx.home != nil {
			mx.gotoHome()
		} else {
			_ = os.Stdin.Close() // no launcher → unblock Run's read for a clean exit
		}
		return
	}
	switch {
	case md == modeHome:
		mx.renderHome() // refresh the live markers
	case wasActive:
		mx.switchTo(next)
	default:
		mx.drawBar()
	}
}

// ---- resize + status bar ----

func (mx *Mux) onResize() {
	if c, r, e := term.GetSize(int(os.Stdout.Fd())); e == nil && c > 0 && r > 0 {
		mx.mu.Lock()
		mx.cols, mx.rows = c, r
		md := mx.mode
		active := mx.active
		mx.mu.Unlock()
		mx.resizeAll()
		if md == modeHome {
			mx.renderHome()
		} else {
			mx.switchTo(active)
		}
	}
}

func (mx *Mux) resizeAll() {
	cols, rows := mx.bodySize()
	mx.mu.Lock()
	kids := append([]*child(nil), mx.children...)
	mx.mu.Unlock()
	for _, ch := range kids {
		ch.sess.Resize(ch.part, cols, rows)
	}
}

// drawBar paints the reserved bottom row: session tabs with the active one highlighted.
// Saves/restores the cursor so the focused child's cursor position is untouched.
func (mx *Mux) drawBar() {
	mx.mu.Lock()
	rows, cols, active := mx.rows, mx.cols, mx.active
	var tabs []string
	for k, c := range mx.children {
		label := fmt.Sprintf(" %d %s ", k+1, c.label)
		if k == active {
			label = "\x1b[7m" + label + "\x1b[0m" // reverse video = active
		}
		tabs = append(tabs, label)
	}
	mx.mu.Unlock()
	bar := "\x1b[38;5;245m" + strings.Join(tabs, "\x1b[38;5;245m|") + "\x1b[0m"
	bar += "  \x1b[38;5;240mctrl-o launcher · ctrl-\\ n/p switch · q quit\x1b[0m"
	bar = clipANSI(bar, cols)
	// save cursor, go to bar row, clear + paint, restore cursor
	fmt.Fprintf(os.Stdout, "\x1b7\x1b[%d;1H\x1b[2K%s\x1b8", rows, bar)
}

func (mx *Mux) restore() {
	mx.mu.Lock()
	kids := append([]*child(nil), mx.children...)
	mx.mu.Unlock()
	for _, ch := range kids {
		ch.sess.End()
	}
	os.Stdout.WriteString("\x1b[?25h\x1b[?7h\x1b[?1049l") // show cursor, autowrap on, leave alt-screen
	if mx.old != nil {
		_ = term.Restore(mx.fd, mx.old)
	}
}

// clipANSI truncates s to at most w visible columns, ignoring ANSI escape sequences.
func clipANSI(s string, w int) string {
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
		if vis >= w {
			break
		}
		b.WriteByte(s[i])
		vis++
		i++
	}
	return b.String()
}
