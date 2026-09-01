package main

import (
	"sort"
	"strings"

	"partyline.sh/partyline/internal/api"
)

// board_model.go — the terminal board's state and the pure functions that shape it: how cards order
// inside a column, how chains group, and how many columns fit the terminal you actually have.
//
// Everything here is pure and tested. The rendering and the key handling read this; they never
// re-derive it, so "what is on screen" has exactly one definition.

// Minimum readable width for one column. Below this a card shows a truncated project label and
// nothing else useful, so the board switches to fewer columns rather than render five unreadable
// ones — the terminal equivalent of the web board's mobile lane switch.
const minColWidth = 24

// boardModel is the whole screen's state.
type boardModel struct {
	data *boardData
	err  error // last refresh error — shown in the status line, never fatal

	// Which source is showing, and where its scope picker last landed. The board switches between
	// partyline and whatever providers are installed; see board_source.go.
	sources []boardSource
	src     int
	scope   string // the active scope id ("" when the source has none)

	col int // focused column, an index into api.BoardColumns
	// Per-column cursor and scroll, kept by column KEY rather than by index so they survive a
	// refresh that changes how many cards a column holds.
	cursor map[api.BoardColumn]int
	scroll map[api.BoardColumn]int

	collapsed map[string]bool // chain id → folded

	w, h int

	// Where background reads started by the model deliver their results, and the channel that tells
	// them to give up when the board exits. Send-only from the model's side.
	events chan<- boardEvent
	stop   <-chan struct{}

	// Set by a handler that has arranged a hand-off and wants the board to exit after this key.
	quitAfterKey bool

	// What to run after the board exits and the terminal is restored — attaching a session, mostly.
	// The board cannot run an interactive program under its own alt screen, so it steps aside first.
	handOff func()

	// The open modal, if any. One at a time — see board_overlay.go.
	overlay boardOverlay

	// The last thing that happened, shown in the status line: an action's result, a refusal from
	// the server, a promotion's outcome. Cleared on the next keypress so it reads as a response to
	// what you just did rather than as permanent chrome.
	toast    string
	toastBad bool
	// toastPending marks a toast that ANNOUNCES work in flight ("reading odoo…") rather than
	// reporting a result. It is cleared when that work lands, so the status line stops claiming to
	// be loading a board that has already arrived — and stops hiding the freshness note behind it.
	toastPending bool

	// What the cursor is on, remembered across a refresh: the board reloads every few seconds, and a
	// cursor that jumped because a card moved column is how you accept the wrong thing. Exactly one
	// of these is set — a chain header is a real cursor position and must be remembered as itself,
	// or every poll would drag the cursor back down to the last card and the board would fight you.
	focusID      string
	focusChainID string
}

// newBoardModel builds the empty model. The caller remembers focus once the first board lands, so
// even an untouched cursor is pinned to a CARD rather than to row 0 of whatever arrives next.
func newBoardModel() *boardModel {
	return &boardModel{
		cursor:    map[api.BoardColumn]int{},
		scroll:    map[api.BoardColumn]int{},
		collapsed: map[string]bool{},
	}
}

// boardRow is one line in a column: either a chain header or a card.
type boardRow struct {
	Card    *api.BoardCard
	ChainID string // set on a header row
	Count   int    // members, on a header row
}

func (r boardRow) header() bool { return r.Card == nil }

// sortColumn orders one column's cards the way the web does.
//
// Backlog keeps QUEUE order (descending rank) because that column answers "what is next" and its
// order is a decision somebody made. Every other column is arrival order, newest first, because
// they answer "what happened" and the newest thing is the one you have not seen.
func sortColumn(col api.BoardColumn, cards []api.BoardCard) []api.BoardCard {
	out := append([]api.BoardCard(nil), cards...)
	if col == api.ColBacklog {
		sort.SliceStable(out, func(i, j int) bool { return out[i].Rank > out[j].Rank })
		return out
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

// columnRows lays a column out as rows: chained cards cluster under one header (folded or not),
// unchained cards stand alone.
//
// Chain members stay in the column's own order and are NOT re-sorted inside the group: within a
// chain, order is the execution order, and showing it any other way would misrepresent what runs
// next. A chain with a single visible member gets no header — a group of one is noise.
func columnRows(col api.BoardColumn, cards []api.BoardCard, collapsed map[string]bool) []boardRow {
	sorted := sortColumn(col, cards)

	counts := map[string]int{}
	for _, c := range sorted {
		if c.ChainID != "" {
			counts[c.ChainID]++
		}
	}

	var rows []boardRow
	seen := map[string]bool{}
	for i := range sorted {
		c := sorted[i]
		chain := c.ChainID
		if chain == "" || counts[chain] < 2 {
			rows = append(rows, boardRow{Card: &sorted[i]})
			continue
		}
		if !seen[chain] {
			seen[chain] = true
			rows = append(rows, boardRow{ChainID: chain, Count: counts[chain]})
		}
		if collapsed[chain] {
			continue
		}
		rows = append(rows, boardRow{Card: &sorted[i]})
	}
	return rows
}

// loaded is the board as it stands, safe to read from the poll goroutine's perspective only for the
// Live flag it needs — every other read happens on the event loop.
func (m *boardModel) loaded() *boardData { return m.data }

// rows returns the focused layout for one column.
func (m *boardModel) rows(col api.BoardColumn) []boardRow {
	if m.data == nil {
		return nil
	}
	return columnRows(col, m.data.Column(col), m.collapsed)
}

// columnKeys is the loaded board's column order — the source's, not a constant. Falls back to
// partyline's five before the first load so the frame has something to draw.
func (m *boardModel) columnKeys() []api.BoardColumn {
	if k := m.data.Keys(); len(k) > 0 {
		return k
	}
	return api.BoardColumns
}

// focusedColumn is the column key the cursor is in.
func (m *boardModel) focusedColumn() api.BoardColumn {
	keys := m.columnKeys()
	if m.col < 0 || m.col >= len(keys) {
		return keys[0]
	}
	return keys[m.col]
}

// focused returns the card under the cursor, if the cursor is on a card (it may be on a chain
// header, which is a real position with its own moves — fold/unfold).
func (m *boardModel) focused() (*api.BoardCard, bool) {
	rows := m.rows(m.focusedColumn())
	i := m.cursor[m.focusedColumn()]
	if i < 0 || i >= len(rows) || rows[i].header() {
		return nil, false
	}
	return rows[i].Card, true
}

// focusedRow returns the row under the cursor, header or card.
func (m *boardModel) focusedRow() (boardRow, bool) {
	rows := m.rows(m.focusedColumn())
	i := m.cursor[m.focusedColumn()]
	if i < 0 || i >= len(rows) {
		return boardRow{}, false
	}
	return rows[i], true
}

// clamp keeps every column's cursor inside its row count. Called after any refresh or fold, since
// both change how many rows a column has.
func (m *boardModel) clamp() {
	for _, col := range m.columnKeys() {
		n := len(m.rows(col))
		if n == 0 {
			m.cursor[col], m.scroll[col] = 0, 0
			continue
		}
		if m.cursor[col] >= n {
			m.cursor[col] = n - 1
		}
		if m.cursor[col] < 0 {
			m.cursor[col] = 0
		}
	}
}

// restoreFocus puts the cursor back on the card it was on before a refresh, wherever that card
// ended up — including in a different column, which is exactly what happens when work moves. A
// board that reloads under your hands and leaves the cursor on a row INDEX is how you accept the
// wrong card; the id is the thing worth keeping.
func (m *boardModel) restoreFocus() {
	if m.data == nil || (m.focusID == "" && m.focusChainID == "") {
		m.clamp()
		return
	}
	for ci, col := range m.columnKeys() {
		for ri, r := range m.rows(col) {
			hit := (m.focusID != "" && r.Card != nil && r.Card.ID == m.focusID) ||
				(m.focusChainID != "" && r.header() && r.ChainID == m.focusChainID)
			if hit {
				m.col, m.cursor[col] = ci, ri
				m.clamp()
				return
			}
		}
	}
	// It left the board (discarded, archived, or a chain that finished). Keep the column, clamp the
	// cursor, and forget — holding a dead id would keep yanking the cursor at every poll.
	m.focusID, m.focusChainID = "", ""
	m.clamp()
}

// rememberFocus records what the cursor is on — a card, or a chain header — so the next refresh can
// put it back. Both branches CLEAR the other, so a stale id can never win.
func (m *boardModel) rememberFocus() {
	r, ok := m.focusedRow()
	if !ok {
		return
	}
	if r.header() {
		m.focusID, m.focusChainID = "", r.ChainID
		return
	}
	m.focusID, m.focusChainID = r.Card.ID, ""
}

// moveCursor moves within the focused column and returns whether anything moved.
func (m *boardModel) moveCursor(d int) bool {
	col := m.focusedColumn()
	rows := m.rows(col)
	if len(rows) == 0 {
		return false
	}
	next := m.cursor[col] + d
	if next < 0 {
		next = 0
	}
	if next >= len(rows) {
		next = len(rows) - 1
	}
	moved := next != m.cursor[col]
	m.cursor[col] = next
	m.rememberFocus()
	return moved
}

// moveColumn steps to the next column that HAS cards, so tabbing across an empty board does not
// strand the cursor in a column with nothing in it. If no column has cards it just steps one.
func (m *boardModel) moveColumn(d int) {
	keys := m.columnKeys()
	n := len(keys)
	for step := 1; step <= n; step++ {
		i := ((m.col+d*step)%n + n) % n
		if len(m.rows(keys[i])) > 0 {
			m.col = i
			m.rememberFocus()
			return
		}
	}
	m.col = ((m.col+d)%n + n) % n
	m.rememberFocus()
}

// visibleColumns decides how much of the board fits, and which slice of it to show.
//
// Three tiers rather than the web's two, because a terminal is not a phone: a wide terminal shows
// the whole board, a narrow one shows a single column with the others as a tab strip, and the
// common middle — a half-screen terminal — shows as many whole columns as fit, scrolled to keep
// the focused one on screen. Partial columns are never drawn: half a card is worse than a card you
// have to press → to reach.
func visibleColumns(width, focus, total int) (start, count int) {
	if total <= 0 {
		return 0, 0
	}
	fit := (width + 1) / (minColWidth + 1) // +1 for the gutter between columns
	if fit < 1 {
		fit = 1
	}
	if fit >= total {
		return 0, total
	}
	start = focus - fit/2
	if start < 0 {
		start = 0
	}
	if start+fit > total {
		start = total - fit
	}
	return start, fit
}

// cardTitle is what a tile leads with: the task, falling back to the project label when a card has
// no task text yet (an unscheduled item mid-creation).
func cardTitle(c api.BoardCard) string {
	if t := strings.TrimSpace(c.Task); t != "" {
		return t
	}
	if t := strings.TrimSpace(c.Title); t != "" {
		return t
	}
	return "(untitled)"
}

// cardState is the one-word state a tile advertises, and the single place that judgement lives.
//
// The order of these tests is the whole point. A card can be several of these at once — stalled AND
// chain-waiting, failing AND holding a PR — and the one that reaches the operator has to be the one
// that decides what they do next. "Waiting on chain" outranks "stalled" because a chain-waiting run
// is idle BY DESIGN and telling someone it is stuck sends them to fix nothing.
func cardState(c api.BoardCard) (label string, urgent bool) {
	switch {
	case c.Foreign:
		// A foreign card has no run behind it, so none of the reasoning below applies: the source
		// says what state its own item is in, and `urgent` is whatever it declared.
		if s := strings.TrimSpace(c.StateLabel); s != "" {
			return s, c.Attention
		}
		return "—", c.Attention
	case c.Unscheduled:
		return "planned", false
	case c.ChainWaiting:
		return "waiting on chain", false
	case c.ConcurrencyWaiting:
		return "waiting for a slot", false
	case c.MachineLocked:
		return "machine needs update", true
	case c.Stalled:
		return "stalled", true
	case c.Status == "failed":
		return "failed", true
	case c.Status == "needs_approval":
		return "needs you", true
	case c.Attention:
		return "finished with findings", true
	case c.Reviewing:
		return "reviewing", false
	case c.ReviewWaiting:
		return "review queued", false
	case c.Status == "paused":
		return "paused", false
	case c.Status == "running" && !c.Started:
		return "starting…", false
	case c.Status == "running":
		return "building", false
	case c.Status == "accepted":
		return "dispatched", false
	case c.Status == "queued":
		return "queued", false
	case c.Status == "done":
		return "ready to accept", false
	}
	return c.Status, false
}

// countUrgent is how many cards on the whole board are asking for a human — the number the header
// leads with, and the one that decides whether the board is worth looking at right now.
func countUrgent(b *boardData) int {
	if b == nil {
		return 0
	}
	n := 0
	for _, col := range b.Keys() {
		for _, c := range b.Column(col) {
			if _, urgent := cardState(c); urgent {
				n++
			}
		}
	}
	return n
}
