package main

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// Moving through a list longer than the popup scrolls with the selection, both ways.
func TestGateListScrollFollowsSelection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var runs []*loopRun
	for i := 0; i < 20; i++ {
		runs = append(runs, waitingRun(t, loopAwaiting))
	}
	m, _ := fakeGate(t, runs, nil)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 96, Height: 14})
	m = nm.(gateModel)
	m.list = true
	for i := 0; i < len(m.runs)-1; i++ {
		m = gateKeys(m, "j")
		if id := m.runs[m.sel].ID; !strings.Contains(plain(m), id) {
			t.Fatalf("down to row %d: %s is off screen", m.sel, id)
		}
	}
	if m.scroll == 0 {
		t.Fatal("the list never scrolled")
	}
	for m.sel > 0 {
		m = gateKeys(m, "k")
		if id := m.runs[m.sel].ID; !strings.Contains(plain(m), id) {
			t.Fatalf("up to row %d: %s is off screen", m.sel, id)
		}
	}
	if m.scroll != 0 {
		t.Errorf("back at the first row, scroll %d", m.scroll)
	}
}

// A refresh keeps the list where the selection scrolled it.
func TestGateListRefreshKeepsScroll(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var runs []*loopRun
	for i := 0; i < 20; i++ {
		runs = append(runs, waitingRun(t, loopAwaiting))
	}
	m, _ := fakeGate(t, runs, nil)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 96, Height: 14})
	m = nm.(gateModel)
	m.list = true
	for i := 0; i < 15; i++ {
		m = gateKeys(m, "j")
	}
	nm, _ = m.Update(gateTick{})
	m = nm.(gateModel)
	if id := m.runs[m.sel].ID; m.sel != 15 || !strings.Contains(plain(m), id) {
		t.Errorf("after a refresh row %d is selected and %s visible: %v", m.sel, id, strings.Contains(plain(m), id))
	}
}

// Closing snoozes what the popup showed, not an item that arrived after its last refresh.
func TestGateCloseSnoozesWhatWasShown(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	shown, late := waitingRun(t, loopAwaiting), waitingRun(t, loopAwaiting)
	m, _ := fakeGate(t, []*loopRun{shown}, nil)
	m.act.list = func() []*loopRun { return []*loopRun{shown, late} }
	m.list = true
	gateKeys(m, "q")
	now := time.Now()
	if !gateSnoozed(waitingSig([]*loopRun{shown}), now) || gateSnoozed(waitingSig([]*loopRun{shown, late}), now) {
		t.Error("the snooze covers an item the popup never showed")
	}
}
