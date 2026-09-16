package main

import (
	"strings"

	"partyline.sh/partyline/internal/api"
)

// board_import.go — the one thing you can DO to a foreign card.
//
// Seeing an Odoo task rendered in a terminal is mildly useful; Odoo already has a UI and it is
// better at being Odoo than we are. What Odoo cannot do is turn that task into specified,
// agent-buildable work. That is the whole point of showing foreign boards here, and it is why this
// is the only write in the provider design: everything else stays read-only.
//
// It reuses the import path that already exists rather than inventing a second door. partyline still
// never talks to the tracker — the card was already read by the provider, and what travels is text.

// importForeign turns the focused foreign card into a partyline planning draft.
func (m *boardModel) importForeign(c *api.Client) bool {
	card, ok := m.focused()
	if !ok {
		return false
	}
	if !card.Foreign {
		m.setToast("this card is already partyline work", false)
		return false
	}

	thread := m.boardThread(c)
	if thread == "" {
		m.openOverlay(&noticeOverlay{heading: "no context thread here", body: wrapPlain(
			"Importing files the item against this repo's context thread, and this directory is not "+
				"set up as a partyline project.\n\nRun `ptln project setup` in the repo the work belongs to.", 70)})
		return false
	}

	source := m.activeSource()
	tool := ""
	if source != nil {
		tool = source.Name()
	}
	title := strings.TrimSpace(cardTitle(*card))

	m.openOverlay(&confirmOverlay{
		prompt: "Import \"" + clipVis(title, 60) + "\" from " + tool + " into partyline?\n\n" +
			"It arrives as a PLANNED item, not a running one — the readiness gate still applies " +
			"before anything is dispatched. Nothing is written back to " + tool + ".",
		onYes: func(m *boardModel, c *api.Client) bool {
			return m.doImport(c, *card, thread, tool)
		},
	})
	return false
}

// doImport files the item and stamps it with where it came from.
//
// The SOURCE STAMP is what makes a re-import an update rather than a duplicate: work_items has a
// unique partial index on (org_id, source_tool, source_id), so importing the same Odoo task twice
// converges instead of littering the backlog. Filing and stamping are two calls because only an
// imported item has a source, and widening the create contract for the minority case would make
// every other caller carry three empty fields.
func (m *boardModel) doImport(c *api.Client, card api.BoardCard, thread, tool string) bool {
	title := strings.TrimSpace(cardTitle(card))
	if title == "" {
		m.setToast("that card has no title to import", true)
		return false
	}

	// The body carries what the provider gave us and says where it came from, so whoever shapes it
	// later can go back to the original.
	var doc strings.Builder
	doc.WriteString("Imported from " + tool + ".\n\n")
	if s := strings.TrimSpace(card.Title); s != "" {
		doc.WriteString(s + "\n\n")
	}
	if s := strings.TrimSpace(card.Detail); s != "" {
		doc.WriteString(s + "\n\n")
	}
	if card.SourceURL != "" {
		doc.WriteString(card.SourceURL + "\n")
	}

	id, err := c.CreateWorkItem(thread, "task", title, "", doc.String(), 0, nil)
	if err != nil {
		m.setToast("import refused: "+err.Error(), true)
		return true
	}
	if card.ID != "" && tool != "" {
		// Best-effort: the item is filed either way, and an unstamped item is a working item that
		// merely will not de-duplicate on a second import. Failing the whole import over the stamp
		// would be the worse trade.
		if err := c.SetWorkItemSource(id, tool, card.ID, card.SourceURL); err != nil {
			m.setToast("imported, but could not link it back to "+tool+": "+err.Error(), true)
			return true
		}
	}

	m.setToast("imported into partyline Backlog as a planned item", false)
	return true
}
