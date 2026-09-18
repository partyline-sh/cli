// Package ptymux is a local, single-terminal multiplexer for LLM CLI sessions: it
// hosts N PTY-backed children (each a ptysess.Session) inside the CURRENT terminal and
// cycles between them — one full-screen at a time, switched with a ctrl-\ prefix. NOT a
// pane-splitting terminal multiplexer (that's tmux's job); this is "windows". It composes
// ptysess for the PTY + VT emulator + Snapshot() and owns its own raw-mode input loop +
// prefix, since a local mux is single-owner (no grant/who/lock sharing semantics).
//
// The mux also hosts a "home" view (the `ptln llms` launcher) as a first-class screen:
// home is where you pick/launch sessions; launching spawns an in-process child and
// switches to it; ctrl-\ o drops back to home with the session still running. When the
// last child exits, the mux returns to home rather than quitting — the launcher persists.
package ptymux

import (
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"

	"partyline.sh/partyline/internal/brand"
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
	// Enter is called each time the mux returns to the launcher (e.g. ctrl-\ o, or the last
	// session closing) — a transition, NOT a resize repaint. The launcher uses it to reset
	// transient view state (a search filter) so you come back to the full list.
	Enter()
}

// PaneHome is a Home that can additionally render itself into an arbitrary rectangle, so the
// mux can host it INSIDE a split pane (ctrl-\ |). RenderLines must return position-INDEPENDENT
// rows — SGR colour escapes only, no cursor positioning, no erase-in-line/display — because the
// split painter clips, pads and absolutely positions every row itself (see splitFrame). The
// full-screen Render path is untouched by this.
type PaneHome interface {
	Home
	RenderLines(cols, rows int) []string
}

// HomeAction is the home view's response to a key. At most one of the verbs is set; an
// all-zero action means "stayed in home, re-render".
type HomeAction struct {
	Spawn      *Spec  // launch this session as a new in-process child, then switch to it
	SpawnMany  []Spec // launch several at once (multi-select), then focus the first
	SwitchKey  string // jump to an already-live child by key (skip launching a duplicate)
	SplitSetup bool   // open the guided empty split (the manager's bare `|`, same as ctrl-\ |)
	Return     bool   // go back to the session you came from (the active live child)
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
	Label       string   // shown in the status bar (e.g. "claude · payments")
	Key         string   // stable id (the llms session id) — dedupes "open" vs "switch"
	Model       string   // model name, shown in the session picker ("" hides it)
	Argv        []string // program + args (e.g. ["claude","--resume",id]); resumeArgv
	Dir         string   // working dir ("" inherits)
	Thread      string   // Common Ground thread this session is attached to ("" = none)
	ThreadLabel string   // the thread's human title, for the status-bar context indicator
	Engine      string   // engine name for PARTYLINE_ENGINE (claude/codex/gemini/antigravity)
	MCPs        []string // catalog MCP servers wired into this session (names; ctrl-\ m)

	// Group ties sessions that share one split window. Empty (the only value older workspace
	// files carry) means "its own window", so a file written before merging existed restores
	// exactly as it always did. The value is the Key of the group's first session.
	Group string `json:",omitempty"`
	// Layout is tmux's window_layout string, carried by the FIRST spec of a group so a resume
	// restores the pane sizes the human left, not an even split. Ignored when Group is empty.
	Layout string `json:",omitempty"`
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
	// loading: the session is booting — its screen is still blank, so the mux holds its output
	// (gate paused) and shows the animated splash until the screen renders content (or a timeout).
	loading   bool
	loadStart time.Time
	// startedAt: when this child was spawned. Used ONLY to tell "the program ran and later ended"
	// from "the program died on launch" — the second is an error the human needs to see, and it
	// used to vanish with the tab.
	startedAt time.Time
	// thread: the Common Ground thread this session is attached to ("" = none). Set at launch
	// (ptln new --thread) or later via the ctrl-\ c menu; drives that menu's target + the checkup.
	// threadWired: whether the AGENT actually has the recall/remember tools for that thread (true
	// only when the session was (re)launched wired — a plain SetActiveThread is record-only).
	thread      string
	threadLabel string   // the thread's human title, for the status-bar indicator
	engine      string   // engine name for this child's PARTYLINE_ENGINE
	mcps        []string // catalog MCP servers wired into this session (ctrl-\ m)
	threadWired bool
	shared      bool // this session is being broadcast live over the relay (ctrl-\ s share)
	// unsubmitted counts input bytes forwarded to this child SINCE the last CR/LF we forwarded —
	// i.e. how much the human has typed that they haven't submitted yet. It exists for exactly one
	// caller: something that wants to paste into the child's prompt and must not corrupt a
	// half-written one. writeActive is the ONLY path human input takes to a child (splits keep
	// mx.active pointing at the focused pane), so this counter sees everything the human types.
	//
	// It is a LOWER BOUND on "the prompt is dirty", never an upper one: the engine can put text in
	// its own input box with no stdin traffic at all (a restored draft, an inline completion). So
	// zero means "we have no evidence of unsubmitted input", not "the box is provably empty". Treat
	// non-zero as authoritative and zero as merely permissive.
	unsubmitted int
}

// Mux owns the terminal and the set of children.
type Mux struct {
	mu         sync.Mutex
	outMu      sync.Mutex // serializes EVERY write to os.Stdout (gate output, switch repaint, bar)
	queryMu    sync.Mutex // guards queryOwner (held only briefly; independent of mu/outMu)
	queryOwner *child     // child awaiting an async terminal reply on stdin; nil = none pending
	home       Home
	mode       uiMode
	children   []*child
	active     int
	cols, rows int
	fd         int
	old        *term.State
	sawPfx     bool
	// Tail of a terminal report whose terminator hasn't arrived yet, held across reads so a split
	// OSC/DCS is never forwarded to the child as typed input (pending.go). Single-goroutine: only
	// the stdin loop touches it.
	pendingIn  []byte
	pfxCh      *child    // the child paused while the ctrl-\ command panel is armed (resumed on dismiss)
	confirming bool      // a quit-confirmation prompt is on screen
	exitOnce   sync.Once // guards teardown: the normal return and the signal handler race for it
	// wake self-pipe: a background goroutine (e.g. the daemon presence stream) calls Wake()
	// to ask the input loop to repaint the launcher — the goroutine never writes to the
	// screen itself (the mux owns all output). Non-blocking pipe fds; poll(2) waits on both
	// stdin and the read end, so an async event surfaces a banner without a keypress.
	wakeR, wakeW int
	barActive    bool // ctrl-\ ←/→ tab selector is live (a candidate is highlighted on the bar)
	barSel       int  // candidate tab index while barActive (committed on ⏎, dropped on esc)
	// closeTarget: the BUSY child a y/n confirm is about (E9 slice 2). nil while confirming
	// means the prompt is quit's. Closing an idle tab never confirms — pruning stays fast;
	// the prompt exists only for the mis-chord that would eat live agent work.
	closeTarget *child
	// pairs are the BOUND splits: each owns one ribbon slot and outlives navigation, so a
	// paired session never falls apart into two tabs (see split.go / splittab.go).
	pairs []*pairSlot
	// split, when non-nil, is the pair currently PAINTED as two panes (ctrl-\ |). Parking a
	// split clears this and leaves its binding in pairs. EVERY full-width path below is
	// unchanged and reached only when split == nil.
	split     *splitState
	scrolling bool      // scrollback view is open for the active child (live paint is frozen)
	scrollOff int       // lines scrolled up from the live bottom (0 = live); per active child
	lastInput time.Time // wall-clock of the last keystroke — drives idle detection
	banner    string    // a transient notice shown in the status row (e.g. a Common Ground checkup); cleared on the next keystroke

	// The composed status bar, and whether it still needs to reach the terminal. Guarded by
	// outMu (not mu) because both writers — drawBar and gate.Write — already hold that lock, so
	// the paint can never split a child's write.
	barBytes   []byte
	barPending bool

	// StatusFn resolves a live child's key to "waiting" (your move) / "active"
	// (still working) / "" (unknown), for the quit-confirmation counts. Optional.
	StatusFn func(key string) string

	// barWake is a one-shot request from WakeBar for the woke path to repaint the status row.
	barWake bool

	// ToolActivityFn resolves a live child's key to the partyline tool it just called, and whether
	// that was recent enough to still be happening. Resolved at DRAW time like StatusFn (it stats a
	// file), so it must not be called with mx.mu held.
	ToolActivityFn func(key string) (string, bool)

	// BeforeQuit, if set, is called once with the open children (as Specs) at the moment
	// quit is confirmed — BEFORE teardown — so the workspace can be saved for --resume.
	BeforeQuit func([]Spec)

	// Skin, if set, re-colours the mux's OWN chrome (picker, quit prompt, status bar) to
	// match the launcher theme. Applied only to our overlays — never to child output.
	Skin func(string) string

	// LoadingFrame, if set, renders an animated splash (returns a full-screen frame for the
	// given phase) shown while a freshly-spawned session is still booting — i.e. before it
	// emits any output. Cleared automatically on the child's first byte. nil ⇒ no splash.
	LoadingFrame func(phase, cols, rows int) []byte
	loadPhase    int // advanced by the loading ticker (under outMu)

	// ContextFn, if set, is the `ctrl-\ c` action: the mux restores a normal terminal, runs it
	// (an interactive prompt — e.g. the Common Ground "record a fact / view context" menu), then
	// re-enters and repaints. Kept generic so ptymux knows nothing about Common Ground. nil ⇒
	// the `c` key and its menu label are hidden.
	ContextFn func()

	// ShareFn, if set, is the `ctrl-\ s` action (fire-and-forget — e.g. open a shared terminal
	// in a new tab). It runs on the input goroutine and should return promptly; surface any
	// result via SetBanner itself. nil ⇒ the `s` key and its menu label are hidden.
	ShareFn func()

	// MCPFn, if set, is the `ctrl-\ m` action: the MCP-server manager for the focused session
	// (same suspend/resume contract as ContextFn — it may queue a reattach). nil ⇒ the `m` key
	// and its menu label are hidden.
	MCPFn func()

	// WorktreeFn, if set, is the `ctrl-\ w` action: fork the focused session into a git
	// worktree (it queues a NEW tab via SetPendingOpen rather than replacing the session).
	// nil ⇒ the `w` key and its menu label are hidden.
	WorktreeFn func()

	// NewFn, if set, is the `ctrl-\ n` action: the New/Run menu (start a fresh AI session,
	// run an autonomous task, crank a backlog) — all opening a NEW tab via SetPendingOpen.
	// nil ⇒ the `n` key and its menu label are hidden.
	NewFn func()

	// KeepGoingFn, if set, is the `ctrl-\ g` action: arm/disarm keep-going on the focused
	// session (may queue a relaunch via SetPendingReattach). nil ⇒ the `g` key is hidden.
	KeepGoingFn func()

	// PeerFn, if set, is the `ctrl-\ p` action: ask a teammate's agent for read-only feedback
	// (ask_peer) and inject the answer into the focused session. Fire-and-forget — like ShareFn,
	// it never relaunches the session (it writes to the child's input). nil ⇒ the `p` key is hidden.
	PeerFn func()

	// NewPaneHomeFn, if set, builds a FRESH, independent session manager for one split pane —
	// ctrl-\ | needs two of them (one per pane) with their own selection/scroll state. nil ⇒
	// `|` does nothing. It is called on the input goroutine, never concurrently.
	NewPaneHomeFn func() PaneHome

	// pendingReattach: a session queued (by ContextFn, via SetPendingReattach) to relaunch in
	// place of the focused one — run right after suspend re-enters, so we never spawn into cooked
	// mode. See the ctrl-\ c handler.
	pendingReattach *Spec
	// pendingOpen: a NEW session queued (by WorktreeFn, via SetPendingOpen) to open as an
	// additional tab — same "never spawn into cooked mode" rule as pendingReattach.
	pendingOpen *Spec
}

// ActiveSession returns the focused live child's session + label, or (nil, "") if the launcher
// (home) is showing. Lets a ShareFn act on the session the user is looking at.
func (mx *Mux) ActiveSession() (*ptysess.Session, string) {
	mx.mu.Lock()
	defer mx.mu.Unlock()
	if mx.mode != modeLive || mx.active < 0 || mx.active >= len(mx.children) {
		return nil, ""
	}
	ch := mx.children[mx.active]
	return ch.sess, ch.label
}

// ActiveMCPs returns the catalog MCP servers wired into the focused session (nil if none or on
// the launcher) — the ctrl-\ m menu's current state, and preserved across thread attach/detach.
func (mx *Mux) ActiveMCPs() []string {
	mx.mu.Lock()
	defer mx.mu.Unlock()
	if mx.mode != modeLive || mx.active < 0 || mx.active >= len(mx.children) {
		return nil
	}
	return append([]string(nil), mx.children[mx.active].mcps...)
}

// ActiveThread returns the Common Ground thread the focused session is attached to ("" if none
// or on the launcher) — used by the checkup to know what to watch.
func (mx *Mux) ActiveThread() string {
	t, _ := mx.ActiveThreadInfo()
	return t
}

// ActiveThreadInfo returns the focused session's thread and whether the AGENT is actually wired
// to it (has the recall/remember tools) vs. merely record-only. The ctrl-\ c menu uses `wired` to
// avoid claiming "attached" when it isn't.
func (mx *Mux) ActiveThreadInfo() (thread string, wired bool) {
	mx.mu.Lock()
	defer mx.mu.Unlock()
	if mx.mode != modeLive || mx.active < 0 || mx.active >= len(mx.children) {
		return "", false
	}
	ch := mx.children[mx.active]
	return ch.thread, ch.threadWired
}

// SetActiveThread binds a thread to the focused session for RECORD-ONLY use (the menu writes as
// you + the checkup watches it) without relaunching the agent — so it's marked not-wired.
func (mx *Mux) SetActiveThread(id, label string) {
	mx.mu.Lock()
	defer mx.mu.Unlock()
	if mx.active >= 0 && mx.active < len(mx.children) {
		mx.children[mx.active].thread = id
		mx.children[mx.active].threadLabel = label
		mx.children[mx.active].threadWired = false
	}
}

// ActiveLaunch returns the focused child's launch argv + dir + label + key, so a caller can
// rebuild it (e.g. relaunch it wired to a thread). ok is false on the launcher.
func (mx *Mux) ActiveLaunch() (argv []string, dir, label, key string, ok bool) {
	mx.mu.Lock()
	defer mx.mu.Unlock()
	if mx.mode != modeLive || mx.active < 0 || mx.active >= len(mx.children) {
		return nil, "", "", "", false
	}
	ch := mx.children[mx.active]
	return append([]string(nil), ch.argv...), ch.dir, ch.label, ch.key, true
}

// SetPendingReattach queues a session to relaunch in place of the focused one. The ctrl-\ c
// handler runs it AFTER suspend re-enters raw+alt-screen (never spawn into a cooked terminal).
func (mx *Mux) SetPendingReattach(sp Spec) { mx.pendingReattach = &sp }

// SetPendingOpen queues a NEW session to open (as an additional tab) right after the
// current cooked-mode menu returns — the ctrl-\ w fork uses it.
func (mx *Mux) SetPendingOpen(sp Spec) { mx.pendingOpen = &sp }

// TakePendingOpen pops the queued session, if any. For hosts that route spawns to an
// external backend (the tmux graduation): the menu queues exactly as before, and the
// wrapper drains the queue before the mux's own drain would spawn it in-process.
func (mx *Mux) TakePendingOpen() *Spec {
	sp := mx.pendingOpen
	mx.pendingOpen = nil
	return sp
}

// SetActiveShared marks/unmarks the focused session as being broadcast live over the relay, so
// its tab shows the share glyph. Called by the ctrl-\ s share/stop actions on the session they
// act on (always the focused one).
func (mx *Mux) SetActiveShared(v bool) {
	mx.mu.Lock()
	if mx.active >= 0 && mx.active < len(mx.children) {
		mx.children[mx.active].shared = v
	}
	mx.mu.Unlock()
}

// ReplaceActive launches sp and closes the currently-focused child — the "relaunch wired" behind
// attaching a thread. It spawns + focuses the new child first (reusing the tested paths), then
// ends the old one; watchExit removes the old and the active index stays on the new child.
func (mx *Mux) ReplaceActive(sp Spec) {
	mx.mu.Lock()
	var old *child
	if mx.active >= 0 && mx.active < len(mx.children) {
		old = mx.children[mx.active]
	}
	mx.mu.Unlock()
	mx.spawnOrSwitch(sp) // appends + focuses the new child (distinct key → never dedups to old)
	if old != nil {
		old.sess.End() // its watchExit removes it; active stays on the new child (see watchExit)
	}
}

func (mx *Mux) skin(s string) string {
	if mx.Skin != nil {
		return mx.Skin(s)
	}
	return s
}

// LiveSpecs is liveSpecs for callers outside the package. Exported so the workspace snapshot
// can be taken WHILE the mux runs, not only at quit: a mux that is killed (binary swapped,
// crash, SIGKILL) never reaches BeforeQuit, and a snapshot that only exists on a clean quit
// silently goes stale the moment you open a session and don't quit.
func (mx *Mux) LiveSpecs() []Spec { return mx.liveSpecs() }

// liveSpecs snapshots the open children as Specs (for workspace save/restore).
func (mx *Mux) liveSpecs() []Spec {
	mx.mu.Lock()
	defer mx.mu.Unlock()
	out := make([]Spec, 0, len(mx.children))
	for _, c := range mx.children {
		out = append(out, Spec{Label: c.label, Key: c.key, Model: c.model, Argv: c.argv, Dir: c.dir, Thread: c.thread, ThreadLabel: c.threadLabel, Engine: c.engine})
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
// SpawnProgress, when set, is called immediately before each initial spec passed to New is
// spawned, with its index and the total. It exists so a launch door can report "reopening
// <label> (3 of 7)" on its boot splash while `ptln --resume` works through a saved workspace —
// the one wait whose cost scales with how much there is to restore.
//
// Display only: it never changes what is spawned or in what order, and a slow hook just delays
// the spawn it precedes, so keep it cheap.
var SpawnProgress func(sp Spec, i, n int)

func New(home Home, specs []Spec) (*Mux, error) {
	mx := &Mux{home: home, wakeR: -1, wakeW: -1}
	// Self-pipe for Wake(): non-blocking + close-on-exec so a wake never blocks the caller
	// and the fds don't leak into spawned children. (unix.Pipe is portable — Pipe2 is
	// Linux-only.) Best-effort — without it, Wake() is a no-op and the launcher just falls
	// back to repaint-on-keypress.
	var p [2]int
	if err := unix.Pipe(p[:]); err == nil {
		for _, fd := range p {
			_ = unix.SetNonblock(fd, true)
			unix.CloseOnExec(fd)
		}
		mx.wakeR, mx.wakeW = p[0], p[1]
	}
	for i, sp := range specs {
		if SpawnProgress != nil {
			SpawnProgress(sp, i, len(specs))
		}
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

// Wake asks the input loop to repaint the launcher. Safe to call from any goroutine: it's a
// single non-blocking byte into the self-pipe (EAGAIN — pipe already has a pending wake — is
// fine, the loop will repaint once). A no-op if the pipe couldn't be created.
// WakeBar asks for an async repaint of the STATUS ROW, for a change that is neither a keystroke nor
// a banner — today, a partyline tool call lighting a session's ☎ marker.
//
// Separate from Wake() because the woke path is deliberately conservative: it repaints only for a
// banner, since a blind repaint on every wake would fire for every checkup tick. This sets a flag the
// woke branch consumes exactly once, so a caller gets one paint per thing it actually changed.
func (mx *Mux) WakeBar() {
	mx.mu.Lock()
	mx.barWake = true
	mx.mu.Unlock()
	mx.Wake()
}

func (mx *Mux) Wake() {
	if mx.wakeW >= 0 {
		_, _ = unix.Write(mx.wakeW, []byte{0})
	}
}

// IdleSince reports how long since the last keystroke — the idle clock the Common Ground
// post-gap checkup polls. Zero (never-typed) reads as 0 so a just-launched session isn't
// treated as "away." Safe from a background goroutine.
func (mx *Mux) IdleSince() time.Duration {
	mx.mu.Lock()
	t := mx.lastInput
	mx.mu.Unlock()
	if t.IsZero() {
		return 0
	}
	return time.Since(t)
}

// SetBanner shows a transient one-line notice in the status row and wakes the input loop to
// paint it WITHOUT a keypress. It's informational only — never injected into the agent — and
// is cleared on the next keystroke (the user acknowledging it). BannerActive lets a poller
// avoid stacking notices.
func (mx *Mux) SetBanner(s string) {
	mx.mu.Lock()
	mx.banner = s
	mx.mu.Unlock()
	mx.Wake()
}

func (mx *Mux) BannerActive() bool {
	mx.mu.Lock()
	defer mx.mu.Unlock()
	return mx.banner != ""
}

// waitInput blocks until stdin has input or Wake() fired, via poll(2) on both fds. Returns
// wake=true when only the self-pipe signaled (drained here). EINTR (a SIGWINCH landing on the
// poll) just retries. If the wake pipe is unavailable it falls back to a plain blocking read
// signal (stdin only), so the loop still works.
func (mx *Mux) waitInput() (wake bool, err error) {
	if mx.wakeR < 0 {
		return false, nil // no pipe → caller reads stdin directly (blocking)
	}
	fds := []unix.PollFd{
		{Fd: int32(mx.fd), Events: unix.POLLIN},
		{Fd: int32(mx.wakeR), Events: unix.POLLIN},
	}
	for {
		if _, e := unix.Poll(fds, -1); e != nil {
			if e == unix.EINTR {
				continue // a signal (e.g. SIGWINCH) interrupted the wait — retry
			}
			return false, e
		}
		if fds[1].Revents&unix.POLLIN != 0 { // drain all pending wake bytes
			var tmp [64]byte
			for {
				if n, e := unix.Read(mx.wakeR, tmp[:]); n <= 0 || e != nil {
					break
				}
			}
			if fds[0].Revents&unix.POLLIN == 0 { // wake only — no stdin to read
				return true, nil
			}
		}
		if fds[0].Revents&(unix.POLLIN|unix.POLLHUP|unix.POLLERR) != 0 {
			return false, nil // stdin readable (or closed) — read it
		}
	}
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
// childEnv builds a spawned session's FULL environment. Critically it strips any inherited
// PARTYLINE_THREAD_ID / PARTYLINE_ENGINE from our own env, then adds back ONLY this child's own
// (from its Spec) — so a session's thread lives in its own process env, never in a shared global.
// Two sessions on different threads therefore can't cross-contaminate, and a re-spawn keeps its
// own thread regardless of what any other session set. (The old bug: these rode a process-global
// os.Setenv on the mux, so the last launch/attach won for everyone.)
func childEnv(sp Spec) []string {
	env := os.Environ()
	out := make([]string, 0, len(env)+3)
	for _, kv := range env {
		if strings.HasPrefix(kv, "PARTYLINE_THREAD_ID=") || strings.HasPrefix(kv, "PARTYLINE_ENGINE=") ||
			strings.HasPrefix(kv, "PARTYLINE_SESSION_KEY=") || strings.HasPrefix(kv, "PARTYLINE_SESSION_LABEL=") {
			continue // never inherit another session's (or a stale global) thread/engine/identity
		}
		out = append(out, kv)
	}
	out = append(out, "PARTYLINE=1")
	// PARTYLINE_SESSION_KEY tells a child (and anything it spawns, e.g. the cg-mcp server) WHICH
	// session it is running in. Without it an ask_peer made by the agent is anonymous, and an answer
	// coming back has no session to be delivered to — the whole reason it's here. Stripped above so a
	// nested launch can never inherit a sibling's identity and steer a delivery at the wrong agent.
	if sp.Key != "" {
		out = append(out, "PARTYLINE_SESSION_KEY="+sp.Key)
	}
	// PARTYLINE_SESSION_LABEL is the same identity in human form. ask_session puts it in the framing
	// the TARGET reads ("ACR FLEET MANAGER is asking you…"), so an arriving question is attributable
	// at a glance instead of reading like the human changing subject mid-thought. Stripped above with
	// the key for the same reason: an inherited label would misattribute a nested session's question.
	if sp.Label != "" {
		out = append(out, "PARTYLINE_SESSION_LABEL="+sp.Label)
	}
	if sp.Thread != "" {
		out = append(out, "PARTYLINE_THREAD_ID="+sp.Thread)
	}
	if sp.Engine != "" {
		out = append(out, "PARTYLINE_ENGINE="+sp.Engine)
	}
	return out
}

func (mx *Mux) spawn(sp Spec) error {
	sess, err := ptysess.NewIn(sp.Dir, sp.Argv, "you", false, childEnv(sp))
	if err != nil {
		return err
	}
	g := &gate{out: &mx.outMu, mx: mx}
	cols, rows := mx.bodySize()
	part := sess.Attach(sp.Label, g, cols, rows, true, true)
	// A session spawned WITH a thread is wired (llmsNew adds the cg-mcp config; ReplaceActive and
	// workspace-restore carry the wiring in argv). A thread set later via SetActiveThread is not.
	ch := &child{startedAt: time.Now(), label: sp.Label, key: sp.Key, model: sp.Model, argv: sp.Argv, dir: sp.Dir, thread: sp.Thread, threadLabel: sp.ThreadLabel, engine: sp.Engine, mcps: sp.MCPs, threadWired: sp.Thread != "", sess: sess, part: part, gate: g}
	g.ch = ch
	mx.mu.Lock()
	mx.children = append(mx.children, ch)
	mx.mu.Unlock()
	go mx.watchExit(ch)
	return nil
}

// LiveKeys returns the set of currently-live child keys, for the home view to mark which
// sessions are already running in the mux.
// LiveSession is one open session as an addressing surface sees it: the stable child key plus the
// human name on its tab. Deliberately not the *Session — a caller listing what is open has no
// business writing to any of them.
type LiveSession struct {
	Key   string
	Label string
}

// LiveSessions lists the open sessions by key and name — what ask_session resolves a name against,
// and what list_sessions shows an agent so it can address one correctly. Keyless children (a bare
// shell opened from the launcher) are omitted: without a key there is no address to deliver to.
func (mx *Mux) LiveSessions() []LiveSession {
	mx.mu.Lock()
	defer mx.mu.Unlock()
	out := make([]LiveSession, 0, len(mx.children))
	for _, c := range mx.children {
		if c.key != "" {
			out = append(out, LiveSession{Key: c.key, Label: c.label})
		}
	}
	return out
}

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
	out     *sync.Mutex // shared mux output lock — serializes the active-check WITH the write
	mx      *Mux        // back-ref: records this child as the "query owner" when its output queries the terminal
	ch      *child      // the child this gate belongs to (set after construction)
	active  bool
	paused  bool // don't paint (scroll viewport owns the screen, OR a loading splash is up)
	modes   modeState
	lastOut atomic.Int64 // unixnano of the child's last PTY output — the activity signal for the
	// switcher's status dot. Atomic so drawBar reads it without the output lock. Stamped on EVERY
	// child byte (even while inactive), so a background session's "working" state is still fresh.
}

// Write holds the shared output lock across BOTH the active-check and the stdout write, so
// once setActive(false) returns, this child can never paint again — no in-flight write can
// land after a switch and leave stale text on the new screen.
func (g *gate) Write(b []byte) (int, error) {
	g.lastOut.Store(time.Now().UnixNano()) // activity signal for the switcher's status dot
	g.out.Lock()
	g.modes.observe(b) // always sniff modes (even inactive) so a switch can restore them
	live := g.active && !g.paused
	if live {
		// THE CHILD OWNS THE SCREEN WHILE IT IS DRAWING. Its bytes go through untouched and nothing
		// of ours is interleaved with them. The mux briefly injected here — clamping the child's
		// scroll region on the way out, and re-asserting the bar on a 100ms keepalive — to make the
		// bar permanent rather than summoned. Both wrote to the terminal without the child knowing,
		// and a child that redraws DIFFERENTIALLY (Claude Code does) never rewrites the cells it
		// believes are already correct, so whatever we left in them survived: stale glyphs in the
		// spaces between words, a composer drawn a row off. The bar is summoned again (ctrl-\, tab
		// switch) and may scroll away in between — an accepted cost, and the only version of this
		// that a pass-through mux can keep correct.
		os.Stdout.Write(b)
		// This child's frame may have just closed — that is the only safe moment to paint a bar
		// that was held back, and we are already holding the output lock.
		if g.mx != nil {
			g.mx.flushBarLocked(&g.modes)
		}
	}
	g.out.Unlock()
	// Only a child whose output actually reached the real terminal can trigger an async reply
	// on stdin. Record it as the "query owner" so that reply is routed back to IT — not to
	// whoever happens to be active when the reply lands (see handleInput). A backgrounded
	// child's query is discarded above, so it never causes a reply. queryMu is separate from
	// the out lock, so this can't deadlock the write path.
	if live && g.mx != nil && containsTerminalQuery(b) {
		g.mx.noteQuery(g.ch)
	}
	return len(b), nil
}

func (g *gate) setActive(a bool) {
	g.out.Lock()
	g.active = a
	g.out.Unlock()
}

// restoreModes returns this child's DEC-private-mode re-assertion under the output lock (the
// same lock observe() runs under), for a caller that builds a frame outside outMu.
func (g *gate) restoreModes() []byte {
	g.out.Lock()
	defer g.out.Unlock()
	return g.modes.restore()
}

// setPaused freezes (or resumes) live painting for this child without touching active —
// scroll mode pauses the active child so the scrollback viewport owns the screen, then
// resumes it on exit. The vt keeps being fed either way (Write still sniffs).
func (g *gate) setPaused(p bool) {
	g.out.Lock()
	g.paused = p
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

	// ?2026 — SYNCHRONIZED OUTPUT. Not a mode we restore; a transient "the child is mid-frame"
	// flag, and the reason this field exists at all.
	//
	// Claude Code brackets EVERY repaint in ?2026h … ?2026l and draws entirely with RELATIVE
	// motion inside it (measured: 44 frames while typing one line, zero absolute positioning,
	// zero DECSC). So a frame is a transaction whose meaning depends on the cursor being exactly
	// where the child left it. If the status bar paints in the middle of one, its cursor jump and
	// its text are composed INTO the child's frame — the ribbon lands wherever the child's cursor
	// was instead of the bottom row, and every relative move the child makes afterwards is offset,
	// which is the interleaved input line.
	inFrame   bool
	frameOpen time.Time // when ?2026h arrived — bounds how long a paint may be deferred
}

// frameGrace bounds deferral. observe() only recognises a sequence contained in ONE write, so a
// ?2026l split across two reads would otherwise leave inFrame stuck true and the bar never
// repainted again. Deferring is an optimisation; going stale is a visible bug — so past this, we
// paint regardless.
const frameGrace = 150 * time.Millisecond

// midFrame reports whether a child is inside a synchronized-update bracket right now.
func (m *modeState) midFrame() bool {
	return m.inFrame && time.Since(m.frameOpen) < frameGrace
}

// the private modes we care to restore (others are left at terminal default)
func trackedMode(n int) bool {
	switch n {
	case 25, 1, 2004, 1000, 1002, 1003, 1005, 1006, 1015:
		return true
	case 2026: // observed for frame timing, never re-asserted (see modeState.inFrame)
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
	case 2026:
		// Deliberately NOT stored for restore(): re-asserting "begin frame" on a switch would
		// open a bracket nobody closes.
		m.inFrame = set
		if set {
			m.frameOpen = time.Now()
		}
		return
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
	mx.lastInput = time.Now() // start the idle clock at launch (not "away" out of the gate)
	defer mx.teardown(false)

	// Animate the loading splash for a still-booting active session (stopped on return).
	if mx.LoadingFrame != nil {
		stop := make(chan struct{})
		defer close(stop)
		go mx.loadingLoop(stop)
	}

	os.Stdout.WriteString("\x1b[?1049h\x1b[?25l") // alt-screen, hide cursor
	// Mouse mode follows the focused child: restore() re-emits only what the child set, so
	// claude (which turns mouse on) scrolls itself, while codex/gemini/shells leave it off
	// and the terminal keeps native selection + scroll (copy/paste). The mux never grabs the
	// mouse itself; history scroll in the mux is the keyboard (ctrl-\ [). Launcher = mouse-off.
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

	// Raw mode disables ISIG, so a keyboard ctrl-C reaches the input loop as a byte and this
	// never fires. It fires when a BLOCKING surface has handed the terminal back to cooked
	// mode (e.g. a poll that reads no stdin at all, where no keypress can cancel) — there,
	// ctrl-C is the user's only way out, and the default disposition would kill us mid-flight.
	// It MUST run BeforeQuit: that hook writes the --resume workspace snapshot, so skipping it
	// silently loses every open session. Cleanup is idempotent (teardown), so this races
	// harmlessly with a normal quit already in progress.
	kill := make(chan os.Signal, 1)
	signal.Notify(kill, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(kill)
	go func() {
		<-kill
		mx.teardown(true)
		os.Exit(130)
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
		woke, werr := mx.waitInput()
		if werr != nil {
			return werr
		}
		if woke { // Wake() fired with no keypress — repaint to surface an async notice
			mx.mu.Lock()
			homeIdle := mx.mode == modeHome && !mx.confirming && !mx.barActive
			liveBanner := mx.mode != modeHome && !mx.confirming && !mx.barActive && !mx.scrolling &&
				(mx.banner != "" || mx.barWake)
			mx.barWake = false // one-shot: consumed whether or not this wake could paint
			mx.mu.Unlock()
			if homeIdle {
				mx.renderHome()
			} else if liveBanner {
				mx.drawBar() // surface a checkup banner in a live session without a keypress
			}
			continue
		}
		n, err := os.Stdin.Read(buf)
		if err != nil || n == 0 {
			return nil
		}
		mx.mu.Lock()
		md := mx.mode
		confirming := mx.confirming
		barActive := mx.barActive
		mx.lastInput = time.Now() // idle clock for the post-gap checkup
		clearedBanner := mx.banner != ""
		mx.banner = "" // any keystroke acknowledges/dismisses a checkup banner
		mx.mu.Unlock()
		if clearedBanner {
			mx.drawBar() // repaint the status row without the dismissed banner
		}
		if confirming {
			if mx.handleConfirm(buf[:n]) {
				return nil // confirmed quit
			}
			continue
		}
		if barActive { // the ctrl-\ ←/→ tab selector owns input until ⏎ / esc
			mx.handleBarSelect(buf[:n])
			continue
		}
		if md == modeHome {
			// The launcher is keyboard-only and runs with mouse OFF, so the terminal keeps
			// native selection/scroll here too. legacyKeys normalizes even if a child left
			// kitty mode on.
			norm := legacyKeys(buf[:n])
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
			mx.mu.Lock()
			scrolling := mx.scrolling
			mx.mu.Unlock()
			if scrolling {
				mx.handleScroll(norm)
			} else if mx.handleInput(norm) {
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
	case act.SplitSetup: // bare `|` in the manager → the guided empty split
		mx.startEmptySplit()
		mx.mu.Lock()
		opened := mx.split != nil
		mx.mu.Unlock()
		if !opened {
			mx.renderHome() // declined (terminal too narrow) — stay in the manager
		}
	case act.Return: // esc from the manager → back to the session you came from
		mx.mu.Lock()
		a, n := mx.active, len(mx.children)
		mx.mu.Unlock()
		if n > 0 {
			mx.gotoLive(a) // out-of-range a falls back to home
		} else {
			mx.renderHome()
		}
	case act.Spawn != nil:
		mx.spawnOrSwitch(*act.Spawn)
	case len(act.SpawnMany) > 0:
		mx.spawnMany(act.SpawnMany)
	case act.Suspend != nil:
		mx.suspend(act.Suspend)
		// The suspended fn may have queued a new tab (the New/Run menu does — #160). Drain it
		// exactly like the in-session chord path, or a session picked from the front door
		// would silently never open.
		if sp := mx.pendingOpen; sp != nil {
			mx.pendingOpen = nil
			mx.spawnOrSwitch(*sp)
			return false // spawnOrSwitch repainted into the new session
		}
	default:
		mx.renderHome() // stayed in home; repaint for the state change
	}
	return false
}

// handleInput processes a chunk of (normalized) live-mode input; returns true to quit.
func (mx *Mux) handleInput(data []byte) bool {
	// A terminal report can be BIGGER THAN ONE READ — an OSC 52 clipboard reply carrying a screenful
	// of selected text runs to several KB — so it arrives split. matchTerminalReport can only answer
	// "is a complete report at the head of this buffer", and returned 0 for a half-arrived one, which
	// sent it to the child as if the human had typed it: select some text, get a wall of base64 in
	// your composer. Hold the unterminated tail and glue it to the next read (see pending.go).
	if len(mx.pendingIn) > 0 {
		data = append(mx.pendingIn, data...)
		mx.pendingIn = nil
	}
	if n := pendingReportTail(data); n > 0 {
		mx.pendingIn = append([]byte(nil), data[len(data)-n:]...)
		data = data[:len(data)-n]
		if len(data) == 0 {
			return false
		}
	}
	var fwd []byte
	flush := func() {
		if len(fwd) > 0 {
			mx.writeActive(fwd)
			fwd = nil
		}
	}
	i := 0
	for i < len(data) {
		// Mouse reports flow straight through to the child as ordinary input. Mouse mode is
		// only ever on when the focused child asked for it (claude scrolls itself that way);
		// children that don't want the mouse never get reports, because the mux leaves mouse
		// off for them so the terminal keeps native selection + scroll (copy/paste works).
		// Scroll back through the mux's own per-child buffer with the keyboard: ctrl-\ [
		if mx.sawPfx {
			mx.sawPfx = false
			// Arrow after the prefix starts the bar selector: a highlight moves along the
			// bottom bar (←/→), ⏎ commits the switch, esc cancels. handleBarSelect owns
			// input until then. Up/down are consumed (no-op) so they don't leak to the child.
			if _, an := arrowAt(data[i:]); an > 0 {
				flush()
				mx.disarmPanel(true) // the selector only repaints the bar row — clear the panel first
				mx.startBarSelect(data[i : i+an])
				i += an
				continue
			}
			// Otherwise the command key may arrive as a plain byte, a ctrl-modified byte
			// (ctrl held through the chord), or a CSI-u / modifyOtherKeys escape sequence
			// (apps that report all keys as escape codes). decodeCmdKey resolves all three
			// to the base letter/digit so the chord works no matter the encoding.
			k, n := decodeCmdKey(data[i:])
			switch {
			case k == 0x1c: // literal ctrl-\ → child
				fwd = append(fwd, 0x1c)
			case k == 'o': // the session manager / launcher
				flush()
				mx.gotoHome()         // clears the screen (covers the panel) with the child still paused
				mx.disarmPanel(false) // resume the now-backgrounded child; no extra repaint
			case k == '|': // open an EMPTY split: a session manager in both panes
				flush()
				mx.startEmptySplit()
				mx.disarmPanel(false) // startEmptySplit repainted the whole screen — just resume
				return false
			// ctrl-\ tab arrives as 'i' (tab IS ctrl-I — see decodeCmdKey), so both work.
			case (k == 'i' || k == '\t') && mx.splitActive(): // move focus between panes
				flush()
				mx.splitFocusNext()
				mx.disarmPanel(false)
				return false
			case k == 'z' && mx.splitActive(): // zoom the focused pane to full width (toggle)
				flush()
				mx.splitZoom()
				mx.disarmPanel(false)
				return false
			// Scrollback is a full-width viewport, so it's unavailable while split (the key is
			// swallowed by the default arm below rather than corrupting the panes).
			case k == '[' && !mx.splitActive(): // enter scrollback (keyboard; tmux-style ctrl-\ [) — the wheel
				flush()                // belongs to the child now, so history scroll is a keystroke
				mx.scrollBy(wheelStep) // pauses the child + paints the viewport over the panel
				mx.forgetPanel()       // scroll mode now owns the pause; exitScroll resumes it
			case k == 'c' && mx.ContextFn != nil: // Common Ground: record/view shared context
				flush()
				mx.suspend(mx.ContextFn)
				// The menu may have queued a "relaunch this session wired to a thread" — do it now,
				// back in raw + alt-screen (never spawn into the cooked prompt).
				if sp := mx.pendingReattach; sp != nil {
					mx.pendingReattach = nil
					mx.ReplaceActive(*sp)
				}
				mx.disarmPanel(false) // suspend/ReplaceActive repainted over the panel — just resume
				return false          // suspend repainted; drop the rest of this input batch
			case k == 's' && mx.ShareFn != nil: // share this session / open a shared tab
				flush()
				mx.suspend(mx.ShareFn)
				mx.disarmPanel(false) // suspend repainted over the panel — just resume the child
				return false          // suspend repainted; drop the rest of this input batch
			case k == 'p' && mx.PeerFn != nil: // ask a teammate's agent (ask_peer) → inject the answer here
				flush()
				mx.suspend(mx.PeerFn) // fire-and-forget: PeerFn writes to the child's input, never relaunches
				mx.disarmPanel(false) // suspend repainted over the panel — just resume the child
				return false          // suspend repainted; drop the rest of this input batch
			case k == 'm' && mx.MCPFn != nil: // MCP servers: wire/unwire for this session
				flush()
				mx.suspend(mx.MCPFn)
				// A toggle may have queued "relaunch this session re-wired" — same contract as
				// the ctrl-\ c menu: do it back in raw + alt-screen.
				if sp := mx.pendingReattach; sp != nil {
					mx.pendingReattach = nil
					mx.ReplaceActive(*sp)
				}
				mx.disarmPanel(false) // suspend/ReplaceActive repainted over the panel — just resume
				return false          // suspend repainted; drop the rest of this input batch
			case k == 'w' && mx.WorktreeFn != nil: // fork this session into a git worktree
				flush()
				mx.suspend(mx.WorktreeFn)
				// The fork queues a NEW tab (not a replace) — open it back in raw mode.
				if sp := mx.pendingOpen; sp != nil {
					mx.pendingOpen = nil
					mx.spawnOrSwitch(*sp)
				}
				mx.disarmPanel(false) // suspend/spawn repainted over the panel — just resume
				return false          // suspend repainted; drop the rest of this input batch
			case k == 'n' && mx.NewFn != nil: // New/Run: fresh session · autonomous task · crank
				flush()
				mx.suspend(mx.NewFn)
				if sp := mx.pendingOpen; sp != nil { // the menu queued a new tab
					mx.pendingOpen = nil
					mx.spawnOrSwitch(*sp)
				}
				mx.disarmPanel(false) // suspend/spawn repainted over the panel — just resume
				return false          // suspend repainted; drop the rest of this input batch
			case k == 'g' && mx.KeepGoingFn != nil: // keep-going: arm/disarm auto-continue
				flush()
				mx.suspend(mx.KeepGoingFn)
				if sp := mx.pendingReattach; sp != nil { // arm/disarm relaunches in place
					mx.pendingReattach = nil
					mx.ReplaceActive(*sp)
				}
				mx.disarmPanel(false) // suspend/ReplaceActive repainted over the panel — just resume
				return false          // suspend repainted; drop the rest of this input batch
			case k == 'x':
				flush()
				if mx.splitActive() {
					mx.closePane() // close the pane, not the program — back to full width on the other session
				} else {
					mx.closeActive()
				}
			case k == 'q':
				flush()
				if mx.requestQuit() { // confirm if sessions are running
					return true
				}
				mx.disarmPanel(false) // the confirm box (or quit) cleared the screen — just resume
				return false
			case k >= '1' && k <= '9':
				flush()
				mx.jump(int(k - '1')) // switchTo repaints the whole screen over the panel
				mx.disarmPanel(false) // resume the now-backgrounded child; no extra repaint
			default: // unknown prefix key — swallow the whole sequence
			}
			// A chord that stayed live WITHOUT its own repaint (literal ctrl-\, unknown key, esc
			// to cancel, x-close) still has the command panel painted over the child. Only these
			// paths leave mx.pfxCh set: repaintLive erases the panel cleanly (child still paused)
			// and resumes. Transitions above cleared the panel themselves (disarmPanel/forgetPanel),
			// so pfxCh is nil there and we only touch the bottom bar.
			mx.mu.Lock()
			stillLive := mx.mode == modeLive && !mx.barActive && !mx.scrolling && !mx.confirming
			armed := mx.pfxCh != nil
			mx.mu.Unlock()
			if stillLive {
				if armed {
					mx.disarmPanel(true) // repaintLive (clean erase) + resume the paused child
				} else {
					mx.drawBar()
				}
			}
			i += n
			continue
		}
		if data[i] == 0x1c {
			flush()
			mx.sawPfx = true
			i++
			mx.armPanel() // surface the command panel the instant ctrl-\ is pressed (not blind)
			continue
		}
		// The focused pane still shows its session manager (an unfilled split pane), so typed
		// keys belong to that manager, not to a child PTY. The ctrl-\ chord above is handled
		// first, so every mux command still works; everything else goes to the manager — and
		// the whole remaining chunk goes with it (a menu key can be a multi-byte escape).
		if mx.splitHomeFocused() {
			flush()
			return mx.splitHomeKey(legacyKeys(data[i:]))
		}
		// Terminal report replies (DSR/DA/window-op/DECRPM, or an OSC color/title report) arrive
		// asynchronously on stdin and belong to the child that ISSUED the query — not whoever is
		// active now. If the user switched tabs in the gap, forwarding to the active child would
		// type the reply (e.g. an OSC title report carrying the tab name) into the wrong session.
		// Route it to the recorded query owner instead; drop it if nothing is waiting.
		if data[i] == 0x1b {
			if rn := matchTerminalReport(data[i:]); rn > 0 {
				flush() // keep any real input already queued ahead of this in order to the active child
				if owner := mx.takeQueryOwner(); owner != nil {
					owner.sess.WriteInput(append([]byte(nil), data[i:i+rn]...))
				}
				i += rn
				continue
			}
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
	if ch != nil {
		ch.noteInput(b) // track unsubmitted input while we hold the lock — see child.unsubmitted
	}
	mx.mu.Unlock()
	if ch != nil {
		ch.sess.WriteInput(b)
	}
}

// noteInput folds one forwarded input chunk into the unsubmitted count. A CR or LF is the human
// submitting, so the count restarts from whatever followed the LAST one in the chunk. Caller holds mu.
func (ch *child) noteInput(b []byte) {
	if i := lastSubmitIndex(b); i >= 0 {
		ch.unsubmitted = len(b) - i - 1
		return
	}
	ch.unsubmitted += len(b)
}

// lastSubmitIndex returns the index of the last CR/LF in b, or -1. Both count: engines take either,
// and bracketed paste is stripped upstream so a newline here means the human pressed Enter.
func lastSubmitIndex(b []byte) int {
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] == '\r' || b[i] == '\n' {
			return i
		}
	}
	return -1
}

// SessionByKey resolves a live child key (the llms session id) to its session. This is how a
// background deliverer reaches THE SESSION THAT ASKED and only that one — never the focused session,
// never a broadcast. ok=false means that session isn't live in this mux (it exited, or it's running
// under a different mux), which the caller must handle as "leave it in the store".
func (mx *Mux) SessionByKey(key string) (sess *ptysess.Session, label, dir string, ok bool) {
	if key == "" {
		return nil, "", "", false // no origin recorded → nothing to target. Do NOT fall back.
	}
	mx.mu.Lock()
	defer mx.mu.Unlock()
	for _, ch := range mx.children {
		if ch.key == key {
			return ch.sess, ch.label, ch.dir, true
		}
	}
	return nil, "", "", false
}

// UnsubmittedInput reports how many input bytes the human has typed into this session without
// submitting them. known=false means the session isn't live here (so the question is meaningless).
// See child.unsubmitted for why 0 is permissive rather than proof.
func (mx *Mux) UnsubmittedInput(key string) (n int, known bool) {
	mx.mu.Lock()
	defer mx.mu.Unlock()
	for _, ch := range mx.children {
		if ch.key == key {
			return ch.unsubmitted, true
		}
	}
	return 0, false
}

// SessionStatus resolves one live child's key through StatusFn ("waiting" = the agent is done and
// it's your move, "active" = still working, "" = unknown). Exposed so a deliverer can avoid pasting
// into a session mid-turn.
func (mx *Mux) SessionStatus(key string) string {
	mx.mu.Lock()
	fn := mx.StatusFn
	mx.mu.Unlock()
	if fn == nil || key == "" {
		return ""
	}
	return fn(key)
}

// ---- ctrl-\ ←/→ inline tab selector (replaces the old modal picker) ----

// startBarSelect enters the inline selector: a candidate highlight on the bottom bar, seeded
// one step from the active tab in the arrow's direction. It then owns input (handleBarSelect)
// until ⏎ commits or esc cancels. arrow is the raw arrow key bytes (to read its direction).
func (mx *Mux) startBarSelect(arrow []byte) {
	mx.mu.Lock()
	if len(mx.children) == 0 {
		mx.mu.Unlock()
		return
	}
	mx.barActive = true
	mx.barSel = slotOfChild(mx.tabSlotsLocked(), mx.active) // barSel is a ribbon SLOT, not a child
	if mx.barSel < 0 {
		mx.barSel = 0
	}
	mx.mu.Unlock()
	if dir, _ := arrowAt(arrow); dir != 0 {
		mx.barMove(dir) // moves the candidate + repaints the bar
	} else {
		mx.drawBar()
	}
}

// barMove slides the candidate highlight along the bar (wrapping) and repaints ONLY the bar —
// cheap, no session snapshot. The expensive switch happens once, on commit.
func (mx *Mux) barMove(d int) {
	mx.mu.Lock()
	if n := len(mx.tabSlotsLocked()); n > 0 {
		// n ribbon slots (a split counts as ONE, so cycling steps over it rather than into a
		// half) + 1 virtual "launcher" slot at index n → wrap over n+1 items, so arrowing past
		// the last session lands on the launcher.
		mx.barSel = (mx.barSel + d + (n + 1)) % (n + 1)
	}
	mx.mu.Unlock()
	mx.drawBar()
}

// barCommit switches to the highlighted candidate (the one snapshot repaint) + exits.
func (mx *Mux) barCommit() {
	mx.mu.Lock()
	mx.barActive = false
	sel := mx.barSel
	n := len(mx.tabSlotsLocked())
	mx.mu.Unlock()
	if sel >= n { // the virtual launcher slot at the end of the bar
		mx.gotoHome()
		return
	}
	mx.gotoSlot(sel)
}

// barCancel exits the selector without switching; repaint so the highlight returns to active.
func (mx *Mux) barCancel() {
	mx.mu.Lock()
	mx.barActive = false
	mx.mu.Unlock()
	mx.drawBar()
}

// handleBarSelect owns input while the selector is up: ←/→ move, ⏎ commit, esc cancel, x close-picked,
// 1-9 jump+commit; any other key exits without switching.
func (mx *Mux) handleBarSelect(data []byte) {
	for i := 0; i < len(data); {
		if dir, n := arrowAt(data[i:]); n > 0 {
			if dir != 0 {
				mx.barMove(dir)
			}
			i += n
			continue
		}
		switch b := data[i]; {
		case b == '\r' || b == '\n':
			mx.barCommit()
		case b == 0x1b: // bare esc → cancel (arrow ESC[… is handled above)
			mx.barCancel()
		case b >= '1' && b <= '9':
			mx.mu.Lock()
			idx := int(b - '1')
			ok := idx < len(mx.tabSlotsLocked())
			if ok {
				mx.barSel = idx
			}
			mx.mu.Unlock()
			if ok {
				mx.barCommit()
			} else {
				mx.barCancel()
			}
		case b == 'x':
			// Close the HIGHLIGHTED tab without switching to it (#156): prune from the ribbon in
			// one motion. The selector STAYS ARMED so several tabs can be closed in a row — the
			// cursor holds its position and lands on the neighbor, exactly like pruning browser
			// tabs. ⏎ still switches, esc still cancels.
			mx.barClose()
		default: // any other key exits the selector (the key is dropped)
			mx.barCancel()
		}
		return
	}
}

// barCloseTarget is the selection rule, pure for tests: which CHILD index does closing ribbon
// slot `sel` end? -1 for the virtual launcher slot (not closable) or out of range. A split slot
// closes its LEFT pane — the pair then dissolves through the same watchExit path a pane's natural
// exit takes, and the surviving session becomes an ordinary tab (never both panes in one press).
func barCloseTarget(slots []tabSlot, sel int) int {
	if sel < 0 || sel >= len(slots) {
		return -1
	}
	return slots[sel].main
}

// barClose ends the highlighted slot's session. Everything downstream is the EXISTING child-exit
// path: watchExit removes it, fixes the active index, dissolves a pair, and repaints the bar.
// Every consumer of barSel already tolerates the slot count shrinking underneath it (barMove
// re-mods against the current count, barCommit treats out-of-range as the launcher, drawBar's
// highlight follows barSel) — which is what lets the selector stay open across closes.
func (mx *Mux) barClose() {
	mx.mu.Lock()
	idx := barCloseTarget(mx.tabSlotsLocked(), mx.barSel)
	var ch *child
	if idx >= 0 && idx < len(mx.children) {
		ch = mx.children[idx]
	}
	mx.mu.Unlock()
	if ch == nil || ch.sess == nil {
		return // the launcher slot, or nothing there — the key is a no-op, the selector stays up
	}
	// A busy tab confirms even from the selector — it is EASIER to hit x on the wrong tab here,
	// not harder. Confirming drops the selector (the prompt owns input); idle closes keep it up.
	if childBusy(ch) {
		mx.mu.Lock()
		mx.barActive = false
		mx.mu.Unlock()
	}
	mx.confirmOrEnd(ch)
}

// jump is ctrl-\ <digit>: i is a ribbon SLOT index (what the bar shows), so a split's digit
// lands on the split itself, never on one of its halves.
func (mx *Mux) jump(i int) { mx.gotoSlot(i) }

// ---- mode transitions ----

// gotoHome deactivates the focused child (it keeps running in the background) and shows
// the launcher.
func (mx *Mux) gotoHome() {
	if mx.home == nil {
		return
	}
	if mx.splitActive() {
		mx.parkSplit() // the launcher owns the whole screen; the pair stays bound and one tab
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
	//   ESC[?1049h      alt-screen: the launcher is ephemeral, so it must NOT land in native
	//                   scrollback (a live main-screen child now leaves us on the native screen)
	//   ESC[r           reset the scroll region to full screen
	//   ESC[?6l         origin mode off (menu uses absolute positioning)
	//   ESC[2J          clear; then hide cursor + autowrap off (the menu's contract)
	mx.outMu.Lock()
	os.Stdout.Write(modeDefaults())
	os.Stdout.WriteString("\x1b[?1049h\x1b[r\x1b[?6l\x1b[2J\x1b[?25l\x1b[?7l")
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

// maxReplayLines caps how much scrollback we replay into the terminal's native buffer on a
// switch — enough that native scroll feels like "all my history" without a multi-thousand-line
// burst that makes switching sluggish. The terminal's own scrollback is finite anyway.
const maxReplayLines = 1500

// wrapSync brackets a repaint in the terminal's synchronized-output mode (DECSET 2026) so the
// scrollback replay lands as one atomic update instead of a visible fast-scroll. Terminals that
// don't support it ignore the markers.
func wrapSync(b []byte) []byte {
	out := make([]byte, 0, len(b)+16)
	out = append(out, "\x1b[?2026h"...)
	out = append(out, b...)
	out = append(out, "\x1b[?2026l"...)
	return out
}

// paintBody builds the bytes that repaint child ch's live screen. The caller owns outMu. For a
// MAIN-screen program (claude, a shell) it drops to the native screen, ERASES the terminal's
// saved scrollback (\x1b[3J — kills the previous session's history bleeding into the buffer),
// then replays THIS session's scrollback + screen so native mouse-scroll and drag-copy show
// only this session's history, per session. The screen block is padded to the FULL terminal
// height (mx.rows) so it always lands at row 1 with the bar row free — see SnapshotHistory
// (#238: without the full-height pad, scrollback floats the screen down a row). For an
// alt-screen program (vim/htop) it paints the normal snapshot — alt-screen has no scrollback.
func (mx *Mux) paintBody(ch *child) []byte {
	var out []byte
	if ch.sess.IsAltScreen() {
		out = append(out, ch.sess.Snapshot()...) // enters alt-screen; no native scrollback to manage
	} else {
		out = append(out, []byte("\x1b[?1049l\x1b[3J\x1b[2J\x1b[H")...)        // main screen, wipe saved+visible, home
		out = append(out, ch.sess.SnapshotHistory(maxReplayLines, mx.rows)...) // this session's scrollback + screen
	}
	// Mouse mode follows the child: restore() re-emits ONLY what this child asked for.
	// claude turns mouse on (scrolls itself); codex/gemini/shells don't → native copy/scroll.
	//
	// NOTHING ELSE GOES IN HERE. A scroll region asserted after the snapshot homes the cursor
	// (DECSTBM does), and one asserted before it makes the full-height snapshot scroll inside a
	// shorter region — the two orderings that produced #238 in each direction. The snapshot is
	// already padded to the full terminal height by SnapshotHistory so it lands at row 1 on its
	// own, and the child's own next draw puts the cursor where it belongs.
	out = append(out, ch.gate.modes.restore()...)
	return out
}

// switchTo repaints child i: deactivate old gate, reset mode bleed, write the child's
// snapshot, re-assert its modes, draw the bar, then activate its gate (live output flows).
func (mx *Mux) switchTo(i int) {
	// A switch that lands on a PAIRED session re-enters its split rather than painting the session
	// full width — the binding outlives navigation, so that is what "returning to it" means. Doing
	// it HERE, at the one funnel every caller goes through (esc out of the manager, ⏎ on a live
	// session, a resize repaint, an exiting neighbour, gotoSlot), is what keeps a bound pair from
	// ever falling apart into two tabs.
	mx.mu.Lock()
	pr := mx.reentryTargetLocked(i)
	cols, rows := mx.cols, mx.rows
	mx.mu.Unlock()
	if pr != nil {
		if _, _, _, ok := splitGeom(cols, rows); ok {
			mx.enterPair(pr)
			return
		}
	}
	// Otherwise a tab switch PARKS any split — it never unbinds a filled pair, which keeps running
	// at pane width while you are elsewhere. The full-width repaint below then restores the
	// single-pane screen, incl. the #238 row-1 snapshot rules, with no split-specific code.
	if mx.splitActive() {
		mx.parkSplit()
	}
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
	mx.scrolling, mx.scrollOff = false, 0 // a switch always lands on the live screen
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
	// A session whose screen is still blank is booting — show the animated splash and HOLD its
	// output (gate paused) so its setup bytes can't wipe the splash; the loading ticker reveals
	// it once the screen renders content. An already-rendered session paints normally.
	loading := mx.LoadingFrame != nil && !snapshotHasContent(ch.sess)
	if loading {
		out = append(out, mx.LoadingFrame(mx.loadPhase, mx.cols, mx.rows)...)
	} else {
		out = append(out, mx.paintBody(ch)...)
	}
	os.Stdout.Write(wrapSync(out))
	ch.gate.active = true
	ch.gate.paused = loading // hold live output behind the splash; reveal() unpauses
	mx.outMu.Unlock()

	mx.mu.Lock()
	ch.loading = loading
	if loading {
		ch.loadStart = time.Now()
	}
	mx.mu.Unlock()

	mx.drawBar()
}

// reveal ends a session's loading splash and paints its now-rendered screen (the content path of
// switchTo), unpausing live output. Called by the loading ticker when the screen fills in.
func (mx *Mux) reveal(ch *child) {
	mx.outMu.Lock()
	var out []byte
	out = append(out, modeDefaults()...)
	out = append(out, []byte("\x1b[r\x1b[?6l")...)
	out = append(out, mx.paintBody(ch)...)
	os.Stdout.Write(wrapSync(out))
	ch.gate.paused = false
	mx.outMu.Unlock()
	mx.mu.Lock()
	ch.loading = false
	mx.mu.Unlock()
	mx.drawBar()
}

// snapshotHasContent reports whether a session's emulated screen has any visible glyph (i.e. it
// has rendered something, not just entered alt-screen / set colours). Skips ANSI escapes and
// whitespace; any printable byte (incl. UTF-8 glyphs) counts.
func snapshotHasContent(sess *ptysess.Session) bool {
	b := sess.Snapshot()
	for i := 0; i < len(b); {
		if b[i] == 0x1b { // skip an escape sequence
			i++
			switch {
			case i < len(b) && b[i] == '[': // CSI: ESC [ … final(0x40–0x7e)
				i++
				for i < len(b) && (b[i] < 0x40 || b[i] > 0x7e) {
					i++
				}
				if i < len(b) {
					i++
				}
			case i < len(b) && b[i] == ']': // OSC: ESC ] … BEL
				for i < len(b) && b[i] != 0x07 {
					i++
				}
				if i < len(b) {
					i++
				}
			default:
				i++
			}
			continue
		}
		if b[i] > ' ' && b[i] != 0x7f {
			return true
		}
		i++
	}
	return false
}

// loadingLoop repaints the animated loading splash (~8fps) while the active session is still
// booting (hasn't emitted output yet). It shares outMu with child output, so it can't interleave;
// the gate wipes the splash on the first real byte, and the next tick sees produced==true and
// goes idle. Stops when Run returns (via the stop channel).
func (mx *Mux) loadingLoop(stop chan struct{}) {
	tick := time.NewTicker(120 * time.Millisecond)
	defer tick.Stop()
	const maxLoad = 12 * time.Second // safety: reveal even if a session never renders visible content
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
			mx.mu.Lock()
			live := mx.mode == modeLive && !mx.confirming && !mx.barActive && !mx.scrolling
			var ch *child
			if live && mx.active >= 0 && mx.active < len(mx.children) {
				ch = mx.children[mx.active]
			}
			loading := ch != nil && ch.loading
			var since time.Duration
			if loading {
				since = time.Since(ch.loadStart)
			}
			cols, rows := mx.cols, mx.rows
			mx.mu.Unlock()
			if !loading {
				continue
			}
			if since > maxLoad || snapshotHasContent(ch.sess) {
				mx.reveal(ch) // the session rendered — swap the splash for its screen
				continue
			}
			mx.outMu.Lock()
			mx.loadPhase++
			os.Stdout.Write(mx.LoadingFrame(mx.loadPhase, cols, rows))
			mx.outMu.Unlock()
		}
	}
}

// ---- per-child scrollback view ----
//
// Each child's vt emulator already captures lines that scroll off the top (a 10k-line ring,
// isolated per session). Scroll mode freezes live painting and renders a window of that
// child's OWN [scrollback ++ screen] — so scrolling back never shows another session's
// output (the old bug was leaning on the host terminal's single shared scrollback).

const wheelStep = 3 // lines per wheel notch

// curLocked returns the active child, or nil. Caller holds mx.mu.
func (mx *Mux) curLocked() *child {
	if mx.active >= 0 && mx.active < len(mx.children) {
		return mx.children[mx.active]
	}
	return nil
}

// scrollBy enters scroll mode if needed and moves the view by d lines (positive = older).
// Reaching the live bottom (off ≤ 0) exits back to live. d may be huge (Home → top); the
// viewport render clamps the stored offset to the scrollback length.
func (mx *Mux) scrollBy(d int) {
	mx.mu.Lock()
	ch := mx.curLocked()
	if ch == nil {
		mx.mu.Unlock()
		return
	}
	if !mx.scrolling {
		mx.scrolling = true
		mx.scrollOff = 0
	}
	mx.scrollOff += d
	if mx.scrollOff < 0 {
		mx.scrollOff = 0
	}
	off := mx.scrollOff
	mx.mu.Unlock()
	if off <= 0 {
		mx.exitScroll()
		return
	}
	ch.gate.setPaused(true) // freeze live paint; the viewport owns the screen now
	mx.renderViewport()
}

// exitScroll leaves scroll mode and repaints the live screen (the viewport is discarded).
func (mx *Mux) exitScroll() {
	mx.mu.Lock()
	if !mx.scrolling {
		mx.mu.Unlock()
		return
	}
	mx.scrolling = false
	mx.scrollOff = 0
	ch := mx.curLocked()
	mx.mu.Unlock()
	mx.repaintLive() // paint the authoritative live screen while the gate is still paused (no interleave)
	if ch != nil {
		ch.gate.setPaused(false) // resume live output
	}
}

// renderViewport paints the scrollback window for the active child at the current offset,
// plus a status line. Held together under outMu so a child write can't interleave.
func (mx *Mux) renderViewport() {
	mx.mu.Lock()
	ch := mx.curLocked()
	off := mx.scrollOff
	cols, rows := mx.cols, mx.rows
	mx.mu.Unlock()
	if ch == nil || cols <= 0 || rows <= 0 {
		return
	}
	h := rows - 1 // body height; the bottom row is the status line
	if h < 1 {
		h = 1
	}
	lines, sbLen := ch.sess.ScrollViewport(off, h)
	if off > sbLen { // clamp the stored offset to what's actually available (Home / over-scroll)
		mx.mu.Lock()
		if mx.scrollOff > sbLen {
			mx.scrollOff = sbLen
		}
		off = mx.scrollOff
		mx.mu.Unlock()
	}
	var b []byte
	b = append(b, "\x1b[?25l\x1b[H"...) // hide cursor, home
	for _, ln := range lines {
		b = append(b, ln...)
		b = append(b, "\x1b[0m\x1b[K\r\n"...) // reset SGR (no colour bleed) + clear to EOL
	}
	// One row, same as drawBar: the viewport body is rows-1 tall (see h above), so this status
	// line must never wrap. The keys are the ones scrollKeyAt actually decodes.
	label := brand.HintBar("SCROLLBACK", []brand.Hint{
		{Key: fmt.Sprintf("%d/%d", off, sbLen), Label: "lines up"},
		{Key: "↑↓ PgUp/PgDn", Label: "scroll"},
		{Key: "Home", Label: "top"},
		{Key: "q · esc · ⏎", Label: "back to live"},
	}, cols)
	b = append(b, fmt.Sprintf("\x1b[%d;1H\x1b[2K%s", rows, brand.PadTo(label, cols))...)
	mx.outMu.Lock()
	os.Stdout.Write(b)
	mx.outMu.Unlock()
}

// repaintLive restores the active child's live screen (used on scroll exit). Mirrors
// switchTo's repaint, minus the gate toggling — the gate stays active (just paused until
// the caller resumes it). Held under outMu so no child write interleaves the repaint.
func (mx *Mux) repaintLive() {
	mx.mu.Lock()
	split := mx.split != nil
	ch := mx.curLocked()
	mx.mu.Unlock()
	if split { // the split owns every row; repaint the panes instead of one full-width child
		mx.paintSplit()
		mx.drawBar()
		return
	}
	if ch == nil {
		mx.gotoHome()
		return
	}
	var out []byte
	out = append(out, modeDefaults()...)
	out = append(out, []byte("\x1b[r\x1b[?6l")...) // reset scroll region + origin mode
	out = append(out, mx.paintBody(ch)...)         // replays this session's scrollback (main-screen)
	mx.outMu.Lock()
	os.Stdout.Write(wrapSync(out))
	mx.outMu.Unlock()
	mx.drawBar()
}

// handleScroll consumes a chunk of input while in scroll mode: wheel + arrows/PgUp/PgDn/
// Home scroll; q/esc/Enter (or any other key) returns to live. Stops early once a move
// drops back to the live bottom.
func (mx *Mux) handleScroll(data []byte) {
	mx.mu.Lock()
	h := mx.rows - 1
	mx.mu.Unlock()
	if h < 2 {
		h = 2
	}
	for i := 0; i < len(data); {
		// Scroll mode is keyboard-only (arrows / PgUp/PgDn / Home / j/k/g / space; q/esc/⏎
		// exits). The wheel isn't ours anymore — it belongs to the child or the terminal.
		act, kn := scrollKeyAt(data[i:])
		if kn <= 0 {
			kn = 1
		}
		switch act {
		case scrUp:
			mx.scrollBy(1)
		case scrDown:
			mx.scrollBy(-1)
		case scrPgUp:
			mx.scrollBy(h - 1)
		case scrPgDn:
			mx.scrollBy(-(h - 1))
		case scrTop:
			mx.scrollBy(1 << 30)
		case scrExit:
			mx.exitScroll()
			return
		}
		i += kn
		mx.mu.Lock()
		still := mx.scrolling
		mx.mu.Unlock()
		if !still { // a move hit the live bottom and exited
			return
		}
	}
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

// drainInput discards any bytes already queued on the terminal fd (e.g. a burst of mouse
// SGR reports) so they don't leak into a cooked prompt. Called while still in raw mode, on
// the input goroutine (no concurrent reader), so the brief non-blocking toggle is safe.
func (mx *Mux) drainInput() {
	_ = unix.SetNonblock(mx.fd, true)
	buf := make([]byte, 4096)
	for {
		n, err := unix.Read(mx.fd, buf)
		if n <= 0 || err != nil {
			break
		}
	}
	_ = unix.SetNonblock(mx.fd, false)
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

// suspend hands the real terminal to fn (e.g. a pager or the Common Ground menu): leave
// alt-screen + cooked mode, run fn, then re-enter alt-screen + raw and repaint.
//
// Mouse discipline matters here: the focused child may have mouse reporting ON (claude
// does). If we drop to a cooked line-prompt with it still enabled, every mouse move floods
// stdin with SGR reports (^[[<35;…M) that bury the prompt. So we disable mouse reporting +
// bracketed paste FIRST, while still raw, and drain whatever already queued — then hand off.
// On re-entry, modes.restore() turns the child's modes (incl. mouse) back on.
func (mx *Mux) suspend(fn func()) {
	os.Stdout.WriteString("\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l\x1b[?2004l")
	mx.drainInput() // discard mouse/paste bytes already queued so they don't leak into fn's prompt
	os.Stdout.WriteString("\x1b[?7h\x1b[?25h\x1b[?1049l")
	if mx.old != nil {
		_ = term.Restore(mx.fd, mx.old)
	}
	fn()
	_, _ = term.MakeRaw(mx.fd) // mx.old already holds the original cooked state
	// Clear the main-screen buffer the cooked menu drew on BEFORE re-entering the alt-screen.
	// Otherwise the menu's box lingers in the main buffer and reappears as a ghost the moment
	// we finally leave the alt-screen on quit (mx.restore) — an unclean-looking exit.
	os.Stdout.WriteString("\x1b[2J\x1b[H\x1b[?1049h\x1b[?25l\x1b[?7l")
	// Return to wherever the user was: back into the live session (ctrl-\ c came from a child),
	// or the launcher (a home-screen Suspend action, e.g. a pager).
	mx.mu.Lock()
	live := mx.mode == modeLive
	mx.mu.Unlock()
	if live {
		mx.repaintLive()
	} else {
		mx.renderHome()
	}
}

// childBusy mirrors the ribbon's "working" signal: PTY output within the last ~1.5s. That is
// the exact set of sessions where an accidental close destroys in-flight agent work — and the
// exact set the ribbon already marks, so the confirm fires precisely when the UI says busy.
func childBusy(c *child) bool {
	return c != nil && c.gate != nil && time.Since(time.Unix(0, c.gate.lastOut.Load())) < 1500*time.Millisecond
}

// confirmOrEnd closes ch — immediately when idle, behind a y/n prompt when BUSY. The asymmetry
// is the whole design: q (quit) always confirms because it takes everything; x takes one tab, so
// it only confirms when that tab is actively producing work. Mirrors requestQuit's machinery
// (confirming + gate mute + renderConfirm), routed by closeTarget.
func (mx *Mux) confirmOrEnd(ch *child) {
	if ch == nil || ch.sess == nil {
		return
	}
	if !childBusy(ch) {
		ch.sess.End()
		return
	}
	mx.mu.Lock()
	mx.confirming = true
	mx.closeTarget = ch
	if mx.mode == modeLive && mx.active >= 0 && mx.active < len(mx.children) {
		mx.children[mx.active].gate.setActive(false) // mute so output doesn't paint over the prompt
	}
	mx.mu.Unlock()
	mx.renderCloseConfirm(ch.label)
}

// renderCloseConfirm is the one-tab sibling of renderConfirm (quit's box) — same drawing, same
// key contract (y/⏎ acts, anything else keeps working), scoped to the one busy session.
func (mx *Mux) renderCloseConfirm(label string) {
	mx.drawCenteredBox([]string{
		"\x1b[1mClose busy session?\x1b[0m",
		"",
		label + " produced output moments ago — it looks mid-task.",
		"",
		brand.HintBar("CLOSE", []brand.Hint{
			{Key: "y · ⏎", Label: "close it"}, {Key: "esc · any other key", Label: "keep it"}}, 0),
	})
}

func (mx *Mux) closeActive() {
	mx.mu.Lock()
	if mx.active < 0 || mx.active >= len(mx.children) {
		mx.mu.Unlock()
		return
	}
	ch := mx.children[mx.active]
	mx.mu.Unlock()
	mx.confirmOrEnd(ch) // idle → gone; BUSY → y/n first (E9 slice 2)
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
	mx.mu.Lock()
	target := mx.closeTarget
	mx.mu.Unlock()
	switch kb[0] {
	case 'y', 'Y', '\r', '\n':
		if target != nil { // a single-tab close, not quit
			mx.mu.Lock()
			mx.confirming = false
			mx.closeTarget = nil
			mx.mu.Unlock()
			target.sess.End() // watchExit removes + refocuses + repaints
			return false
		}
		mx.fireBeforeQuit() // snapshot the open sessions for --resume before teardown
		return true
	}
	mx.mu.Lock()
	mx.confirming = false
	mx.closeTarget = nil
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
	// Mirrors handleConfirm exactly: y / Y / ⏎ quit, ANY other key keeps working. The old
	// "[y]/[n]" implied n was special and left the escape hatch unnamed.
	lines = append(lines, "", brand.HintBar("QUIT", []brand.Hint{
		{Key: "y · ⏎", Label: "quit"}, {Key: "esc · any other key", Label: "keep working"}}, 0))
	mx.drawCenteredBox(lines)
}

// drawCenteredBox clears the screen and draws a centered rounded box of pre-styled lines.
func (mx *Mux) drawCenteredBox(lines []string) {
	mx.mu.Lock()
	cols, rows := mx.cols, mx.rows
	mx.mu.Unlock()
	w := 0
	for _, l := range lines {
		if v := brand.VisWidth(l); v > w {
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
		pad := w - brand.VisWidth(l)
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

// earlyExitWindow is how soon after spawn an exit counts as "failed to start" rather than "the
// session ended". Generous: a resume that bounces off a lock, a missing binary, or a bad flag all
// return well inside this, while a real session a human worked in never does.
const earlyExitWindow = 5 * time.Second

// earlyExitScrapeLines bounds the scrollback we read for the reason. A launch failure says its piece
// in a line or two; anything longer is a session that actually ran.
const earlyExitScrapeLines = 40

// earlyExitReason pulls the human-meaningful last words out of a child's final screen.
//
// Pure and exported-for-test on purpose: this is the whole value of the fix, and it can be checked
// against real captured output without spawning a PTY. The rendering above is two lines precisely
// so the logic worth testing lives here instead.
func earlyExitReason(screen []byte) string {
	// Strip CSI/OSC so a message wrapped in colour doesn't read as gibberish in the bar.
	clean := ansiSeq.ReplaceAllString(string(screen), "")
	lines := strings.Split(clean, "\n")
	// Search backwards: the last thing printed is the reason it stopped. Earlier lines are banners,
	// version strings and boot noise that would bury it.
	for i := len(lines) - 1; i >= 0; i-- {
		t := strings.TrimSpace(lines[i])
		if t == "" || len(t) < 8 {
			continue // blank, or a prompt fragment / stray glyph rather than a sentence
		}
		if strings.HasPrefix(t, "$") || strings.HasPrefix(t, ">") {
			continue // the shell prompt, not the program's message
		}
		if len(t) > 180 {
			t = t[:177] + "…"
		}
		return t
	}
	return ""
}

// ansiSeq matches CSI and OSC escapes — enough to make a terminal message readable as plain text.
// CSI is ESC [ <private> <params> <intermediates> <final>. The private markers < = > ? are the part
// that is easy to forget: a terminal writes ESC[>4;2m and ESC[<u routinely (kitty keyboard
// protocol), and a class of only [0-9;?] leaves those in the string — which then renders as
// gibberish in a one-line status bar. Found by testing against real captured output.
var ansiSeq = regexp.MustCompile(`\x1b\[[0-9;?<>=]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[()][A-Z0-9]|\x1b[=>]`)

func (mx *Mux) watchExit(ch *child) {
	<-ch.sess.Done
	// A child that dies almost immediately did not "finish" — it failed to start, and whatever it
	// printed on the way out is the only explanation anyone will ever get. Removing the tab silently
	// throws that away: `ptln llms resume` on a session Claude considers live prints
	// "…is currently running as a background agent" and exits, and all the human sees is a tab
	// blinking shut. Cost of that: an hour of believing the work was destroyed.
	//
	// Deliberately surfaced through the EXISTING banner rather than a new dead-tab render. This file
	// has a history of paint bugs found by bisect (#238, twice mis-fixed); a one-line banner touches
	// none of the scroll/snapshot machinery.
	if time.Since(ch.startedAt) < earlyExitWindow {
		if why := earlyExitReason(ch.sess.SnapshotHistory(earlyExitScrapeLines, 0)); why != "" {
			mx.SetBanner("✗ " + ch.label + ": " + why)
		}
	}
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
	mx.clearQueryOwner(ch) // a dead child must never remain the query owner
	n := len(mx.children)
	wasActive := idx == mx.active
	// Keep `active` pointing at the same child after the slice shifts: removing one BEFORE the
	// focused child shifts it down by one (this happens during a reattach, which spawns the new
	// child then ends the old one that sits earlier in the list).
	if idx < mx.active {
		mx.active--
	}
	if mx.active >= n {
		mx.active = n - 1
	}
	next := mx.active
	md := mx.mode
	mx.mu.Unlock()

	if n == 0 {
		mx.mu.Lock()
		mx.pairs = nil // nothing left to bind; don't keep dead children alive through a binding
		mx.mu.Unlock()
		if mx.home != nil {
			mx.gotoHome()
		} else {
			_ = os.Stdin.Close() // no launcher → unblock Run's read for a clean exit
		}
		return
	}
	// A PANE's program exited — a pair can't reference a dead session, so this (with ctrl-\ x)
	// is the other path that unbinds. The survivor becomes an ordinary tab and we fall through to
	// a normal full-width repaint of whatever still has focus.
	if mx.dropPaired(ch) && md != modeHome {
		mx.switchTo(next)
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

// clearResizeOverlays drops every transient overlay a resize invalidates and un-pauses the
// children whose gate those overlays had frozen. It is pure state + gate flags — the caller
// does the repaint — so the invariant is testable without a terminal.
//
// The repaint that follows ERASES all three overlays (scrollback view, quit prompt, ctrl-\
// command panel), so their input-routing state has to go with them. Leaving mx.confirming set
// was TRAP-4: no prompt on screen, the child un-muted by switchTo, and Run still routing keys
// to handleConfirm — so the next y/⏎ quit partyline and closed every session. An orphaned
// pfxCh is the same shape, plus it leaves its child paused forever.
//
// Returns the children it un-paused (for tests).
func (mx *Mux) clearResizeOverlays() []*child {
	mx.mu.Lock()
	var resumed []*child
	if mx.scrolling {
		mx.scrolling, mx.scrollOff = false, 0
		if ch := mx.curLocked(); ch != nil {
			resumed = append(resumed, ch)
		}
	}
	// The quit prompt is a cancel, never a confirm: the repaint un-mutes the focused child
	// (switchTo) / re-paints the split, which is exactly handleConfirm's "n" outcome.
	mx.confirming = false
	if ch := mx.pfxCh; ch != nil {
		mx.pfxCh = nil
		resumed = append(resumed, ch)
	}
	mx.sawPfx = false // the panel is gone, so the next key is a key again, not a mux command
	mx.mu.Unlock()
	for _, ch := range resumed {
		if ch.gate != nil {
			ch.gate.setPaused(false)
		}
	}
	return resumed
}

// onResize re-reads the terminal size and repaints. #238: the repaint funnels through
// switchTo / paintSplit / renderHome — do not reorder or add rows here; the child screen is
// rows-1 tall, snapshot replay must land at row 1, and the focused child's cursor is
// re-asserted LAST (the bar is framed in DECSC/DECRC, see drawBar).
func (mx *Mux) onResize() {
	if c, r, e := term.GetSize(int(os.Stdout.Fd())); e == nil && c > 0 && r > 0 {
		mx.mu.Lock()
		mx.cols, mx.rows = c, r
		md := mx.mode
		active := mx.active
		mx.mu.Unlock()
		mx.clearResizeOverlays()
		mx.resizeAll()
		switch {
		case md == modeHome:
			mx.renderHome()
		case mx.splitActive(): // keep the split across a resize; repaint at the new geometry
			mx.paintSplit()
			mx.drawBar()
		default:
			mx.switchTo(active)
		}
	}
}

func (mx *Mux) resizeAll() {
	cols, rows := mx.bodySize()
	mx.mu.Lock()
	kids := append([]*child(nil), mx.children...)
	pairs := append([]*pairSlot(nil), mx.pairs...)
	full, fullRows := mx.cols, mx.rows
	mx.mu.Unlock()
	for _, ch := range kids {
		c, r := cols, rows
		// A PAIRED child is told its OWN half-width geometry — a child that thinks it's full
		// width while painted into half a screen produces garbage. This holds while the pair is
		// parked too, so its emulator stays pane-width and a re-entry replays it exactly.
		if pc, pr, ok := paneSizeOf(pairs, ch, full, fullRows); ok {
			c, r = pc, pr
		}
		ch.sess.Resize(ch.part, c, r)
	}
}

// Bottom-bar chrome. Tabs are two-tone background segments — brand pink (brand.PillBg, the
// same colour as every mode pill) for the focused one, dim grey otherwise. Powerline arrows are
// opt-in (PTLN_POWERLINE=1) because the glyph is missing from most default fonts; the fallback
// separator is a plain space.
const barIdleBg = "\x1b[48;5;236m"

// barPhone is the bar's leading ☎ — brand.Phone plus its trailing space.
func barPhone() string { return brand.Phone() + " " }

// visCols counts the visible DISPLAY COLUMNS of s — the same metric the terminal actually uses.
//
// This was `visBytes`, counting non-escape BYTES, with a comment insisting that was deliberate:
// measuring in columns while clipping in bytes would make the two disagree, so both counted bytes.
// The consistency was real and the choice was still wrong — the TERMINAL renders columns, and every
// multi-byte glyph in the ribbon (● ☎ ◉ ⌂) costs 3 bytes to draw 1 column.
//
// Measured on a real 9-session bar: 4 phantom columns per tab, ~38 across the row. The bar believed
// it was 38 columns wider than it rendered, so the clipper cut 38 columns of real content and the
// launcher — the last segment — fell off the end. That is the "10th tab is clipped" report.
//
// brand.VisWidth is the app-wide column metric and handles wide runes and combining marks; there is
// no reason for the status bar to have its own.
func visCols(s string) int { return brand.VisWidth(s) }

func barSep() string {
	if os.Getenv("PTLN_POWERLINE") == "1" {
		return "\x1b[38;5;236m\x1b[0m"
	}
	return " "
}

// drawBar paints the reserved bottom row: session tabs with the active one highlighted.
// Saves/restores the cursor so the focused child's cursor position is untouched.
// ctxMarkColor decides the ☎ marker's colour. Pure and separate from drawBar because this is the
// only part of the status row worth asserting, and a PTY cannot be stood up in a test — the same
// discipline that finally fixed the tab-switch garble (a headless round-trip over the thing that was
// actually wrong, instead of eyeballing a terminal).
//
// Precedence, strongest first:
//
//	tool call   the agent is talking to PARTYLINE right now. Rarest and most informative, and it
//	            lasts seconds, so it cannot mask the states below for long. Index 51 is the source
//	            palette's "marked", so every theme remaps it (llms_theme.go) rather than leaving it
//	            cyan on a light background.
//	active      the agent is working (green) — passed in as stateClr, already resolved for the dot.
//	waiting     it finished its turn and it is your move (amber), likewise.
//	at rest     cyan when the agent is wired to the thread, dim when the session is record-only.
func ctxMarkColor(ctx int, state string, stateClr int, toolLive bool) int {
	if toolLive {
		return 51
	}
	if state == "active" || state == "waiting" {
		return stateClr
	}
	if ctx == 2 {
		return 245 // record-only (agent not wired) — dim at rest
	}
	return 39 // cyan — attached + wired, idle
}

func (mx *Mux) drawBar() {
	// Snapshot tabs under the lock, then resolve status OUTSIDE it — StatusFn tail-reads
	// session files (I/O), so we must not hold mx.mu across it.
	type tabInfo struct {
		label, key string
		active     bool
		ctx        int  // 0 none · 1 attached+wired · 2 attached record-only
		shared     bool // being broadcast live over the relay
		busy       bool // produced PTY output very recently → the agent is working
	}
	mx.mu.Lock()
	rows, cols := mx.rows, mx.cols
	// The ribbon addresses SLOTS, not children: a live split is one slot holding both its panes,
	// so it shows (and is jumped to) as a single tab. With no split, slots == children 1:1.
	slots := mx.tabSlotsLocked()
	// While the selector is up, the highlight tracks the CANDIDATE (barSel), not the
	// active tab — that's the moving cursor you commit with ⏎.
	selecting := mx.barActive
	hl := slotOfChild(slots, mx.active)
	if selecting {
		hl = mx.barSel
	}
	busyOf := func(c *child) bool {
		// "working" = PTY output within the last ~1.5s. Lock-free read of the atomic; this is
		// resolved at draw time (on switch/interaction), matching the bar's event-driven repaint.
		return c.gate != nil && time.Since(time.Unix(0, c.gate.lastOut.Load())) < 1500*time.Millisecond
	}
	infos := make([]tabInfo, len(slots))
	for k, sl := range slots {
		c := mx.children[sl.main]
		ctx := 0
		if c.thread != "" {
			ctx = 2 // attached, record-only
			if c.threadWired {
				ctx = 1 // attached + agent wired
			}
		}
		busy := busyOf(c)
		if sl.split() { // a split is busy when EITHER of its panes is
			busy = busy || busyOf(mx.children[sl.pair])
		}
		infos[k] = tabInfo{label: mx.slotLabelLocked(sl, cols), key: c.key, active: k == hl,
			ctx: ctx, shared: c.shared, busy: busy}
	}
	// Captured under mu, used under outMu: whether the focused child is mid-frame decides if this
	// paint may go out now or must wait for its frame to close.
	var curG *gate
	if c := mx.curLocked(); c != nil {
		curG = c.gate
	}
	fn := mx.StatusFn
	actFn := mx.ToolActivityFn
	banner := mx.banner
	pfx := mx.sawPfx // ctrl-\ pressed, awaiting the command key — show the menu now, not blind
	// A split with an unfilled pane is mid-setup: the bar carries the guided hint instead.
	setup := mx.split != nil && !mx.split.filled()
	splitLive := mx.split != nil && mx.split.filled()
	mx.mu.Unlock()

	var tabs []string
	for k, ti := range infos {
		// One activity state per session, from two signals: recent PTY output (works for ANY
		// engine incl. freshly-launched ones) says "working"; the store-backed StatusFn refines
		// it to waiting-vs-active for sessions we can read. Resolved at draw time (the bar is
		// event-driven — repainted on switch/interaction, never a background timer).
		state := "idle"
		if ti.busy {
			state = "active"
		}
		if fn != nil {
			if s := fn(ti.key); s == "active" || s == "waiting" {
				state = s // store knowledge (waiting = your move) overrides raw output activity
			}
		}
		clr := 245 // idle / unknown (grey)
		switch state {
		case "active":
			clr = 46 // green — the agent is working
		case "waiting":
			clr = 214 // amber — finished its turn, your move
		}
		segBg, segFg := barIdleBg, "\x1b[38;5;250m"
		if ti.active {
			segBg, segFg = brand.PillBg(), "\x1b[1;38;5;16m" // brand pink, dark bold text
		}
		// The ☎ context marker doubles as the activity light: it takes the SAME state colour
		// when the session is active/waiting, so the telephone you're watching turns green while
		// the agent works and amber when it's your move — falling back to cyan (wired, idle) or
		// dim (record-only, not wired) at rest.
		ctxMark := ""
		if ti.ctx != 0 {
			toolLive := false
			if actFn != nil {
				_, toolLive = actFn(ti.key)
			}
			ctxMark = fmt.Sprintf("\x1b[38;5;%dm☎%s", ctxMarkColor(ti.ctx, state, clr, toolLive), segFg)
		}
		// Live-share marker (magenta ◉): this session is being broadcast over the relay.
		if ti.shared {
			ctxMark += "\x1b[38;5;201m◉" + segFg
		}
		tabs = append(tabs, fmt.Sprintf("%s\x1b[38;5;%dm●%s%s %d %s \x1b[0m", segBg, clr, segFg, ctxMark, k+1, ti.label))
	}
	// The launcher is a virtual last item in the switch bar: arrow past the last session to
	// reach it, ⏎ opens it. Always shown (dimmed) so it's discoverable; highlighted when it's
	// the current candidate during selection.
	lseg := barIdleBg + "\x1b[38;5;250m ⌂ launcher \x1b[0m"
	if selecting && hl == len(infos) {
		lseg = brand.PillBg() + "\x1b[1;38;5;16m ⌂ launcher \x1b[0m" // the candidate
	}
	// RESERVE THE LAUNCHER. It is the last segment, so a bar that overflows loses it first — and
	// losing it is not cosmetic: arrowing past the last session is how you REACH the launcher, so an
	// invisible one is an unreachable one. Trim the SESSION tabs to make room instead, which costs a
	// few characters of a label rather than a whole door.
	//
	// Column-accurate now that visCols measures what the terminal draws; with the old byte metric
	// this budget would have been ~38 columns too pessimistic on a 9-session row.
	bar := fitRibbon(barPhone(), tabs, lseg, barSep(), cols)
	// The four hand-swapped hint strings are now one derived hint bar (see barHint). It replaces
	// the per-mode colour with a mode PILL, which is the same badge every other surface wears.
	hint := barHint(selecting, pfx, setup, splitLive, cols-visCols(bar)-2)
	// Right-align the hint when the row has room; otherwise it trails with a two-space gap
	// and clipANSI trims from the right as before. Budget in clipANSI's own metric (visible
	// BYTES), not columns, so the multi-byte tab glyphs can't push the hint past its cut.
	gap := cols - visCols(bar) - visCols(hint)
	if gap < 2 {
		gap = 2
	}
	bar += strings.Repeat(" ", gap) + hint
	if banner != "" {
		// A checkup notice owns the status row while shown (cleared on the next keystroke).
		// Informational only — never injected into the agent.
		bar = "\x1b[1;38;5;214m" + banner + "\x1b[0m"
	}
	bar = clipANSI(bar, cols)
	// Save cursor (DECSC), jump to the reserved bottom row, paint, restore (DECRC) — the whole
	// thing inside our OWN synchronized-update bracket so the terminal presents it as one frame.
	//
	// The previous version of this comment claimed the bar "can't land mid-render" because it is
	// only drawn on switch/home/resize. That was wrong twice over: drawBar is also called
	// asynchronously from the input loop on Wake(), and even a switch-time paint races a child
	// that is repainting concurrently. What makes the difference is not WHEN we paint but whether
	// the child is mid-frame — see modeState.inFrame.
	seq := []byte(fmt.Sprintf("\x1b[?2026h\x1b7\x1b[%d;1H\x1b[2K%s\x1b8\x1b[?2026l", rows, mx.skin(bar)))

	mx.outMu.Lock()
	mx.barBytes = seq
	if curG != nil && curG.modes.midFrame() {
		mx.barPending = true // gate.Write paints it the moment the child closes its frame
	} else {
		mx.barPending = false
		os.Stdout.Write(seq)
	}
	mx.outMu.Unlock()
}

// flushBarLocked paints a deferred status bar once the child's synchronized-update bracket has
// closed. Caller holds outMu — which gate.Write already does, so the bar lands strictly BETWEEN
// two child frames and can never be composed into one.
func (mx *Mux) flushBarLocked(m *modeState) {
	if !mx.barPending || m.midFrame() {
		return
	}
	mx.barPending = false
	os.Stdout.Write(mx.barBytes)
}

// armPanel is invoked the instant ctrl-\ is pressed: it pauses the active child (so its live
// output can't paint over the panel — mirrors scroll mode's freeze), records that child so we
// can resume exactly it on dismiss, then paints the command panel + tab bar.
func (mx *Mux) armPanel() {
	mx.mu.Lock()
	ch := mx.curLocked()
	mx.pfxCh = ch
	mx.mu.Unlock()
	if ch != nil && ch.gate != nil {
		ch.gate.setPaused(true)
	}
	mx.drawCmdPanel()
	mx.drawBar()
}

// disarmPanel tears the command panel down. If repaint, it repaints the live screen (which
// erases the panel rows) while the child is STILL paused — the same no-interleave discipline
// as exitScroll — then resumes it. If !repaint, the caller's own transition (home / switch /
// suspend / confirm) already repainted over the panel, so we only resume the paused child.
func (mx *Mux) disarmPanel(repaint bool) {
	mx.mu.Lock()
	ch := mx.pfxCh
	mx.pfxCh = nil
	mx.mu.Unlock()
	if repaint {
		mx.repaintLive() // clean full repaint of the active child (paint happens with ch paused)
	}
	if ch != nil && ch.gate != nil {
		ch.gate.setPaused(false)
	}
}

// forgetPanel clears the armed-panel bookkeeping WITHOUT resuming the child — used when the
// next owner (scroll mode) has already taken over the pause and will resume it itself.
func (mx *Mux) forgetPanel() {
	mx.mu.Lock()
	mx.pfxCh = nil
	mx.mu.Unlock()
}

// panelBg is the subtle overlay background for the command panel (a dim slate); keys are drawn
// bright over it. Colours are re-asserted per row so an item's SGR reset can't strip the bg.
const panelBg = "\x1b[48;5;236m"

// cmdItem renders one command as a bright key + dim label. It deliberately avoids a full SGR
// reset (\x1b[0m) so the surrounding panel background survives across items on a row.
func cmdItem(key, label string) string {
	return panelBg + "\x1b[1;38;5;214m" + key + "\x1b[22;38;5;250m " + label
}

// commandPanelLines packs items into rows that each fit within cols display columns, joined by
// a two-space gap. Pure layout (measures with visLen, so it's ANSI-agnostic; does no capping),
// so the row count grows with the item count and is unit-testable. A single item wider than
// cols still gets its own row (nothing to split it on) — callers clip on paint.
func commandPanelLines(items []string, cols int) []string {
	if cols <= 0 || len(items) == 0 {
		return nil
	}
	const gapW = 2 // "  " between items on a row
	var lines []string
	cur := ""
	curW := 0
	for _, it := range items {
		iw := brand.VisWidth(it)
		switch {
		case cur == "":
			cur, curW = it, iw
		case curW+gapW+iw <= cols:
			cur += "  " + it
			curW += gapW + iw
		default:
			lines = append(lines, cur)
			cur, curW = it, iw
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// drawCmdPanel paints the growing command panel on the K rows directly ABOVE the tab bar while
// the ctrl-\ chord is armed. Height grows with the command count (capped at ~half the screen).
// Every command is gated on the SAME nil-checks the bar/dispatch use, so the panel lists exactly
// what will actually fire. Painted under outMu with DECSC/DECRC, same discipline as drawBar.
func (mx *Mux) drawCmdPanel() {
	mx.mu.Lock()
	rows, cols := mx.rows, mx.cols
	newFn, ctxFn, mcpFn := mx.NewFn != nil, mx.ContextFn != nil, mx.MCPFn != nil
	wtFn, kgFn, shFn := mx.WorktreeFn != nil, mx.KeepGoingFn != nil, mx.ShareFn != nil
	peerFn := mx.PeerFn != nil
	split, canSplit := mx.split != nil, mx.NewPaneHomeFn != nil
	mx.mu.Unlock()
	if rows < 3 || cols <= 0 {
		return // no room for a panel above the bar
	}
	// Optional (session-capability) commands first, then the always-available ones.
	var items []string
	if newFn {
		items = append(items, cmdItem("n", "new/run"))
	}
	if ctxFn {
		items = append(items, cmdItem("c", "context"))
	}
	if mcpFn {
		items = append(items, cmdItem("m", "mcp"))
	}
	if wtFn {
		items = append(items, cmdItem("w", "worktree"))
	}
	if kgFn {
		items = append(items, cmdItem("g", "keep-going"))
	}
	if shFn {
		items = append(items, cmdItem("s", "share"))
	}
	if peerFn {
		items = append(items, cmdItem("p", "ask-peer"))
	}
	if split {
		items = append(items, cmdItem("tab", "focus pane"), cmdItem("z", "zoom pane"))
	} else if canSplit {
		items = append(items, cmdItem("|", "split · pick two"), cmdItem("[", "scroll"))
	} else {
		items = append(items, cmdItem("[", "scroll"))
	}
	closeLbl := "close"
	if split {
		closeLbl = "close pane"
	}
	items = append(items,
		cmdItem("←/→·1-9", "switch"),
		cmdItem("o", "launcher"),
		cmdItem("x", closeLbl),
		cmdItem("q", "quit"),
		cmdItem("esc", "cancel"),
	)
	lines := commandPanelLines(items, cols-2) // -2 for the one-column left/right insets
	// Cap the panel to ~half the screen (leave one row for the top border + the tab bar below).
	maxLines := rows/2 - 1
	if maxLines < 1 {
		maxLines = 1
	}
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	k := len(lines)
	if k == 0 {
		return
	}
	// The bar sits on row `rows`. Command rows occupy (rows-k)..(rows-1); the dim border is the
	// row just above them, at (rows-k-1). Guard against tiny terminals.
	borderRow := rows - k - 1
	if borderRow < 1 {
		return
	}
	pad := func(s string) string { // clip to cols and fill the rest with the panel bg
		s = clipANSI(s, cols)
		if p := cols - brand.VisWidth(s); p > 0 {
			s += strings.Repeat(" ", p)
		}
		return s + "\x1b[0m"
	}
	var b strings.Builder
	b.WriteString("\x1b7") // DECSC — save cursor + mode context
	// Top border: a dim rule on the panel background so it reads as a distinct overlay edge.
	b.WriteString(fmt.Sprintf("\x1b[%d;1H\x1b[2K%s", borderRow,
		pad(panelBg+"\x1b[38;5;240m"+strings.Repeat("─", cols))))
	for i, ln := range lines {
		b.WriteString(fmt.Sprintf("\x1b[%d;1H\x1b[2K%s", rows-k+i, pad(panelBg+" "+ln)))
	}
	b.WriteString("\x1b8") // DECRC — restore the child's cursor + mode context
	mx.outMu.Lock()
	os.Stdout.WriteString(mx.skin(b.String()))
	mx.outMu.Unlock()
}

// teardown runs the exit path exactly once, whichever of the two exits gets there first: the
// normal return from Run, or the SIGINT/SIGTERM handler. fireBeforeQuit is true only for the
// signal path — the keyboard quit paths (requestQuit / handleConfirm) already fired the hook
// themselves before returning, and the Once keeps this from firing it a second time.
func (mx *Mux) teardown(fireBeforeQuit bool) {
	mx.exitOnce.Do(func() {
		if fireBeforeQuit {
			mx.fireBeforeQuit()
		}
		mx.restore()
	})
}

func (mx *Mux) restore() {
	mx.mu.Lock()
	kids := append([]*child(nil), mx.children...)
	mx.mu.Unlock()
	for _, ch := range kids {
		ch.sess.End()
	}
	// Full mode reset on teardown: modeDefaults() clears mouse tracking (?1000/1002/1003/
	// 1006/1015), bracketed paste (?2004), app-cursor (?1) + shows the cursor — otherwise a
	// child that enabled mouse reporting leaves the shell spewing movement reports. Then
	// autowrap on + leave the alt-screen.
	os.Stdout.WriteString(string(modeDefaults()) + "\x1b[?7h\x1b[?1049l")
	if mx.old != nil {
		_ = term.Restore(mx.fd, mx.old)
	}
	if mx.wakeR >= 0 {
		_ = unix.Close(mx.wakeR)
	}
	if mx.wakeW >= 0 {
		_ = unix.Close(mx.wakeW)
	}
}

// clipANSI truncates s to at most w DISPLAY COLUMNS, never severing an escape sequence and never
// cutting mid-rune.
//
// The old hand-rolled version walked BYTES: it counted each byte of a multi-byte glyph as a column
// (so it cut far too early) and could stop halfway through one, emitting the partial character
// visible at the right edge of a full ribbon. brand.Clip does both correctly and is what every
// other surface already uses.
func clipANSI(s string, w int) string { return brand.Clip(s, w) }

// fitRibbon assembles the status bar so the trailing segment (the launcher) always survives, and
// so every session stays visible.
//
// SHRINK LABELS, DON'T DROP TABS. The first cut dropped whole tabs from the right, which was still
// wrong in the way that started this: a tab you cannot see is a session you cannot tell is running,
// and the reported bar showed all nine fitting comfortably — only the launcher was lost. So labels
// give up characters first, down to a floor, and a tab is only dropped when even the floor will not
// fit (a genuinely tiny terminal, where something has to go).
//
// Pure and width-only, so the whole policy is testable without a terminal.
func fitRibbon(prefix string, tabs []string, tail string, sep string, cols int) string {
	if len(tabs) == 0 {
		return prefix + tail
	}
	sepW := visCols(sep)
	budget := cols - visCols(prefix) - visCols(tail) - minHintRoom

	fits := func(ts []string) bool {
		w := 0
		for _, t := range ts {
			w += visCols(t) + sepW
		}
		return w <= budget
	}
	if fits(tabs) {
		return prefix + strings.Join(tabs, sep) + sep + tail
	}
	// Shave a column off the widest tab at a time. Slower than solving for a width, and it keeps
	// short labels fully readable instead of trimming everything to the narrowest common size —
	// "LANDSEARCH" stays whole while "DARCYRENO.COM WEBSITE" gives up its tail.
	cur := append([]string(nil), tabs...)
	for !fits(cur) {
		widest, wi := 0, -1
		for i, t := range cur {
			if w := visCols(t); w > widest && w > minTabWidth {
				widest, wi = w, i
			}
		}
		if wi < 0 {
			break // everything is at the floor — fall through to dropping
		}
		cur[wi] = shrinkSeg(cur[wi])
	}
	// Still over: drop from the right, and say how many.
	keep := len(cur)
	for keep > 0 && !fits(cur[:keep]) {
		keep--
	}
	out := prefix
	for i := 0; i < keep; i++ {
		out += cur[i] + sep
	}
	if keep < len(cur) {
		out += "\x1b[38;5;245m+" + itoaSmall(len(cur)-keep) + "\x1b[0m" + sep
	}
	return out + tail
}

// shrinkSeg removes one display column from a rendered tab, taking it from the LABEL (just before
// the segment's trailing space) so the leading ● marker and the tab NUMBER — the two things you
// navigate by — are never what gets cut.
func shrinkSeg(seg string) string {
	// Segments end " label \x1b[0m". Find the last visible character before the trailing space+reset
	// and drop it, leaving an ellipsis on the first cut so a shortened name reads as shortened.
	const reset = "\x1b[0m"
	body, suffix := seg, ""
	if i := strings.LastIndex(seg, reset); i >= 0 {
		body, suffix = seg[:i], seg[i:]
	}
	body = strings.TrimSuffix(body, " ")
	r := []rune(body)
	if len(r) == 0 {
		return seg
	}
	// Keep exactly one ellipsis rather than accumulating them.
	if r[len(r)-1] == '…' {
		r = r[:len(r)-1]
	}
	if len(r) == 0 {
		return seg
	}
	r = r[:len(r)-1]
	return string(r) + "… " + suffix
}

// minHintRoom is the width left for the right-aligned hint + its gap. Small: the hint clips
// gracefully, whereas a lost launcher does not.
const minHintRoom = 12

// minTabWidth is the floor a tab shrinks to — enough for the marker, the number and a few
// characters of name. Below this a tab stops being identifiable, and dropping it is more honest
// than showing an unreadable stub.
const minTabWidth = 10

func itoaSmall(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return fmt.Sprintf("%d", n)
}
