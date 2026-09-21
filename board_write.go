package main

// board_write.go — managing the board FROM the board: move a card, edit its title, comment on it.
//
// Until now the terminal board could read everything and act only on partyline's own runs (the
// `a` menu). Managing the backlog — dragging a card to another column, renaming it, leaving a
// note — meant the web app for partyline work and the tracker's own UI for a foreign board. This
// file gives all three gestures to the board itself:
//
//	m   grab the card and carry it with the arrows (board_carry.go)
//	e   edit its title
//	c   add a comment
//
// A PARTYLINE card's legal moves are its existing transitions — moveDestination below maps each
// `a`-menu action to the column it lands in, and the carry only drops where one of them reaches.
// Nothing new is invented server-side, so a move can never do something the action menu could
// not — and every guard those transitions carry (confirms on destructive ones, the review gate
// on Accept) still applies, because the drop simply runs the action.
//
// A FOREIGN card's moves go through the SOURCE, when it implements the optional write interfaces
// below. Writes use the provider's own MCP tools under the provider's own credentials, so the
// tracker's permission model still decides — partyline adds a gesture, not an authority. Sources
// that implement nothing stay read-only with an honest message, which keeps the previous
// "read-only by design" posture as the DEFAULT rather than the only option.

import (
	"fmt"
	"strings"

	"partyline.sh/partyline/internal/api"
)

// The optional write capabilities a board source may implement. Separate interfaces, because a
// tracker can sensibly support any subset (a CI board might take comments but never moves).
type boardMover interface {
	// MoveCard puts the card in another of the board's own columns. `to` is a column key from the
	// SAME loaded board — the picker only ever offers those, so a source never sees a column it
	// did not itself declare.
	MoveCard(scope, cardID string, to api.BoardColumn) error
}

type boardCardEditor interface {
	// EditTitle replaces the card's title. Title-only on purpose: descriptions are documents, and
	// replacing a document through a one-line prompt destroys everything the line did not carry.
	EditTitle(scope, cardID, title string) error
}

type boardCommenter interface {
	// CommentCard appends a comment to the card's thread. Append-only — the safe write.
	CommentCard(scope, cardID, body string) error
}

// moveDestination maps a partyline action to the column it lands the card in. Actions that take
// the card OFF the board (discard, delete) are not moves and return false — `m` moves cards
// between columns; removing one is the `a` menu's job, behind its own confirm.
func moveDestination(key string) (api.BoardColumn, bool) {
	switch key {
	case "start", "continue", "retry", "resume", "restart", "promote":
		return api.ColBuilding, true
	case "requeue":
		return api.ColBacklog, true
	case "accept":
		return api.ColAccepted, true
	}
	return "", false
}

// partylineMoves is the pure core for partyline cards: the subset of the card's actions that
// change its column, paired with where each one lands.
func partylineMoves(card api.BoardCard) []pickerItem {
	var out []pickerItem
	for _, act := range boardActions(card) {
		dest, ok := moveDestination(act.Key)
		if !ok || dest == card.Column {
			continue
		}
		out = append(out, pickerItem{
			Label: "→ " + dest.Title() + " — " + act.Label,
			Note:  act.Hint,
			Value: act.Key,
		})
	}
	return out
}

// editCard is the `e` key: retitle the focused card in place.
func (m *boardModel) editCard(c *api.Client) bool {
	card, ok := m.focused()
	if !ok {
		return false
	}

	if card.Foreign {
		src := m.sources[m.src]
		editor, can := src.(boardCardEditor)
		if !can {
			m.setToast(m.data.Source+" boards are read-only here — no edit tool is configured", false)
			return false
		}
		scope, id := m.scope, card.ID
		m.openOverlay(&inputOverlay{
			prompt: "title",
			value:  card.Task,
			hint:   "replaces the title in " + m.data.Source + " · descriptions stay in the tracker (a one-line prompt would flatten them)",
			onDone: func(m *boardModel, c *api.Client, value string) bool {
				if err := editor.EditTitle(scope, id, value); err != nil {
					m.setToast("edit failed: "+err.Error(), true)
					return false
				}
				m.setToast("title updated in "+m.data.Source, false)
				return true
			},
		})
		return false
	}

	// A partyline card's editable identity is its WORK ITEM. A run without one (dispatched
	// directly, no plan behind it) has nothing to retitle — its label is the project.
	if card.ItemID == "" {
		m.setToast("this card has no work item to edit — its title is the project label", false)
		return false
	}
	itemID := card.ItemID
	m.openOverlay(&inputOverlay{
		prompt: "title",
		value:  card.Task,
		hint:   "renames the work item everywhere it appears",
		onDone: func(m *boardModel, c *api.Client, value string) bool {
			if err := c.UpdateWorkItemTitle(itemID, value); err != nil {
				m.setToast("edit failed: "+err.Error(), true)
				return false
			}
			m.setToast("title updated", false)
			return true
		},
	})
	return false
}

// commentCard is the `c` key: append a note to the focused card's thread.
func (m *boardModel) commentCard(c *api.Client) bool {
	card, ok := m.focused()
	if !ok {
		return false
	}
	if !card.Foreign {
		// partyline discussion lives in the item's context thread, which already has first-class
		// doors (the session's remember, the detail pane). A second comment store would split it.
		m.setToast("partyline cards discuss in their context thread — `d` shows it", false)
		return false
	}
	src := m.sources[m.src]
	commenter, can := src.(boardCommenter)
	if !can {
		m.setToast(m.data.Source+" boards are read-only here — no comment tool is configured", false)
		return false
	}
	scope, id := m.scope, card.ID
	m.openOverlay(&inputOverlay{
		prompt: "comment",
		hint:   "posts an internal note to " + m.data.Source + " — staff-visible, nobody is emailed",
		onDone: func(m *boardModel, c *api.Client, value string) bool {
			if err := commenter.CommentCard(scope, id, value); err != nil {
				m.setToast("comment failed: "+err.Error(), true)
				return false
			}
			m.setToast("note posted to "+m.data.Source, false)
			return true // refresh: the chatter in the detail pane picks it up
		},
	})
	return false
}

// requireTaskScope is the shared guard for Odoo writes: the projects OVERVIEW is a board of
// containers, and the write tools below operate on tasks. Saying which board to use beats a
// server error about a model mismatch.
func requireTaskScope(scope string) error {
	if strings.TrimSpace(scope) == "" {
		return fmt.Errorf("the projects overview is read-only — open a project (⏎) to manage its tasks")
	}
	return nil
}
