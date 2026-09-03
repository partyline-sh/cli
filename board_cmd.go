package main

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/term"
	"partyline.sh/partyline/internal/api"
)

// board_cmd.go — `ptln board`: the work board in the terminal.
//
// The board was web-only, which put the daily loop (what is queued, what is stuck, what needs
// signing off) behind a browser tab while the work itself — the repos, the agents, the sessions —
// lives here. This is the same board over the same endpoints; nothing about the control plane
// changes. What changes is that accepting a run and reading its diff no longer means leaving the
// terminal.
//
// Structure: one goroutine reads keys, one refreshes the board on a timer, and the main loop owns
// ALL state and does every repaint. Nothing else writes to the screen or touches the model, which
// is what keeps a refresh landing mid-keystroke from tearing the frame or moving the cursor under
// somebody's hands.

// boardRefresh is how often the board reloads on its own. The web board pairs an SSE ping with a
// slow timer; the terminal keeps only the timer for now — a change shows within this window, and
// `g` forces it immediately.
const boardRefresh = 5 * time.Second

type boardEvent struct {
	key    []byte
	reload *boardData
	err    error
	resize bool
	tick   bool

	// A finished run-log fetch, carrying the run it was for. The id travels with the payload so a
	// slow fetch cannot paint one card's log into another card's pane after the cursor has moved.
	logsFor string
	logs    []api.RunLogLine
	logsErr error

	// A finished run action: what it was called, and whether the server took it.
	actionLabel string
	actionErr   error

	// A finished scope listing, carrying the source it was for so a slow one cannot open a picker
	// for a board you have since switched away from.
	scopesFor string
	scopes    []boardScope
	scopesErr error
}

// boardMain is the command entry. --help never reaches here: clispec intercepts it and prints the
// registry entry, which is where the keymap is declared.
func boardMain(args []string) {
	_ = args
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		// Piped or scripted: print the board once, in the same prose an agent reads through
		// read_board, rather than refusing. `ptln board | less` should say something useful.
		c := api.New()
		b, err := c.ReadBoard()
		if err != nil {
			fatal(err)
		}
		fmt.Print(renderBoard(b))
		return
	}
	if err := runBoardApp(); err != nil {
		fmt.Fprintln(os.Stderr, "ptln board: "+err.Error())
		os.Exit(1)
	}
}

func runBoardApp() error {
	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	restore := func() {
		_ = term.Restore(fd, old)
		os.Stdout.WriteString("\x1b[?1049l\x1b[?25h") // primary screen, cursor back
	}
	defer restore()
	os.Stdout.WriteString("\x1b[?1049h\x1b[?25l") // alt screen, hidden cursor

	c := api.New()

	m := newBoardModel()
	m.w, m.h = termSize()
	// partyline first, then whatever board providers are installed (board_provider.go). The order is
	// the switcher's order, and partyline leading means the board opens on your own work.
	m.sources = append([]boardSource{partylineSource{c: c}}, discoverBoardProviders()...)
	m.client, m.srcErr = c, map[string]string{}
	// The baseline for binaryReplaced: what ptln looked like on disk when this board started.
	if fi, err := os.Stat(selfExe()); err == nil {
		m.selfStamp = fi.ModTime()
	}
	m.beginBusy("reading " + m.activeSource().Name())
	m.scope = loadBoardScope(m.activeSource().Name())
	events := make(chan boardEvent, 8)

	var once sync.Once
	stop := make(chan struct{})
	closeStop := func() { once.Do(func() { close(stop) }) }
	defer closeStop()

	// The model can start its own background reads (a detail pane's log tail) and needs somewhere to
	// deliver them. It only ever SENDS; the loop below is the sole reader and the sole writer of state.
	m.events, m.stop = events, stop

	go readBoardKeys(events, stop)
	go watchBoardResize(events, stop)
	go m.pollBoard(events, stop)

	// First paint before the first load lands, so the screen is never blank while the network runs.
	m.render()

	for {
		select {
		case ev := <-events:
			switch {
			case ev.resize:
				m.w, m.h = termSize()
			case ev.actionLabel != "":
				m.applyActionResult(ev.actionLabel, ev.actionErr)
				m.reloadSoon(c) // the board moved (or did not) — find out which
			case ev.logsFor != "":
				m.applyLogs(ev.logsFor, ev.logs, ev.logsErr)
			case ev.reload != nil || ev.err != nil:
				m.endBusy()
				if ev.reload != nil {
					if ring, note := boardBell(m.data, ev.reload); ring {
						os.Stdout.WriteString("\a")
						m.setToast("→ "+note, false)
					}
					m.data, m.err = ev.reload, nil
					if s := m.activeSource(); s != nil {
						delete(m.srcErr, s.Name()) // it works now; stop saying it does not
					}
					if m.toastPending {
						m.toast, m.toastPending = "", false
					}
				} else {
					m.err = ev.err
					// Remembered against the source so the board picker can show WHY this one is
					// unusable, instead of letting you select it and find out.
					if s := m.activeSource(); s != nil && ev.err != nil {
						m.srcErr[s.Name()] = ev.err.Error()
					}
				}
				m.restoreFocus()
				// Pin the cursor to whatever it is on NOW, including on the very first load: until
				// something is remembered, the cursor is an index, and index 0 of Backlog after a
				// refresh need not be the card that was there when you looked.
				m.rememberFocus()
				m.refreshDetail(c) // an open detail pane tails, and re-reads its own card
			case ev.scopesFor != "":
				m.applyScopes(ev.scopesFor, ev.scopes, ev.scopesErr)
			case ev.tick:
				// nothing to do — a tick exists only to repaint relative timestamps, and to step
				// the loading animation while a read is in flight
			case ev.key != nil:
				quit, refresh := m.handleKey(ev.key, c)
				if quit {
					// A hand-off REPLACES this process rather than spawning a child. The key reader
					// is parked in a blocking Read on stdin that closing `stop` cannot interrupt, so
					// a child would race it for every byte and the operator would lose keystrokes in
					// whatever they were handed to. Exec leaves exactly one reader on the terminal.
					if m.handOff != nil {
						closeStop()
						restore()
						m.handOff()
					}
					return nil
				}
				if refresh {
					// Off the loop. A synchronous read froze the entire UI — keys included — for a
					// network round trip, so a slow instance made the board feel hung on every
					// action and every `g`.
					m.reloadSoon(c)
				}
			}
			m.render()
		case <-stop:
			return nil
		}
	}
}

func (m *boardModel) render() { os.Stdout.WriteString(themed(m.frame())) }

// reloadSoon re-reads the board in the background and delivers it as an event, like the poller.
func (m *boardModel) reloadSoon(c *api.Client) {
	events, stop := m.events, m.stop
	if events == nil {
		return
	}
	go func() {
		b, err := m.loadActive()
		ev := boardEvent{reload: b, err: err}
		if err != nil {
			ev.reload = nil
		}
		select {
		case events <- ev:
		case <-stop:
		}
	}()
}

func termSize() (int, int) {
	w, h, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w <= 0 || h <= 0 {
		return 100, 30
	}
	return w, h
}

// readBoardKeys forwards raw keypresses. Reads are chunked rather than byte-at-a-time so an escape
// sequence (an arrow key is three bytes) arrives as one event and cannot be split across two.
func readBoardKeys(events chan<- boardEvent, stop <-chan struct{}) {
	buf := make([]byte, 16)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil || n == 0 {
			return
		}
		b := append([]byte(nil), buf[:n]...)
		select {
		case events <- boardEvent{key: b}:
		case <-stop:
			return
		}
	}
}

func watchBoardResize(events chan<- boardEvent, stop <-chan struct{}) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	defer signal.Stop(ch)
	for {
		select {
		case <-ch:
			select {
			case events <- boardEvent{resize: true}:
			case <-stop:
				return
			}
		case <-stop:
			return
		}
	}
}

// pollBoard reloads on a timer. A failed load is reported and the OLD board is kept: a network
// blip should degrade to stale data with a line saying so, never to an empty board that looks like
// all your work disappeared.
// pollBoard refreshes on a timer — but ONLY while the active source is live.
//
// A foreign board does not poll. partyline's own instance is fine to ask every five seconds;
// somebody's Odoo is not, and a foreign board is read when you are deciding what to pick up rather
// than watched. The timer keeps running so a switch back to partyline resumes without a restart,
// and the tick is SKIPPED rather than fetched when the source is manual.
func (m *boardModel) pollBoard(events chan<- boardEvent, stop <-chan struct{}) {
	load := func() {
		b, err := m.loadActive()
		ev := boardEvent{reload: b, err: err}
		if err != nil {
			ev.reload = nil
		}
		select {
		case events <- ev:
		case <-stop:
		}
	}
	load()
	t := time.NewTicker(boardRefresh)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			if d := m.loaded(); d != nil && !d.Live && !m.dueForPoll(d) {
				continue // manual source: `g` is the only refresh, unless one was configured
			}
			load()
		case <-stop:
			return
		}
	}
}

// handleKey applies one keypress. Returns (quit, needsRefresh).
func (m *boardModel) handleKey(b []byte, c *api.Client) (bool, bool) {
	m.toast, m.toastBad, m.toastPending = "", false, false // any key clears the last result

	// An open modal owns every key, including q and the arrows: nothing should move on the board
	// underneath a question you have not answered.
	if m.overlay != nil {
		quit, refresh := m.overlayKey(b, c)
		return quit, refresh
	}

	if len(b) >= 3 && b[0] == 0x1b && b[1] == '[' {
		// Shift-arrows arrive as ESC [ 1 ; 2 A/B — a different shape from a plain arrow, and the
		// reason reorder needs its own branch rather than a modifier flag on the one below.
		if seq := string(b); strings.HasPrefix(seq, "\x1b[1;2") {
			switch seq[len(seq)-1] {
			case 'A':
				return false, m.reorder(c, -1)
			case 'B':
				return false, m.reorder(c, 1)
			}
			return false, false
		}
		switch b[2] {
		case 'A':
			m.moveCursor(-1)
			return false, false
		case 'B':
			m.moveCursor(1)
			return false, false
		case 'C':
			m.moveColumn(1)
			return false, false
		case 'D':
			m.moveColumn(-1)
			return false, false
		}
		return false, false
	}
	if len(b) == 1 && b[0] == 0x1b {
		return true, false // lone esc leaves
	}

	switch r := rune(b[0]); r {
	case 'q', 0x03: // q / ctrl-c
		return true, false
	case 'k':
		m.moveCursor(-1)
	case 'j':
		m.moveCursor(1)
	case 'h':
		m.moveColumn(-1)
	case 'l':
		m.moveColumn(1)
	case 'g':
		return false, true
	case '\r', '\n':
		// On a board whose cards are containers — an Odoo project holding tasks — ⏎ descends.
		// Everywhere else it keeps meaning "the primary action on this card".
		if m.drillInto(c) {
			return false, true
		}
		// A foreign card has no action to run, so ⏎ used to answer "nothing to do on this card".
		// It has plenty to READ, which is what opening it should mean.
		if card, ok := m.focused(); ok && card.Foreign {
			m.detailPane(c)
			return false, false
		}
		return false, m.enter(c)
	case 'a':
		return false, m.actionMenu(c)
	case 'd':
		m.detailPane(c)
	case 'o':
		m.openPR()
	case 's':
		return m.attachSession(), false
	case 'r':
		m.reviewDiff()
	case 'n':
		return false, m.newWork(c)
	case 'D':
		refresh := m.describeWork(c)
		if m.quitAfterKey {
			m.quitAfterKey = false
			return true, false
		}
		return false, refresh
	case 'P':
		if card, ok := m.focused(); ok {
			return false, m.promoteItem(c, *card)
		}
	case 'S':
		return false, m.pickSource(c)
	case 'p':
		return false, m.pickScope(c)
	case 'i':
		return false, m.importForeign(c)
	case '?':
		m.helpOverlay()
	}
	// A handler that arranged a hand-off (describe, attach) asks for the exit here rather than
	// returning it up through every branch.
	if m.quitAfterKey {
		m.quitAfterKey = false
		return true, false
	}
	return false, false
}

// enter fires the focused row's primary move: fold a chain, or the card's first offered action.
func (m *boardModel) enter(c *api.Client) bool {
	row, ok := m.focusedRow()
	if !ok {
		return false
	}
	if row.header() {
		m.collapsed[row.ChainID] = !m.collapsed[row.ChainID]
		m.clamp()
		return false
	}
	act, has := primaryAction(*row.Card)
	if !has {
		m.setToast("nothing to do on this card", false)
		return false
	}
	return m.runAction(c, *row.Card, act)
}

// runAction is the guard: a destructive move opens a confirmation and fires nothing yet, everything
// else goes straight through to fireAction.
func (m *boardModel) runAction(c *api.Client, card api.BoardCard, act boardAction) bool {
	if act.Danger {
		m.confirmAction(act.Confirm, card, act)
		return false
	}
	return m.fireAction(c, card, act)
}

// fireAction performs the move. Everything that reaches here is either non-destructive or has been
// confirmed — it never asks again.
func (m *boardModel) fireAction(c *api.Client, card api.BoardCard, act boardAction) bool {
	switch act.Key {
	case "promote":
		return m.promoteItem(c, card)
	case "delete":
		return m.deleteItem(c, card)
	case "rank":
		m.setToast("use shift-↑/↓ on a backlog card to reorder it", false)
		return false
	}

	// Fired off the loop, so a slow or hung instance cannot freeze the board mid-action. The result
	// arrives as an event and lands in the status line; the board stays usable throughout.
	m.setToast(act.Label+"…", false)
	events, stop := m.events, m.stop
	if events == nil {
		return false
	}
	label, id, path, force := act.Label, card.ID, act.Path, act.Force
	go func() {
		_, err := c.RunAction(id, path, force)
		select {
		case events <- boardEvent{actionLabel: label, actionErr: err}:
		case <-stop:
		}
	}()
	return false
}

// applyActionResult turns a finished action into the line the operator reads.
func (m *boardModel) applyActionResult(label string, err error) {
	if err == nil {
		m.setToast(label+" — done", false)
		return
	}
	// A 409 is the server saying the card already moved, which is information rather than a failure.
	// Telling someone their keypress "failed" when the board simply moved on under them is worse
	// than saying what actually happened.
	if strings.Contains(err.Error(), "409") {
		m.setToast("that card had already moved — the board is up to date", false)
		return
	}
	m.setToast(label+" refused: "+err.Error(), true)
}

func (m *boardModel) setToast(s string, bad bool) {
	m.toast, m.toastBad, m.toastPending = s, bad, false
}

// setPendingToast announces work that is still happening. It is cleared the moment the board it
// promised arrives, rather than lingering as a false claim over fresh data.
func (m *boardModel) setPendingToast(s string) {
	m.toast, m.toastBad, m.toastPending = s, false, true
}

// dueForPoll reports whether a manual source has asked to be re-read on an interval anyway.
//
// Opt-in per board, in minutes, because the right answer is a property of the tracker and not of
// partyline: a hosted Jira behind a rate limit and a local Odoo want very different numbers, and
// the default of never is the one that cannot be rude to anybody.
func (m *boardModel) dueForPoll(d *boardData) bool {
	s, ok := m.activeSource().(pollingSource)
	if !ok {
		return false
	}
	every := s.PollInterval()
	if every <= 0 {
		return false
	}
	return time.Since(d.ReadAt) >= every
}

// pollingSource is the optional half of boardSource: a provider that wants automatic re-reads says
// how often. Optional rather than part of boardSource so that writing a provider stays a two-method
// job — nobody should have to answer this question to show a board.
type pollingSource interface {
	PollInterval() time.Duration
}

// ── loading ──────────────────────────────────────────────────────────────────────────────────────

// busyPulse is how often the loading indicator advances. Fast enough to read as motion, slow
// enough that a board on a slow link is not spending its time repainting a spinner.
const busyPulse = 110 * time.Millisecond

// beginBusy marks a read as in flight and starts the pulse that animates it.
//
// The indicator matters more here than in most TUIs: a foreign board is a network round trip to
// somebody else's tracker, which can take seconds, and a static "reading…" is indistinguishable
// from a hang. Without motion there is no way to tell a slow Odoo from a broken one.
func (m *boardModel) beginBusy(what string) {
	if m.busy != "" {
		return // already pulsing; one animation is enough
	}
	m.busy, m.busySince = what, time.Now()
	events, stop := m.events, m.stop
	if events == nil {
		return
	}
	go func() {
		t := time.NewTicker(busyPulse)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				select {
				case events <- boardEvent{tick: true}:
				case <-stop:
					return
				}
			case <-stop:
				return
			}
		}
	}()
}

func (m *boardModel) endBusy() { m.busy, m.busySince = "", time.Time{} }

// fetchScopes lists a source's scopes OFF the event loop.
//
// It used to run inline in the key handler, which froze the whole board for the length of the
// call — no repaint, no spinner, no way to tell it apart from a crash. On a tracker with seventy
// nine projects behind an HTTP hop that is seconds of a dead terminal.
func (m *boardModel) fetchScopes(s boardSource) {
	events, stop := m.events, m.stop
	if events == nil {
		return
	}
	name := s.Name()
	m.beginBusy("reading " + name + "'s projects")
	go func() {
		scopes, err := s.Scopes()
		ev := boardEvent{scopesFor: name, scopes: scopes, scopesErr: err}
		select {
		case events <- ev:
		case <-stop:
		}
	}()
}
