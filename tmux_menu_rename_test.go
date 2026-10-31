package main

import (
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// r on a session opens the launcher's rename box; ⏎ saves the typed name (the row shows it at
// once), esc cancels; on Commands, r is still the relay's hotkey.
func TestMenuRenameSession(t *testing.T) {
	m, c := testMenu(t, "%1")
	var renamed []string
	m.act.rename = func(it tmuxMenuItem, name string) error {
		renamed = append(renamed, it.paneID+"="+name)
		return nil
	}
	m, _ = menuPress(m, "r")
	view := gateANSI.ReplaceAllString(m.View().Content, "")
	if !m.renaming || !strings.Contains(view, "rename session") || !strings.Contains(view, "was: ACR POS") {
		t.Fatalf("r did not open the rename box:\n%s", view)
	}
	nm, _ := m.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	m = nm.(menuModel)
	for _, r := range "Point of sale" {
		nm, _ = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = nm.(menuModel)
	}
	m, _ = menuPress(m, "enter")
	if m.renaming || len(renamed) != 1 || renamed[0] != "%1=Point of sale" || m.items[m.sel].label != "Point of sale" {
		t.Fatalf("renamed %v, row %q", renamed, m.items[m.sel].label)
	}
	m, _ = menuPress(m, "r")
	m, _ = menuPress(m, "esc")
	if m.renaming || len(renamed) != 1 {
		t.Error("esc saved a rename")
	}
	if len(c.picked) != 0 {
		t.Errorf("renaming picked a row: %v", c.picked)
	}
	// On Commands, r runs the relay's row.
	m, _ = menuPress(m, "tab")
	_, _ = menuPress(m, "r")
	nm2, _ := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if m2 := nm2.(menuModel); m2.renaming {
		t.Error("r renamed on the Commands tab")
	}
}

// The saved name is the launcher's: the session's sidecar entry, which every later label reads.
func TestRenameMenuSessionSavesTheLaunchersName(t *testing.T) {
	log := fixtureFake(t)
	if err := renameMenuSession(tmuxMenuItem{paneID: "%3", sessKey: "sess-1"}, "Xero receipts"); err != nil {
		t.Fatal(err)
	}
	if got := loadLLMMeta()["sess-1"].Name; got != "Xero receipts" {
		t.Errorf("saved name %q", got)
	}
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "rename-window -t %3 Xero receipts") && !strings.Contains(string(b), "@ptln_spec") {
		t.Errorf("the live pane was not relabelled:\n%s", b)
	}
	_ = renameMenuSession(tmuxMenuItem{paneID: "%3", sessKey: "sess-1"}, "")
	if got := loadLLMMeta()["sess-1"].Name; got != "" {
		t.Errorf("an empty name did not clear: %q", got)
	}
}
