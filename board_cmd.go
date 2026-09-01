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
	reload *api.Board
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

	m := newBoardModel()
	m.w, m.h = termSize()

	c := api.New()
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
	go pollBoard(c, events, stop)

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
				if ev.reload != nil {
					if ring, note := boardBell(m.board, ev.reload); ring {
						os.Stdout.WriteString("\a")
						m.setToast("→ "+note, false)
					}
					m.board, m.err = ev.reload, nil
				} else {
					m.err = ev.err
				}
				m.restoreFocus()
				// Pin the cursor to whatever it is on NOW, including on the very first load: until
				// something is remembered, the cursor is an index, and index 0 of Backlog after a
				// refresh need not be the card that was there when you looked.
				m.rememberFocus()
				m.refreshDetail(c) // an open detail pane tails, and re-reads its own card
			case ev.tick:
				// nothing to do — a tick exists only to repaint relative timestamps
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
		b, err := c.ReadBoard()
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
func pollBoard(c *api.Client, events chan<- boardEvent, stop <-chan struct{}) {
	load := func() {
		b, err := c.ReadBoard()
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
			load()
		case <-stop:
			return
		}
	}
}

// handleKey applies one keypress. Returns (quit, needsRefresh).
func (m *boardModel) handleKey(b []byte, c *api.Client) (bool, bool) {
	m.toast, m.toastBad = "", false // any key clears the last result — it answered the previous one

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

func (m *boardModel) setToast(s string, bad bool) { m.toast, m.toastBad = s, bad }
