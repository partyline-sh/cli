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
	// Enter is called each time the mux returns to the launcher (e.g. ctrl-o, or the last
	// session closing) — a transition, NOT a resize repaint. The launcher uses it to reset
	// transient view state (a search filter) so you come back to the full list.
	Enter()
}

// HomeAction is the home view's response to a key. At most one of the verbs is set; an
// all-zero action means "stayed in home, re-render".
type HomeAction struct {
	Spawn      *Spec  // launch this session as a new in-process child, then switch to it
	SpawnMany  []Spec // launch several at once (multi-select), then focus the first
	OpenPicker bool   // pop the live-session picker over the launcher (jump to an open one)
	SwitchKey  string // jump to an already-live child by key (skip launching a duplicate)
	Suspend    func() // shell out (e.g. a pager): mux restores the terminal, runs this, re-enters
	Quit       bool   // quit the whole app
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
	Model string   // model name, shown in the session picker ("" hides it)
	Argv  []string // program + args (e.g. ["claude","--resume",id]); resumeArgv
	Dir   string   // working dir ("" inherits)
}

type child struct {
	label string
	key   string
	model string
	argv  []string // resume command (incl. permission flags) — retained for workspace save
	dir   string
	sess  *ptysess.Session
	part  *ptysess.Participant
	gate  *gate
}

// Mux owns the terminal and the set of children.
type Mux struct {
	mu         sync.Mutex
	outMu      sync.Mutex // serializes EVERY write to os.Stdout (gate output, switch repaint, bar)
	home       Home
	mode       uiMode
	children   []*child
	active     int
	cols, rows int
	fd         int
	old        *term.State
	sawPfx     bool
	confirming bool // a quit-confirmation prompt is on screen
	picking    bool // the live-session picker modal is on screen
	pickerHome bool // the picker was opened from the launcher (cancel returns there)
	pick       int  // highlighted index in the picker

	// StatusFn resolves a live child's key to "waiting" (your move) / "active"
	// (still working) / "" (unknown), for the quit-confirmation counts. Optional.
	StatusFn func(key string) string

	// BeforeQuit, if set, is called once with the open children (as Specs) at the moment
	// quit is confirmed — BEFORE teardown — so the workspace can be saved for --resume.
	BeforeQuit func([]Spec)

	// Skin, if set, re-colours the mux's OWN chrome (picker, quit prompt, status bar) to
	// match the launcher theme. Applied only to our overlays — never to child output.
	Skin func(string) string
}

func (mx *Mux) skin(s string) string {
	if mx.Skin != nil {
		return mx.Skin(s)
	}
	return s
}

// liveSpecs snapshots the open children as Specs (for workspace save/restore).
func (mx *Mux) liveSpecs() []Spec {
	mx.mu.Lock()
	defer mx.mu.Unlock()
	out := make([]Spec, 0, len(mx.children))
	for _, c := range mx.children {
		out = append(out, Spec{Label: c.label, Key: c.key, Model: c.model, Argv: c.argv, Dir: c.dir})
	}
	return out
}

// fireBeforeQuit hands the current workspace to the BeforeQuit hook (if any). Call it only
// at a quit-confirmation point, while children still exist and no mutex is held.
func (mx *Mux) fireBeforeQuit() {
	if mx.BeforeQuit != nil {
		mx.BeforeQuit(mx.liveSpecs())
	}
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

// Relabel updates a live child's display label (used when a session is renamed in the
// launcher, so the picker + status bar show the new title). No-op if no live child has
// that key, or the label is unchanged.
func (mx *Mux) Relabel(key, label string) {
	mx.mu.Lock()
	for _, c := range mx.children {
		if c.key == key {
			c.label = label
			break
		}
	}
	mx.mu.Unlock()
}

// spawn starts one child and attaches the mux as its (gated) host.
func (mx *Mux) spawn(sp Spec) error {
	sess, err := ptysess.NewIn(sp.Dir, sp.Argv, "you", false)
	if err != nil {
		return err
	}
	g := &gate{out: &mx.outMu}
	cols, rows := mx.bodySize()
	part := sess.Attach(sp.Label, g, cols, rows, true, true)
	ch := &child{label: sp.Label, key: sp.Key, model: sp.Model, argv: sp.Argv, dir: sp.Dir, sess: sess, part: part, gate: g}
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
	out    *sync.Mutex // shared mux output lock — serializes the active-check WITH the write
	active bool
	modes  modeState
}

// Write holds the shared output lock across BOTH the active-check and the stdout write, so
// once setActive(false) returns, this child can never paint again — no in-flight write can
// land after a switch and leave stale text on the new screen.
func (g *gate) Write(b []byte) (int, error) {
	g.out.Lock()
	g.modes.observe(b) // always sniff modes (even inactive) so a switch can restore them
	if g.active {
		os.Stdout.Write(b)
	}
	g.out.Unlock()
	return len(b), nil
}

func (g *gate) setActive(a bool) {
	g.out.Lock()
	g.active = a
	g.out.Unlock()
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
		picking := mx.picking
		mx.mu.Unlock()
		if picking {
			mx.handlePick(buf[:n])
			continue
		}
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
	case len(act.SpawnMany) > 0:
		mx.spawnMany(act.SpawnMany)
	case act.OpenPicker:
		mx.openPicker(true) // pop the picker over the launcher; cancel returns here
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
			case k == 'w':
				flush()
				mx.openPicker(false)
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
	mx.outMu.Lock()
	os.Stdout.Write(modeDefaults())
	os.Stdout.WriteString("\x1b[r\x1b[?6l\x1b[2J\x1b[?25l\x1b[?7l")
	mx.outMu.Unlock()
	mx.home.Enter() // transition into the launcher — reset any active search filter
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
	var oldCh *child
	if old >= 0 && old < len(mx.children) {
		oldCh = mx.children[old]
	}
	mx.active = i
	ch := mx.children[i]
	mx.mu.Unlock()

	// The whole switch runs under the output lock so no child's in-flight write can paint
	// over the new screen: deactivate the old gate, repaint, activate the new — atomically.
	// (gate flags + modes are read/written here under the same lock the gates use.)
	mx.outMu.Lock()
	if oldCh != nil {
		oldCh.gate.active = false
	}
	var out []byte
	out = append(out, modeDefaults()...)
	// Reset the scroll region (DECSTBM) + origin mode before repainting: the previous
	// session may have pinned a bottom margin (LLM CLIs do this to fix their input box),
	// and a leftover margin makes the new session's snapshot paint into the wrong region —
	// leaving stale text in the input area. gotoHome does the same on the way to the menu.
	out = append(out, []byte("\x1b[r\x1b[?6l")...)
	out = append(out, ch.sess.Snapshot()...)
	out = append(out, ch.gate.modes.restore()...)
	os.Stdout.Write(out)
	ch.gate.active = true
	mx.outMu.Unlock()

	mx.drawBar()
}

// spawnOrSwitch launches sp as a new child and switches to it — unless a child with the
// same key is already live, in which case it just switches there (no duplicate).
// spawnMany opens a batch of sessions (multi-select from the launcher), skipping any
// already live (dedup by key), then focuses the first one of the batch.
func (mx *Mux) spawnMany(specs []Spec) {
	mx.mu.Lock()
	firstNew := len(mx.children)
	mx.mu.Unlock()
	for _, sp := range specs {
		mx.mu.Lock()
		live := false
		for _, c := range mx.children {
			if sp.Key != "" && c.key == sp.Key {
				live = true
				break
			}
		}
		mx.mu.Unlock()
		if !live {
			_ = mx.spawn(sp)
		}
	}
	mx.mu.Lock()
	n := len(mx.children)
	mx.mu.Unlock()
	if n == 0 {
		mx.renderHome()
		return
	}
	if firstNew >= n {
		firstNew = n - 1 // everything was already live → focus the last
	}
	mx.gotoLive(firstNew)
}

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
		mx.fireBeforeQuit() // save an empty workspace (nothing was open)
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
		mx.fireBeforeQuit() // snapshot the open sessions for --resume before teardown
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

// ---- live-session picker (ctrl-\ w) ----

// openPicker puts up the modal that lists the open sessions with their live status, so
// you can see what you're switching to instead of cycling blind. Muting the active child
// keeps its output from painting over the modal; closePicker re-activates via switchTo.
func (mx *Mux) openPicker(fromHome bool) {
	mx.mu.Lock()
	if len(mx.children) == 0 {
		mx.mu.Unlock()
		return
	}
	mx.picking = true
	mx.pickerHome = fromHome // cancel returns to the launcher (not a live session)
	mx.pick = mx.active
	if mx.pick < 0 || mx.pick >= len(mx.children) {
		mx.pick = 0
	}
	// From a live session, mute the active child so it doesn't paint over the modal. From
	// home there's no active child painting, so nothing to mute.
	if !fromHome && mx.active >= 0 && mx.active < len(mx.children) {
		mx.children[mx.active].gate.setActive(false)
	}
	mx.mu.Unlock()
	mx.renderPicker()
}

// handlePick processes a key while the picker is up: ↑↓/jk move, 1-9 jump straight to a
// session, Enter switches to the highlighted one, Esc/q cancel.
func (mx *Mux) handlePick(b []byte) {
	kb := legacyKeys(b)
	if len(kb) == 0 {
		return // dropped (e.g. a key-release) — keep the modal up
	}
	if len(kb) >= 3 && kb[0] == 0x1b && kb[1] == '[' { // arrow keys
		switch kb[2] {
		case 'A':
			mx.movePick(-1)
			return
		case 'B':
			mx.movePick(1)
			return
		}
	}
	switch c := kb[0]; {
	case c == '\r' || c == '\n':
		mx.closePicker(true)
	case c == 0x1b || c == 'q': // bare Esc or q → cancel
		mx.closePicker(false)
	case c == 'k':
		mx.movePick(-1)
	case c == 'j':
		mx.movePick(1)
	case c >= '1' && c <= '9':
		mx.mu.Lock()
		idx := int(c - '1')
		ok := idx < len(mx.children)
		if ok {
			mx.pick = idx
		}
		mx.mu.Unlock()
		if ok {
			mx.closePicker(true)
		}
	}
}

func (mx *Mux) movePick(d int) {
	mx.mu.Lock()
	if n := len(mx.children); n > 0 {
		mx.pick = (mx.pick + d + n) % n
	}
	mx.mu.Unlock()
	mx.renderPicker()
}

// closePicker tears down the modal: switch to the highlighted session (sw) or back to the
// one we came from (cancel). switchTo re-activates the target's gate and repaints it.
func (mx *Mux) closePicker(sw bool) {
	mx.mu.Lock()
	mx.picking = false
	fromHome := mx.pickerHome
	mx.pickerHome = false
	target, pick := mx.active, mx.pick
	mx.mu.Unlock()
	switch {
	case sw:
		mx.switchTo(pick) // picked a session → switch to it (live)
	case fromHome:
		mx.gotoHome() // cancelled a picker opened from the launcher → back to the launcher
	default:
		mx.switchTo(target) // cancelled in a live session → back to it
	}
}

// renderPicker draws the centered session list, each row: number, live status, label, model.
func (mx *Mux) renderPicker() {
	mx.mu.Lock()
	kids := append([]*child(nil), mx.children...)
	pick := mx.pick
	fn := mx.StatusFn
	mx.mu.Unlock()

	lines := []string{"\x1b[1mSwitch session\x1b[0m", ""}
	for i, c := range kids {
		icon, st := "\x1b[38;5;46m●\x1b[0m", "running" // green dot = still working
		if fn != nil && fn(c.key) == "waiting" {
			icon, st = "\x1b[38;5;214m⏳\x1b[0m", "waiting" // amber = your move
		}
		marker := "  "
		if i == pick {
			marker = "\x1b[1m▸ "
		}
		row := fmt.Sprintf("%s%d %s %s \x1b[38;5;245m%s\x1b[0m", marker, i+1, icon, c.label, st)
		if c.model != "" {
			row += fmt.Sprintf(" \x1b[38;5;240m· %s\x1b[0m", c.model)
		}
		if i == pick {
			row += "\x1b[0m"
		}
		lines = append(lines, row)
	}
	lines = append(lines, "", "\x1b[38;5;240m↑↓ move · 1-9 jump · ⏎ switch · esc cancel\x1b[0m")
	mx.drawCenteredBox(lines)
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
	os.Stdout.WriteString(mx.skin(b.String()))
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
	bar += "  \x1b[38;5;240mctrl-o launcher · ctrl-\\ w list · n/p switch · q quit\x1b[0m"
	bar = clipANSI(bar, cols)
	// save cursor, go to bar row, clear + paint, restore cursor
	mx.outMu.Lock()
	fmt.Fprintf(os.Stdout, "\x1b7\x1b[%d;1H\x1b[2K%s\x1b8", rows, mx.skin(bar))
	mx.outMu.Unlock()
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
