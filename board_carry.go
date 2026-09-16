package main

import "partyline.sh/partyline/internal/api"

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
		switch b[2] {
		case 'A':
			m.moveCursor(-1)
		case 'B':
			m.moveCursor(1)
		case 'C':
			m.carryMoveColumn(1)
		case 'D':
			m.carryMoveColumn(-1)
		}
		m.carryHint()
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
		m.moveCursor(-1)
		m.carryHint()
	case 'j':
		m.moveCursor(1)
		m.carryHint()
	case 'h':
		m.carryMoveColumn(-1)
		m.carryHint()
	case 'l':
		m.carryMoveColumn(1)
		m.carryHint()
	}
	return false, false
}

// carryMoveColumn is moveColumn constrained to the columns this card may land in — carrying a
// card into a column it cannot drop into is an invitation to a refusal two keys later.
func (m *boardModel) carryMoveColumn(d int) {
	keys := m.columnKeys()
	n := len(keys)
	for step := 1; step <= n; step++ {
		i := ((m.col+d*step)%n + n) % n
		if m.carry != nil && m.carry.legal[keys[i]] {
			m.col = i
			m.rememberFocus()
			return
		}
	}
}

// carryHint keeps the status line saying what is in hand and what the keys do — handleKey clears
// the toast on every key, so the hint is re-stated after each one.
func (m *boardModel) carryHint() {
	if m.carry == nil {
		return
	}
	m.setToast("carrying "+clipVis(cardTitle(m.carry.card), 32)+" — ↑↓ position · ←→ column · ⏎ drop · esc put back", false)
}

// carryCommit performs the drop at the cursor.
func (m *boardModel) carryCommit(c *api.Client) bool {
	carry := m.carry
	m.carry = nil
	if carry == nil {
		return false
	}
	card := carry.card
	target := m.focusedColumn()

	// Across columns: the transition the landing column stands for.
	if target != carry.from {
		if !carry.legal[target] {
			m.setToast("that column is not a legal landing for this card", false)
			return false
		}
		if card.Foreign {
			mover := m.sources[m.src].(boardMover) // presence proven at grab
			if err := mover.MoveCard(m.scope, card.ID, target); err != nil {
				m.setToast("move failed: "+err.Error(), true)
				return false
			}
			m.setToast("moved — "+m.data.Source+" has it", false)
			return true
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
	row, ok := m.focusedRow()
	if !ok || row.Card == nil {
		m.setToast("drop on a card, not a chain header", false)
		return false
	}
	rank, ok := backlogDropRank(sortColumn(api.ColBacklog, m.data.Column(api.ColBacklog)), card.ID, row.Card.ID)
	if !ok {
		m.setToast("put back — nothing changed", false) // dropped on itself
		return false
	}
	if err := c.SetRunRank(card.ID, rank); err != nil {
		m.setToast("could not reorder: "+err.Error(), true)
		return false
	}
	m.focusID = card.ID // follow the card to where it landed
	m.setToast("reordered", false)
	return true
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
