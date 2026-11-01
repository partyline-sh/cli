package main

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// r cycles the board through all repositories, then each one, then all again; worktrees of one
// repository stay together, and the heading names the filter.
func TestLoopsBoardRepoFilter(t *testing.T) {
	rows := []tabRow{
		{Section: tabInProgress, RunID: "a1", Repo: "partyline (batchbar)", Project: "partyline", Title: "one"},
		{Section: tabInProgress, RunID: "a2", Repo: "partyline", Project: "partyline", Title: "two"},
		{Section: tabInProgress, RunID: "b1", Repo: "acr-cloud", Project: "acr-cloud", Title: "three"},
	}
	m := newTabModel(tabActions{rows: func(time.Time) []tabRow { return rows }, now: time.Now,
		detail: func(tabRow) string { return "" }, gateRun: func(tabRow) error { return nil }})
	ids := func() string {
		var s []string
		for _, r := range m.rows {
			s = append(s, r.RunID)
		}
		return strings.Join(s, ",")
	}
	st := gateStyle{tmuxTheme()}
	if ids() != "a1,a2,b1" || !strings.Contains(m.tabHeading(st), "all repos") {
		t.Fatalf("start: %s %q", ids(), m.tabHeading(st))
	}
	for _, want := range []string{"b1", "a1,a2", "a1,a2,b1"} {
		nm, _ := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
		m = nm.(tabModel)
		if ids() != want {
			t.Errorf("after r: %s, want %s", ids(), want)
		}
	}
	nm, _ := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	m = nm.(tabModel)
	nm, _ = m.Update(tabTick{})
	if m = nm.(tabModel); ids() != "b1" || !strings.Contains(m.tabHeading(st), "acr-cloud") {
		t.Errorf("a refresh dropped the filter: %s %q", ids(), m.tabHeading(st))
	}
	if one := (tabModel{all: rows[:2]}); strings.Contains(one.tabHeading(st), "all repos") {
		t.Error("the filter shows with only one repository")
	}
}
