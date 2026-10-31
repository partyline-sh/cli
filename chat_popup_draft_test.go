package main

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// A paste and text back from the editor are kept as the draft at once, like a typed key.
func TestChatPopupSavesPastedAndEditedDrafts(t *testing.T) {
	m, _ := testChatPopup(t)
	var saved string
	m.saveTail = func(d string) { saved = d }
	nm, _ := m.Update(tea.PasteMsg{Content: "pasted"})
	if saved != "pasted" {
		t.Errorf("after a paste the draft is %q", saved)
	}
	nm, _ = nm.(chatPopup).Update(chatEdited{text: "edited\n"})
	if saved != "edited" {
		t.Errorf("after the editor the draft is %q", saved)
	}
}
