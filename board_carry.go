package main

import (
	"time"

	"partyline.sh/partyline/internal/api"
)

// board_carry.go — pick a card UP and carry it: `m` grabs, the arrows position it, ⏎ commits,
// esc puts it back untouched.
//
// This replaces the v0.105.4 move PICKER with the gesture the operator actually asked for: the
// same arrows that navigate the board carry the card while it is grabbed, so moving work feels
// like moving the cursor rather than filling in a form. The CURSOR is the drop target — grab,
// arrow to where the card should live, drop. Nothing is written until ⏎; esc costs nothing.
//
// What a drop MEANS depends on where it lands, and the rules are the board's existing ones:
//
//	within Backlog       reordering — priority is a decision, and Backlog is the one column
//	                     whose order IS that decision. The drop takes the rank that puts the
//	                     card above the highlighted one (below it, when it is the last).
//	across columns       a transition. A partyline card may only be carried into columns one of
//	                     its `a`-menu actions can reach — ←/→ skip the rest — and the drop RUNS
//	                     that action, confirms and gates intact. A foreign (Odoo) card may be
//	                     carried to any stage; the drop writes through the source's own tool.
//	other columns' order Building/Review/Accepted order follows what happened; Odoo task order
//	                     belongs to Odoo (priority + recency — the curated tool exposes no
//	                     sequence). ↑/↓ still navigate; the drop just ignores the row.
type carryState struct {
	card  api.BoardCard
	from  api.BoardColumn
	legal map[api.BoardColumn]bool // columns ⏎ may commit into; ←/→ skip everything else
}

// grabCard is the `m` key: validate the card can move at all, work out where it may go, and
// enter carry mode. Refusals name the reason — a grab that silently does nothing teaches people
// the key is broken.
func (m *boardModel) grabCard() bool {
	card, ok := m.focused()
	if !ok {
		return false
	}

	legal := map[api.BoardColumn]bool{card.Column: true} // home is always a legal landing
	if card.Foreign {
		src := m.sources[m.src]
		if _, can := src.(boardMover); !can {
			m.setToast(m.data.Source+" boards are read-only here — no move tool is configured", false)
			return false
		}
		if err := requireTaskScope(m.scope); err != nil {
			m.setToast(err.Error(), false)
			return false
		}
		for _, col := range m.data.Columns {
			legal[col.Key] = true
		}
	} else {
		for _, act := range boardActions(*card) {
			if dest, ok := moveDestination(act.Key); ok {
				legal[dest] = true
			}
		}
		// A card with no destination and no orderable home has nothing a grab can do. Backlog
		// cards are always grabbable — reordering is a move even when no column change is.
		if len(legal) == 1 && card.Column != api.ColBacklog {
			m.setToast("this card has nowhere to move right now — `a` shows everything it can do", false)
			return false
		}
	}

	m.carry = &carryState{card: *card, from: card.Column, legal: legal}
	m.carryHint()
	return false
}

// carryKey owns every key while a card is grabbed. Arrows position, ⏎ commits, esc (or q, or a
// second m) cancels — and nothing else does anything, because a stray keystroke mid-carry must
// not fire an action underneath the thing you are holding.
func (m *boardModel) carryKey(b []byte, c *api.Client) (quit, refresh bool) {
	if len(b) >= 3 && b[0] == 0x1b && b[1] == '[' {
		moved := false
		switch b[2] {
		case 'A':
			moved = m.moveCursor(-1)
		case 'B':
			moved = m.moveCursor(1)
		case 'C':
			moved = m.carryMoveColumn(1)
		case 'D':
			moved = m.carryMoveColumn(-1)
		}
		m.carryHint()
		if moved {
			m.carryHop()
		}
		return false, false
	}
	switch b[0] {
	case '\r', '\n':
		return false, m.carryCommit(c)
	case 0x1b, 'q', 'm': // esc / q / m again: put it back
		m.carry = nil
		m.setToast("put back — nothing changed", false)
		return false, false
	case 'k':
		if m.moveCursor(-1) {
			m.carryHop()
		}
		m.carryHint()
	case 'j':
		if m.moveCursor(1) {
			m.carryHop()
		}
		m.carryHint()
	case 'h':
		if m.carryMoveColumn(-1) {
			m.carryHop()
		}
		m.carryHint()
	case 'l':
		if m.carryMoveColumn(1) {
			m.carryHop()
		}
		m.carryHint()
	}
	return false, false
}

// carryMoveColumn is moveColumn constrained to the columns this card may land in — carrying a
// card into a column it cannot drop into is an invitation to a refusal two keys later.
func (m *boardModel) carryMoveColumn(d int) bool {
	keys := m.columnKeys()
	n := len(keys)
	for step := 1; step <= n; step++ {
		i := ((m.col+d*step)%n + n) % n
		if m.carry != nil && m.carry.legal[keys[i]] {
			if i == m.col {
				return false
			}
			m.col = i
			// land the card no lower than the column's end — rows() inserts it at the cursor
			if rows := m.rows(keys[i]); m.cursor[keys[i]] > len(rows)-1 {
				m.cursor[keys[i]] = max(0, len(rows)-1)
			}
			m.rememberFocus()
			return true
		}
	}
	return false
}

// carryHop is the movement animation: the carried tile re-draws at its new spot in two quick
// partial frames before the full one, so the card visibly travels rather than teleporting.
// Held-arrow repeats skip the frames — at key-repeat speed the motion IS the animation.
func (m *boardModel) carryHop() {
	now := time.Now()
	repeat := now.Sub(m.carryLast) < 120*time.Millisecond
	m.carryLast = now
	if repeat {
		return
	}
	for _, reveal := range []int{1, 3} {
		m.carryReveal = reveal
		m.render()
		time.Sleep(24 * time.Millisecond)
	}
	m.carryReveal = 0
}

// carryHint keeps the status line saying what is in hand and what the keys do — handleKey clears
// the toast on every key, so the hint is re-stated after each one.
func (m *boardModel) carryHint() {
	if m.carry == nil {
		return
	}
	m.setToast("✥ "+clipVis(cardTitle(m.carry.card), 32)+" — the card follows the arrows · ⏎ drop · esc put back", false)
}

// carryCommit performs the drop at the cursor. The screen has been showing the card at the
// cursor all along (rows() renders the carry there), so the commit's only job is making the
// world match the preview — locally FIRST, so the drop lands the instant ⏎ goes down, with
// the source write running behind it. The write's failure arrives as an event and the reload
// puts the truth back; optimism here never survives a refusal.
func (m *boardModel) carryCommit(c *api.Client) bool {
	carry := m.carry
	if carry == nil {
		m.carry = nil
		return false
	}
	card := carry.card
	target := m.focusedColumn()

	// The row BELOW the carried card in the preview — the drop ranks above it. Found while
	// the carry still renders (rows() is carry-aware), before the state is cleared.
	var below *api.BoardCard
	rows := m.rows(target)
	for j := m.cursor[target] + 1; j < len(rows); j++ {
		if rows[j].Card != nil {
			below = rows[j].Card
			break
		}
	}
	m.carry = nil

	// Across columns: the transition the landing column stands for.
	if target != carry.from {
		if !carry.legal[target] {
			m.setToast("that column is not a legal landing for this card", false)
			return false
		}
		if card.Foreign {
			mover := m.sources[m.src].(boardMover) // presence proven at grab
			m.applyLocalColumnMove(card.ID, carry.from, target)
			m.focusID = card.ID
			scope := m.scope
			m.asyncWrite("moved — "+m.data.Source+" has it", func() error {
				return mover.MoveCard(scope, card.ID, target)
			})
			return false // the write reports as an event; the board already shows the move
		}
		for _, act := range boardActions(card) {
			if dest, ok := moveDestination(act.Key); ok && dest == target {
				return m.runAction(c, card, act) // confirms and gates apply exactly as in `a`
			}
		}
		m.setToast("no transition reaches that column for this card", false)
		return false
	}

	// Within the column: only Backlog order means anything, and only for partyline cards.
	if card.Foreign {
		m.setToast(m.data.Source+" owns its task order — stages move, rows don't", false)
		return false
	}
	if target != api.ColBacklog {
		m.setToast("only the Backlog is ordered — the other columns follow what happened", false)
		return false
	}
	sorted := sortColumn(api.ColBacklog, m.data.Column(api.ColBacklog))
	var rank float64
	ok := false
	if below != nil {
		rank, ok = backlogDropRank(sorted, card.ID, below.ID)
	} else {
		// nothing under it: the true bottom — below the last card that is not itself
		for i := len(sorted) - 1; i >= 0; i-- {
			if sorted[i].ID != card.ID {
				rank, ok = sorted[i].Rank-1, true
				break
			}
		}
	}
	if !ok {
		m.setToast("put back — nothing changed", false)
		return false
	}
	m.applyLocalRank(card.ID, rank)
	m.focusID = card.ID // follow the card to where it landed
	write := m.rankWrite
	if write == nil {
		write = c.SetRunRank
	}
	m.asyncWrite("reordered", func() error { return write(card.ID, rank) })
	return false
}

// asyncWrite runs one source write off the loop and reports it like an action: the label (or
// the error) lands on the status line, and the follow-up reload reconciles the optimistic
// local state with the source's answer — including reverting it when the write was refused.
func (m *boardModel) asyncWrite(label string, fn func() error) {
	events, stop := m.events, m.stop
	if events == nil { // tests without a loop: do it inline
		if err := fn(); err != nil {
			m.setToast(label+" failed: "+err.Error(), true)
		}
		return
	}
	go func() {
		err := fn()
		select {
		case events <- boardEvent{actionLabel: label, actionErr: err}:
		case <-stop:
		}
	}()
}

// applyLocalColumnMove makes the board show a cross-column drop before the source confirms it.
func (m *boardModel) applyLocalColumnMove(id string, from, to api.BoardColumn) {
	if m.data == nil {
		return
	}
	src := m.data.ByColumn[from]
	for i := range src {
		if src[i].ID == id {
			card := src[i]
			card.Column = to
			m.data.ByColumn[from] = append(append([]api.BoardCard(nil), src[:i]...), src[i+1:]...)
			m.data.ByColumn[to] = append(append([]api.BoardCard(nil), m.data.ByColumn[to]...), card)
			return
		}
	}
}

// applyLocalRank makes a Backlog reorder visible before the write returns.
func (m *boardModel) applyLocalRank(id string, rank float64) {
	if m.data == nil {
		return
	}
	col := m.data.ByColumn[api.ColBacklog]
	for i := range col {
		if col[i].ID == id {
			col[i].Rank = rank
			return
		}
	}
}

// backlogDropRank is the pure core of a reorder-by-drop: given the column's cards in display
// order (rank strictly descending), the carried card, and the card the cursor is on, the rank
// that lands the carried card ABOVE the target — or BELOW it when the target is the column's
// last card, because "the very bottom" is otherwise unreachable by a drop-above rule.
func backlogDropRank(cards []api.BoardCard, selfID, targetID string) (float64, bool) {
	if selfID == targetID {
		return 0, false
	}
	var rest []api.BoardCard
	for _, c := range cards {
		if c.ID != selfID {
			rest = append(rest, c)
		}
	}
	at := -1
	for i := range rest {
		if rest[i].ID == targetID {
			at = i
			break
		}
	}
	if at < 0 || len(rest) == 0 {
		return 0, false
	}
	switch {
	case at == len(rest)-1:
		return rest[at].Rank - 1, true // the last card: drop BELOW it — the true bottom
	case at == 0:
		return rest[0].Rank + 1, true // the top
	default:
		return (rest[at-1].Rank + rest[at].Rank) / 2, true
	}
}
