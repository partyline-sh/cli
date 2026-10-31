package main

import (
	"strings"
	"testing"
	"time"
)

// Each state has its own marker; working-out-of-sight states spin.
func TestBoardIndicatorGlyphs(t *testing.T) {
	th := tmuxTheme()
	for _, tc := range []struct {
		row  tabRow
		want string
	}{
		{tabRow{Section: tabNeedsYou, State: loopAwaiting}, "‼"},
		{tabRow{Section: tabInReview, State: loopChecking}, tabSpinner[3]},
		{tabRow{Section: tabInReview, State: loopBaseline}, tabSpinner[3]},
		{tabRow{Section: tabInProgress, State: loopBuilding, Age: time.Minute}, "/"},
		{tabRow{Section: tabInProgress, State: loopBuilding, Age: time.Hour}, "⚠"},
		{tabRow{Section: tabInProgress, State: loopRevising}, "↺"},
		{tabRow{Section: tabInReview, State: loopChecking, Reset: time.Now().Add(time.Hour)}, "⏸"},
		{tabRow{Section: tabPlanned}, "○"},
		{tabRow{Section: tabDone, State: loopPRReady}, "✓"},
		{tabRow{Section: tabDone, State: loopStopped}, "■"},
		{tabRow{Section: tabDone, State: loopFailed}, "✗"},
	} {
		if got, _ := tabIndicatorGlyph(th, tc.row, 3); got != tc.want {
			t.Errorf("%s/%s: glyph %q, want %q", tabSectionNames[tc.row.Section], tc.row.State, got, tc.want)
		}
	}
}

// A card's last line says what the run waits on.
func TestBoardIndicatorLines(t *testing.T) {
	st := gateStyle{tmuxTheme()}
	for _, tc := range []struct {
		row  tabRow
		want string
	}{
		{tabRow{Section: tabNeedsYou, State: loopAwaiting, Age: 12 * time.Minute}, "approve · waiting 12m"},
		{tabRow{Section: tabNeedsYou, State: loopAwaitYou, Dispute: "F1-3"}, "dispute F1-3"},
		{tabRow{Section: tabNeedsYou, State: loopAwaitYou, OpenP1: 3, Round: 6}, "3 P1 · round 6/6"},
		{tabRow{Section: tabInReview, State: loopChecking, Chips: []tabChip{{"checks", "done"}, {"claude", "reading"}}}, "checks✓ claude…"},
		{tabRow{Section: tabInProgress, State: loopBuilding, Round: 2, OpenP1: 2, Age: 4 * time.Minute}, "●●○○○○ 2 P1 4m"},
		{tabRow{Section: tabInProgress, State: loopBuilding, Round: 1, Age: 45 * time.Minute}, "quiet 45m"},
		{tabRow{Section: tabInReview, State: loopChecking, Reset: time.Date(2026, 1, 1, 14, 30, 0, 0, time.Local)}, "resumes 14:30"},
		{tabRow{Section: tabDone, State: loopStopped, Age: time.Hour}, "stopped · 1h"},
	} {
		got := gateANSI.ReplaceAllString(tabStateLine(st, tc.row, 60), "")
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s/%s: line %q, want it to contain %q", tabSectionNames[tc.row.Section], tc.row.State, got, tc.want)
		}
	}
	if got := gateANSI.ReplaceAllString(tabPips(st, 9), ""); got != "●●●●●●" {
		t.Errorf("pips past the last round: %q", got)
	}
}

// The spinner turns on its own tick; the rows are not re-read for it.
func TestBoardSpinnerTicks(t *testing.T) {
	m, _ := fakeTab(t)
	reads := 0
	m.act.rows = func(time.Time) []tabRow { reads++; return sampleTabRows(time.Now()) }
	nm, cmd := m.Update(tabSpin{})
	if nm.(tabModel).frame != m.frame+1 || cmd == nil || reads != 0 {
		t.Errorf("spin: frame %d→%d, cmd %v, rows read %d times", m.frame, nm.(tabModel).frame, cmd != nil, reads)
	}
}
