package main

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// Clicks land on what the screen draws: a card selects, the selected card opens like ⏎, the heading
// switches tabs, a contractor row selects; the wheel moves like the arrows; the views ask for mouse
// reporting.
func TestLoopsMouse(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m, _ := fakeTab(t)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 38})
	m = nm.(tabModel)
	if m.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatal("the board does not ask for the mouse")
	}
	click := func(x, y int) {
		nm, _ := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
		m = nm.(tabModel)
	}
	start, count := m.visibleColumns()
	cw := max((max(m.w-4, 1)-count+1)/count, 4)
	colX := func(sec tabSection) int { return tabMouseLeftX + (int(sec)-start)*(cw+1) + 3 }

	click(colX(tabInProgress), tabMouseCardsY+2)
	if m.col != tabInProgress || m.detail {
		t.Fatalf("a click on an In progress card: col %d detail %v", m.col, m.detail)
	}
	click(colX(tabInProgress), tabMouseCardsY+2)
	if !m.detail {
		t.Fatal("a second click on the selected card did not open it")
	}
	nm, _ = m.Update(tabKeyMsg("esc"))
	m = nm.(tabModel)
	click(colX(tabDone), tabMouseCardsY+tabCardLines+2+1) // below the only Done card
	if m.col == tabDone {
		t.Fatal("a click below the last card selected something")
	}
	click(colX(tabPlanned)-(cw+1)/2-2, tabMouseCardsY+1) // the gap between two columns is not a card
	click(tabMouseLeftX+len("Board")+4, tabMouseHeadingY)
	if !m.contractors {
		t.Fatal("a click on Contractors did not switch tab")
	}
	for _, c := range []*contractor{
		{ID: "aa0000000000a1", Loop: "a1b2c3", Role: "builder", Engine: "codex", Title: "one", State: "running", Started: time.Now()},
		{ID: "aa0000000000b2", Loop: "a1b2c3", Role: "reviewer", Engine: "claude", Title: "two", State: "running", Started: time.Now().Add(-time.Minute)},
	} {
		if err := c.save(); err != nil {
			t.Fatal(err)
		}
	}
	m.refreshCrew()
	m.crewSel = 0
	if len(m.crew) != 2 {
		t.Fatalf("%d contractors loaded", len(m.crew))
	}
	second := m.crew[1].ID
	click(10, tabMouseCrewY+3) // RUNNING heading, a (2 lines), then b
	if m.crewSel != 1 || m.crew[m.crewSel].ID != second {
		t.Fatalf("a click on the second contractor selected %d", m.crewSel)
	}
	click(2, tabMouseHeadingY)
	if m.contractors {
		t.Fatal("a click on Board did not switch back")
	}
	m.detail = true
	m.vp.SetContent(longText(200))
	nm, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if m = nm.(tabModel); m.vp.YOffset() == 0 {
		t.Fatal("the wheel did not scroll the details")
	}
}

func longText(n int) string {
	s := ""
	for i := 0; i < n; i++ {
		s += "line\n"
	}
	return s
}
