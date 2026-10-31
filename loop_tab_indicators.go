package main

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	ansix "github.com/charmbracelet/x/ansi"

	uitheme "partyline.sh/partyline/internal/theme"
)

// The board's status indicators: a glyph per state (a spinner while reviewers or checks run), and
// on each card's last line what the run is waiting on — reviewer chips, round pips and open P1s,
// a limit's reset, or how long it has waited for you. Batches get a progress strip under the board.

// tabQuietAfter is how long a building run may go without an event before its card says so. A
// long think looks the same as a stall from here, so it is a note, not an alarm.
const tabQuietAfter = 30 * time.Minute

// tabSpinner is the frame sequence for runs that are working out of sight (checks, reviews).
var tabSpinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// tabSpin advances the spinner; only the drawing changes, the rows refresh on tabTick.
type tabSpin struct{}

func tabSpinCmd() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return tabSpin{} })
}

// tabChip is one reviewer or the checks, and how far it has got.
type tabChip struct {
	Name, Mark string // Mark: done, reading, limited
}

// tabIndicators fills the indicator fields of a run's row.
func tabIndicators(row *tabRow, r *loopRun, now time.Time) {
	row.Round, row.Batch = r.Round, tabBatchLabel(r)
	row.BatchID = r.Batch
	row.PR, row.CI = loopPRNumber(r.PR), cachedPRCheck(r)
	var found []loopTracked
	readLoopJSON(r.ID, fmt.Sprintf("findings-%d.json", r.Round), &found)
	for _, f := range found {
		if f.Priority == "P1" && f.Dropped == "" && f.Decision != "dismiss" {
			row.OpenP1++
		}
		if f.Dispute != "" && f.Decision == "" && row.Dispute == "" {
			row.Dispute = f.ID
		}
	}
	if evs, err := loopEvents(r.ID); err == nil {
		for i := len(evs) - 1; i >= 0; i-- {
			if evs[i].Type == "reviewers_waiting" {
				if s, ok := evs[i].Data["reset"].(string); ok {
					if t, err := time.Parse(time.RFC3339Nano, s); err == nil && t.After(now) {
						row.Reset = t
					}
				}
				break
			}
		}
	}
	if r.State == loopChecking || r.State == loopBatchRev || r.State == loopScopeRev {
		for _, part := range strings.Split(tabReviewers(r), " · ") {
			name, mark, _ := strings.Cut(part, " ")
			if name == "checks" {
				name, mark = "checks", map[string]string{"done": "done", "running": "reading"}[mark]
			}
			if name != "" {
				row.Chips = append(row.Chips, tabChip{Name: name, Mark: mark})
			}
		}
	}
	// A batch step built by a contractor: its builder, and the reset it waits for when every
	// builder is limited (the same ⏸ as a reviewer limit).
	if r.State == loopBuilding && builderDriven(r) && r.Builder != "" {
		row.Builder = r.BuilderEngine
		if reset := buildersWaitingUntil(r); !reset.IsZero() {
			row.Reset = reset
		}
	}
}

// tabIndicatorGlyph is the card's marker and its colour.
func tabIndicatorGlyph(th uitheme.Theme, r tabRow, frame int) (string, string) {
	switch {
	case !r.Reset.IsZero(), r.State == loopPaused:
		return "⏸", th.Warning
	case r.Section == tabNeedsYou:
		return uitheme.NeedsYou.Glyph(), th.Colour(uitheme.NeedsYou)
	case r.State == loopBaseline:
		return tabSpinner[frame%len(tabSpinner)], th.Muted
	case r.Section == tabInReview:
		return tabSpinner[frame%len(tabSpinner)], th.Colour(uitheme.Working)
	case r.State == loopRevising:
		return "↺", th.Colour(uitheme.Working)
	case r.Section == tabInProgress && r.Age > tabQuietAfter && r.Builder == "": // a builder reports its own quiet
		return "⚠", th.Warning
	case r.Section == tabInProgress:
		return uitheme.Working.Glyph(), th.Colour(uitheme.Working)
	case r.Section == tabPlanned:
		return "○", th.Faint
	case r.State == loopPRReady:
		return uitheme.Passed.Glyph(), th.Colour(uitheme.Passed)
	case r.State == loopStopped:
		return "■", th.Faint
	}
	return uitheme.Failed.Glyph(), th.Colour(uitheme.Failed)
}

// tabStateLine is a card's last line: what the run is doing or waiting on, styled.
func tabStateLine(st gateStyle, r tabRow, inner int) string {
	age := tabAge(r.Age)
	var s string
	switch {
	case !r.Reset.IsZero():
		s = st.fg(st.th.Warning).Render("resumes " + r.Reset.Local().Format("15:04"))
	case r.Section == tabNeedsYou && r.Dispute != "":
		s = st.text("dispute "+r.Dispute) + st.muted(" · ⏎ to decide")
	case r.Section == tabNeedsYou && r.OpenP1 > 0:
		s = st.fg(st.th.Colour(uitheme.NeedsYou)).Render(fmt.Sprintf("%d P1", r.OpenP1)) + st.muted(fmt.Sprintf(" · round %d/%d", r.Round, loopMaxRounds))
	case r.Section == tabNeedsYou:
		s = st.text("approve") + st.muted(" · waiting "+age)
	case r.State == loopBaseline:
		s = st.muted("baseline checks · " + age)
	case len(r.Chips) > 0:
		var chips []string
		for _, c := range r.Chips {
			mark, colour := "…", st.th.Colour(uitheme.Working)
			switch c.Mark {
			case "done":
				mark, colour = "✓", st.th.Colour(uitheme.Passed)
			case "limited":
				mark, colour = "⏸", st.th.Warning
			}
			chips = append(chips, st.muted(c.Name)+st.fg(colour).Render(mark))
		}
		s = strings.Join(chips, " ")
	case r.Section == tabInReview:
		s = st.muted(ansix.Truncate(termSafe(r.Detail), max(inner-6, 1), "…") + " · " + age)
	case r.State == loopRevising:
		s = st.muted("revising brief · " + age)
	case r.Section == tabInProgress:
		s = tabPips(st, r.Round) + " "
		if r.Builder != "" {
			s = st.muted(uitheme.Gear+" "+termSafe(r.Builder)) + " " + s
		}
		if r.OpenP1 > 0 {
			s += st.fg(st.th.Colour(uitheme.NeedsYou)).Render(fmt.Sprintf("%d P1", r.OpenP1)) + " "
		}
		if r.Age > tabQuietAfter && r.Builder == "" {
			s += st.fg(st.th.Warning).Render("quiet " + age)
		} else {
			s += st.muted(age)
		}
	case r.Section == tabPlanned:
		s = st.muted(r.Batch)
	case r.State == loopStopped:
		s = st.faint("stopped · " + age)
	case r.State == loopPRReady && r.PR > 0:
		s = st.text(fmt.Sprintf("#%d", r.PR)) + tabCIMark(st, r.CI) + st.muted(" · "+age)
	case r.State == loopPRReady:
		s = st.muted("pr_ready · " + age)
	default:
		s = st.fg(st.th.Colour(uitheme.Failed)).Render(ansix.Truncate(termSafe(r.Detail), max(inner-6, 1), "…")) + st.muted(" · "+age)
	}
	return ansix.Truncate(s, inner, "…")
}

// tabCIMark is a PR's CI state after its number: " checks✓", " checks✗", " checks…", or nothing
// when the watcher could not ask gh.
func tabCIMark(st gateStyle, ci string) string {
	var mark string
	var s uitheme.State
	switch ci {
	case "pass":
		mark, s = "✓", uitheme.Passed
	case "fail":
		mark, s = "✗", uitheme.Failed
	case "pending":
		mark, s = "…", uitheme.Working
	default:
		return ""
	}
	return st.muted(" checks") + st.fg(st.th.Colour(s)).Render(mark)
}

// tabPips shows the round out of the most a run gets: ●●○○○○ is round 2 of 6.
func tabPips(st gateStyle, round int) string {
	round = min(max(round, 0), loopMaxRounds)
	return st.fg(st.th.Colour(uitheme.Working)).Render(strings.Repeat("●", round)) + st.faint(strings.Repeat("○", loopMaxRounds-round))
}

// tabBatchStrips is one line per running batch under the board: a bar of steps done, the step
// running and how long the batch has been going.
func tabBatchStrips(st gateStyle, rows []tabRow, w int) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range rows {
		if r.BatchID == "" || seen[r.BatchID] || r.Section == tabDone || r.Section == tabPlanned {
			continue
		}
		seen[r.BatchID] = true
		b, err := loadBatch(r.BatchID)
		if err != nil || len(b.Briefs) == 0 {
			continue
		}
		done := max(len(b.Runs)-1, 0)
		bar := st.fg(st.th.Focus).Render(strings.Repeat("▰", done)) + st.faint(strings.Repeat("▱", len(b.Briefs)-done))
		line := fmt.Sprintf(" %s %s %d/%d  %s", st.muted("batch "+b.ID), bar, done, len(b.Briefs),
			st.muted(fmt.Sprintf("step %d %s · %s in batch", len(b.Runs), strings.ReplaceAll(r.State, "_", " "), tabAge(time.Since(b.Created)))))
		out = append(out, lipgloss.NewStyle().MaxWidth(w).Render(line))
	}
	return out
}
