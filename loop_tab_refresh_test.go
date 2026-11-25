package main

import (
	"strings"
	"testing"
	"time"
)

// Two planned steps with the same title stay told apart across a refresh.
func TestBoardRefreshKeepsAPlannedStepSelected(t *testing.T) {
	steps := func(time.Time) []tabRow {
		return []tabRow{
			{Section: tabPlanned, Title: "tidy up", Batch: "step 2 of 3", Detail: "batch 4cf83d"},
			{Section: tabPlanned, Title: "tidy up", Batch: "step 3 of 3", Detail: "batch 4cf83d"},
		}
	}
	m := newTabModel(tabActions{rows: steps, detail: func(tabRow) string { return "" }, now: time.Now})
	m.col = tabPlanned
	m.selectCard()
	nm, _ := m.Update(tabTick{})
	if cur, _ := nm.(tabModel).current(); cur.Batch != "step 2 of 3" {
		t.Errorf("after refresh %q is selected, want step 2 of 3", cur.Batch)
	}
}

// Open details follow the run as it moves on.
func TestBoardRefreshUpdatesOpenDetails(t *testing.T) {
	m, _ := fakeTab(t)
	text := "round 1"
	m.act.detail = func(tabRow) string { return text }
	m = tabKey(m, "enter")
	text = "round 2"
	nm, _ := m.Update(tabTick{})
	if got := nm.(tabModel).vp.View(); !strings.Contains(got, "round 2") {
		t.Errorf("details after refresh: %q", got)
	}
}
