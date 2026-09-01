package main

import (
	"strings"

	"partyline.sh/partyline/internal/api"
)

// board_overlay.go — modals over the board: confirm, action menu, detail, text entry, pickers.
//
// Every one of them reads its keys through the board's SINGLE stdin reader rather than opening its
// own. A modal that does its own term.MakeRaw + Read (the pattern the ctrl-\ menus use, which is
// right for them because nothing else is reading) would here be a second consumer of the same fd,
// racing the board's reader for every byte: keys would land in whichever goroutine woke first, and
// an arrow key's three bytes could be split across both. So the board owns the reader, the model
// owns an overlay, and keys route to the overlay when one is open.

// boardOverlay is one modal.
type boardOverlay interface {
	// title is the box's embedded heading.
	title() string
	// lines is the body, already styled, clipped by the caller to the box width.
	lines(m *boardModel, w, h int) []string
	// key handles one keypress. Returns whether to close the overlay and whether the board should
	// reload afterwards.
	key(b []byte, m *boardModel, c *api.Client) (close bool, refresh bool)
	// footer is the modal's own hint line.
	footer() string
}

// openOverlay replaces any current overlay. Replacing rather than stacking is deliberate: a stack
// of modals in a terminal is a place to get lost, and every flow here is one question deep.
func (m *boardModel) openOverlay(o boardOverlay) { m.overlay = o }

func (m *boardModel) closeOverlay() { m.overlay = nil }

// overlayKey routes a keypress to the open overlay. Esc always closes, in every overlay, without
// the overlay having to remember to implement it.
func (m *boardModel) overlayKey(b []byte, c *api.Client) (bool, bool) {
	if m.overlay == nil {
		return false, false
	}
	if len(b) == 1 && (b[0] == 0x1b || b[0] == 0x03) {
		m.closeOverlay()
		return false, false
	}
	// Identity, not a bare nil-out. Every multi-step flow works by having a handler open the NEXT
	// overlay and return close=true for itself — a machine picker leading to a project picker, a
	// confirm leading to a promote. Closing unconditionally threw the next step away: the modal
	// vanished, nothing happened, and no error said why. Only close if the overlay we dispatched to
	// is still the one on screen.
	cur := m.overlay
	closeIt, refresh := cur.key(b, m, c)
	if closeIt && m.overlay == cur {
		m.closeOverlay()
	}
	return false, refresh
}

// ── confirm ─────────────────────────────────────────────────────────────────────────────────────

// confirmOverlay guards a destructive move. It requires an explicit y: enter does NOT confirm,
// because enter is the board's "do the primary thing" key and muscle memory would fire it straight
// through the guard that exists to interrupt muscle memory.
type confirmOverlay struct {
	prompt string
	onYes  func(m *boardModel, c *api.Client) bool
}

func (o *confirmOverlay) title() string  { return "confirm" }
func (o *confirmOverlay) footer() string { return boardDim + "y confirm · esc cancel" + reset }

func (o *confirmOverlay) lines(m *boardModel, w, h int) []string {
	return wrapPlain(o.prompt, w)
}

func (o *confirmOverlay) key(b []byte, m *boardModel, c *api.Client) (bool, bool) {
	if len(b) == 0 {
		return false, false
	}
	if b[0] == 'y' || b[0] == 'Y' {
		return true, o.onYes(m, c)
	}
	if b[0] == 'n' || b[0] == 'N' {
		return true, false
	}
	return false, false
}

// confirmAction opens the guard for a destructive action and runs it on y.
func (m *boardModel) confirmAction(prompt string, card api.BoardCard, act boardAction) {
	m.openOverlay(&confirmOverlay{
		prompt: prompt,
		onYes: func(m *boardModel, c *api.Client) bool {
			return m.fireAction(c, card, act)
		},
	})
}

// ── action menu ─────────────────────────────────────────────────────────────────────────────────

// actionOverlay lists every move a card has, with what each one does. It exists because the hint
// bar can only name the primary: the whole point of a board is that the non-obvious move (restart
// rather than continue, to-backlog rather than discard) is sometimes the right one, and a UI that
// only offers the obvious one pushes people back to the browser.
type actionOverlay struct {
	card api.BoardCard
	acts []boardAction
	sel  int
}

func (o *actionOverlay) title() string { return "actions · " + clipVis(cardTitle(o.card), 40) }
func (o *actionOverlay) footer() string {
	return boardDim + "↑↓ move · ⏎ do it · esc close" + reset
}

func (o *actionOverlay) lines(m *boardModel, w, h int) []string {
	var out []string
	for i, a := range o.acts {
		marker, label := "  ", a.Label
		if i == o.sel {
			marker = boardCol + "▸ " + reset
			label = "\x1b[1m" + label + "\x1b[22m"
		}
		if a.Danger {
			label = boardBad + a.Label + reset
		} else if a.Muted {
			label = boardDim + a.Label + reset
		}
		out = append(out, marker+padVis(label, 14)+boardDim+clipVis(a.Hint, max(10, w-18))+reset)
	}
	return out
}

func (o *actionOverlay) key(b []byte, m *boardModel, c *api.Client) (bool, bool) {
	if len(b) >= 3 && b[0] == 0x1b && b[1] == '[' {
		switch b[2] {
		case 'A':
			o.sel = max(0, o.sel-1)
		case 'B':
			if o.sel < len(o.acts)-1 {
				o.sel++
			}
		}
		return false, false
	}
	switch b[0] {
	case 'k':
		o.sel = max(0, o.sel-1)
	case 'j':
		if o.sel < len(o.acts)-1 {
			o.sel++
		}
	case '\r', '\n':
		if o.sel < 0 || o.sel >= len(o.acts) {
			return true, false
		}
		act := o.acts[o.sel]
		if act.Danger {
			// Swap this overlay for the guard rather than firing straight from the menu.
			m.confirmAction(act.Confirm, o.card, act)
			return false, false
		}
		return true, m.fireAction(c, o.card, act)
	}
	return false, false
}

// actionMenu opens the full move list for the focused card.
func (m *boardModel) actionMenu(c *api.Client) bool {
	card, ok := m.focused()
	if !ok {
		return false
	}
	acts := boardActions(*card)
	if len(acts) == 0 {
		m.setToast("nothing to do on this card — it is finished", false)
		return false
	}
	m.openOverlay(&actionOverlay{card: *card, acts: acts})
	return false
}

// ── text entry ──────────────────────────────────────────────────────────────────────────────────

// inputOverlay is a one-line prompt. Deliberately minimal editing (printable characters and
// backspace): anything more is a text editor, and the flows that need one shell out to $EDITOR.
type inputOverlay struct {
	prompt string
	value  string
	hint   string
	onDone func(m *boardModel, c *api.Client, value string) bool
}

func (o *inputOverlay) title() string  { return o.prompt }
func (o *inputOverlay) footer() string { return boardDim + "⏎ save · esc cancel" + reset }

func (o *inputOverlay) lines(m *boardModel, w, h int) []string {
	out := []string{"  " + o.value + boardCol + "▏" + reset}
	if o.hint != "" {
		out = append(out, "", boardDim+"  "+clipVis(o.hint, max(10, w-4))+reset)
	}
	return out
}

func (o *inputOverlay) key(b []byte, m *boardModel, c *api.Client) (bool, bool) {
	if len(b) >= 3 && b[0] == 0x1b {
		return false, false // arrows and friends: ignored rather than inserted as escape junk
	}
	switch b[0] {
	case '\r', '\n':
		if strings.TrimSpace(o.value) == "" {
			return true, false
		}
		return true, o.onDone(m, c, strings.TrimSpace(o.value))
	case 0x7f, 0x08: // backspace
		if r := []rune(o.value); len(r) > 0 {
			o.value = string(r[:len(r)-1])
		}
		return false, false
	}
	for _, ch := range string(b) {
		if ch >= 0x20 && ch != 0x7f {
			o.value += string(ch)
		}
	}
	return false, false
}

// ── picker ──────────────────────────────────────────────────────────────────────────────────────

// pickerItem is one choice, with the value the caller acts on.
type pickerItem struct {
	Label string
	Note  string
	Value string
}

// pickerOverlay chooses from a list — a machine, a project, a thread.
type pickerOverlay struct {
	heading string
	items   []pickerItem
	sel     int
	top     int // first visible item — a machine list longer than the screen must scroll, not overflow
	onPick  func(m *boardModel, c *api.Client, v pickerItem) bool
}

func (o *pickerOverlay) title() string { return o.heading }
func (o *pickerOverlay) footer() string {
	return boardDim + "↑↓ move · ⏎ choose · esc cancel" + reset
}

func (o *pickerOverlay) lines(m *boardModel, w, h int) []string {
	if len(o.items) == 0 {
		return []string{boardDim + "  nothing to choose from" + reset}
	}
	// A box taller than the terminal writes past the bottom and scrolls the board out from under
	// itself, and the items below the fold cannot be reached at all. Window it.
	if h < 1 {
		h = 1
	}
	if o.sel < o.top {
		o.top = o.sel
	}
	if o.sel >= o.top+h {
		o.top = o.sel - h + 1
	}
	if o.top > max(0, len(o.items)-h) {
		o.top = max(0, len(o.items)-h)
	}
	end := min(len(o.items), o.top+h)

	var out []string
	for i, it := range o.items[o.top:end] {
		i += o.top
		marker, label := "  ", it.Label
		if i == o.sel {
			marker = boardCol + "▸ " + reset
			label = "\x1b[1m" + label + "\x1b[22m"
		}
		row := marker + padVis(label, 24)
		if it.Note != "" {
			row += boardDim + clipVis(it.Note, max(8, w-28)) + reset
		}
		out = append(out, row)
	}
	return out
}

func (o *pickerOverlay) key(b []byte, m *boardModel, c *api.Client) (bool, bool) {
	if len(b) >= 3 && b[0] == 0x1b && b[1] == '[' {
		switch b[2] {
		case 'A':
			o.sel = max(0, o.sel-1)
		case 'B':
			if o.sel < len(o.items)-1 {
				o.sel++
			}
		}
		return false, false
	}
	switch b[0] {
	case 'k':
		o.sel = max(0, o.sel-1)
	case 'j':
		if o.sel < len(o.items)-1 {
			o.sel++
		}
	case '\r', '\n':
		if o.sel < 0 || o.sel >= len(o.items) {
			return true, false
		}
		return true, o.onPick(m, c, o.items[o.sel])
	}
	return false, false
}

// ── notice ──────────────────────────────────────────────────────────────────────────────────────

// noticeOverlay is read-only prose: help, or an explanation too long for the status line.
type noticeOverlay struct {
	heading string
	body    []string
	scroll  int
}

func (o *noticeOverlay) title() string  { return o.heading }
func (o *noticeOverlay) footer() string { return boardDim + "↑↓ scroll · esc close" + reset }

func (o *noticeOverlay) lines(m *boardModel, w, h int) []string {
	if o.scroll > max(0, len(o.body)-h) {
		o.scroll = max(0, len(o.body)-h)
	}
	end := min(len(o.body), o.scroll+h)
	return o.body[o.scroll:end]
}

func (o *noticeOverlay) key(b []byte, m *boardModel, c *api.Client) (bool, bool) {
	if len(b) >= 3 && b[0] == 0x1b && b[1] == '[' {
		switch b[2] {
		case 'A':
			o.scroll = max(0, o.scroll-1)
		case 'B':
			o.scroll++
		}
		return false, false
	}
	switch b[0] {
	case 'k':
		o.scroll = max(0, o.scroll-1)
	case 'j':
		o.scroll++
	case '\r', '\n', 'q':
		return true, false
	}
	return false, false
}

// helpOverlay is the whole keymap, because a TUI whose keys are only in a hint bar teaches four of
// them and hides the rest.
func (m *boardModel) helpOverlay() {
	m.openOverlay(&noticeOverlay{heading: "keys", body: []string{
		boardMid + "moving" + reset,
		"  ↑ ↓ / j k      move within a column",
		"  ← → / h l      change column (skips empty ones)",
		"  ⏎              on a chain: fold · on a card: its primary move",
		"",
		boardMid + "acting on a card" + reset,
		"  a              every move this card has, with what each does",
		"  d              detail: acceptance criteria, the live run log, why it is stuck",
		"  s              attach the run's live session in a new window",
		"  r              review the diff (Review column)",
		"  o              open the PR in a browser",
		"",
		boardMid + "putting work on the board" + reset,
		"  n              file a new backlog item",
		"  D              describe a problem — an agent shapes it into buildable work",
		"  P              promote a planned item onto a machine",
		"",
		boardMid + "other boards" + reset,
		"  S              switch source — partyline, or a board provider you have installed",
		"  p              pick which project/board/team to show from this source",
		"  i              import a foreign card into partyline as planned work",
		"",
		boardMid + "the board itself" + reset,
		"  g              refresh now (partyline also refreshes on its own; other sources do not)",
		"  ?              this list",
		"  q / esc        leave",
	}})
}

// ── drawing ─────────────────────────────────────────────────────────────────────────────────────

// overlayBox paints the modal centred over the board.
func (m *boardModel) overlayBox(w, h int) []string {
	if m.overlay == nil {
		return nil
	}
	bw := min(w-4, 78)
	if bw < 20 {
		bw = max(10, w-2)
	}
	inner := bw - 4

	body := m.overlay.lines(m, inner, max(3, h-8))
	rows := make([]string, 0, len(body)+4)
	rows = append(rows, boxTop(m.overlay.title(), bw, 39))
	for _, l := range body {
		rows = append(rows, frameClr+"│ "+reset+padVis(l, inner)+frameClr+" │"+reset)
	}
	rows = append(rows,
		frameClr+"│ "+reset+padVis("", inner)+frameClr+" │"+reset,
		frameClr+"│ "+reset+padVis(m.overlay.footer(), inner)+frameClr+" │"+reset,
		boxBottom(bw))
	return rows
}

// wrapPlain word-wraps uncoloured prose for a modal body.
func wrapPlain(s string, w int) []string {
	if w < 8 {
		w = 8
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		line := ""
		for _, word := range strings.Fields(para) {
			switch {
			case line == "":
				line = word
			case visWidth(line)+1+visWidth(word) <= w:
				line += " " + word
			default:
				out = append(out, line)
				line = word
			}
		}
		out = append(out, line)
	}
	return out
}

// NOTE: no local min() here. Go's builtin min is generic and party_agent.go calls it with
// time.Duration; defining a package-level min(int, int) would shadow the builtin and break that
// call. (max IS defined locally, in llms_tui.go, and predates the builtin.)
