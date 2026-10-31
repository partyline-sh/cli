package main

import (
	"fmt"
	"os"
	uitheme "partyline.sh/partyline/internal/theme"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The Loops board (`ptln loop tab`): every loop and batch on the machine in five columns, decisions
// first. It reads only; deciding happens in the approval popup. This file turns what is on disk
// into rows; loop_tab_view.go draws them.

type tabSection int

const (
	tabNeedsYou tabSection = iota // first: what needs you, then the work left to right as it moves
	tabPlanned
	tabInProgress
	tabInReview
	tabDone
)

var tabSectionNames = [...]string{"Needs you", "Planned", "In progress", "In review", "Done"}

// tabDoneFor is how long finished runs stay listed.
const tabDoneFor = 7 * 24 * time.Hour

// tabRow is one card on the board.
type tabRow struct {
	Section tabSection
	RunID   string // "" for a planned batch step, which has no run yet
	Repo    string
	Project string // the repository, without the worktree: what r filters by
	Title   string
	State   string
	Batch   string // "step n of m" for a batch step
	Detail  string // what is happening, or how it ended
	Goal    string // the brief's goal: what the work is for, shown in the details
	Watch   string // a contractor at work on the run (its builder, a reviewer), which ⏎ opens
	Age     time.Duration
	at      time.Time // last change, for ordering

	// The card's indicators (loop_tab_indicators.go).
	Round   int
	OpenP1  int
	Dispute string    // a disputed finding waiting for the person
	Reset   time.Time // a reviewer limit's reset the round waits for
	Chips   []tabChip // checks and reviewers of the review running now
	BatchID string
	PR      int    // the PR the session recorded with `ptln loop pr`, 0 for none
	CI      string // its checks from the watcher's cache: pass, fail, pending, or "" unknown
	Builder string // the engine building a builder-driven step, while its builder runs
}

// tabSectionOf places a run state. Every run state has a section; TestLoopsTab enumerates them.
func tabSectionOf(state string) (tabSection, bool) {
	switch state {
	case loopAwaiting, loopAwaitYou:
		return tabNeedsYou, true
	case loopBaseline, loopChecking, loopBatchRev, loopScopeRev, loopPaused:
		return tabInReview, true
	case loopBuilding, loopRevising:
		return tabInProgress, true
	case loopPRReady, loopFailed, loopStopped:
		return tabDone, true
	}
	return 0, false
}

// tabRows reads every run and batch into board rows, ordered within each section.
func tabRows(now time.Time) []tabRow {
	var rows []tabRow
	for _, r := range loopRuns() {
		sec, ok := tabSectionOf(r.State)
		if !ok {
			continue
		}
		if waitingForSettings(r) {
			sec = tabNeedsYou
		}
		at := tabLastChange(r)
		if sec == tabDone && now.Sub(at) > tabDoneFor {
			continue
		}
		row := tabRow{Section: sec, RunID: r.ID, Repo: repoLabel(r.Repo), Project: repoName(r.Repo), Title: tabTitle(r),
			State: r.State, Detail: tabDetail(r), Goal: briefGoal(latestBrief(r)), Age: now.Sub(at), at: at}
		tabIndicators(&row, r, now)
		if r.State == loopBuilding && builderDriven(r) {
			row.Watch = r.Builder
		}
		if sec == tabInReview {
			row.Watch = runningReviewer(r)
		}
		rows = append(rows, row)
	}
	rows = append(rows, tabPlannedRows()...)
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Section != rows[j].Section {
			return rows[i].Section < rows[j].Section
		}
		if rows[i].Section == tabDone {
			return rows[i].at.After(rows[j].at) // newest first
		}
		return false // keep loopRuns' and the batch's order otherwise
	})
	return rows
}

// tabPlannedRows lists the steps of approved, running batches that have no run yet.
func tabPlannedRows() []tabRow {
	var rows []tabRow
	dirs, _ := filepath.Glob(filepath.Join(loopsDir(), "batch-*"))
	for _, d := range dirs {
		b, err := loadBatch(strings.TrimPrefix(filepath.Base(d), "batch-"))
		if err != nil || len(b.Hashes) == 0 || b.Stopped || b.Failed {
			continue
		}
		repo, project := "", ""
		if len(b.Runs) > 0 {
			if r, err := loadLoopRun(b.Runs[0]); err == nil {
				repo, project = repoLabel(r.Repo), repoName(r.Repo)
			}
		}
		for i := len(b.Runs); i < len(b.Briefs); i++ {
			rows = append(rows, tabRow{Section: tabPlanned, Repo: repo, Project: project, Title: briefTitle(b.Briefs[i]), Goal: briefGoal(b.Briefs[i]),
				Batch: fmt.Sprintf("step %d of %d", i+1, len(b.Briefs)), Detail: "batch " + b.ID})
		}
	}
	return rows
}

func tabTitle(r *loopRun) string {
	if batchPending(r) {
		if b, err := loadBatch(r.Batch); err == nil {
			// The steps' own names say what the batch is; "Batch of 5 steps" said nothing.
			var names []string
			for _, br := range b.Briefs {
				names = append(names, briefTitle(br))
			}
			return fmt.Sprintf("%d steps: %s", len(b.Briefs), strings.Join(names, " · "))
		}
	}
	return briefTitle(latestBrief(r))
}

func tabBatchLabel(r *loopRun) string {
	if r.Batch == "" || batchPending(r) {
		return ""
	}
	b, err := loadBatch(r.Batch)
	if err != nil {
		return ""
	}
	if r.Step > len(b.Briefs) {
		return "deferred findings"
	}
	return fmt.Sprintf("step %d of %d", r.Step, len(b.Briefs))
}

// tabDetail says what a run is doing, by section.
func tabDetail(r *loopRun) string {
	switch r.State {
	case loopChecking, loopBatchRev, loopScopeRev:
		if p := tabReviewers(r); p != "" {
			return p
		}
	case loopBaseline:
		return "checks running before any work"
	case loopBuilding, loopRevising:
		if r.State == loopBuilding && builderDriven(r) && r.Builder != "" {
			if reset := buildersWaitingUntil(r); !reset.IsZero() {
				return fmt.Sprintf("⏸ resumes %s · every builder at its limit · round %d of %d", reset.Local().Format("15:04"), r.Round, loopMaxRounds)
			}
			// What the builder is doing now says more than the note, which is set when the build starts.
			doing := r.Note
			if a := contractorLastAction(r.Builder); a != "" {
				doing = "→ " + a
			}
			return fmt.Sprintf("%s %s build %d · round %d of %d · %s", uitheme.Gear, r.BuilderEngine, r.Builds, r.Round, loopMaxRounds, doing)
		}
		return fmt.Sprintf("round %d of %d · %s", r.Round, loopMaxRounds, r.Note)
	}
	return r.Note
}

// runningReviewer is a reviewer contractor reading r's work now, or "".
func runningReviewer(r *loopRun) string {
	for _, c := range loadContractors() {
		if c.Loop == r.ID && c.Role == "reviewer" && c.State == "running" {
			return c.ID
		}
	}
	return ""
}

// buildersWaitingUntil is the reset the current build waits for when every builder is limited, or
// zero: the latest builders_waiting event of this build, still in the future.
func buildersWaitingUntil(r *loopRun) time.Time {
	evs, err := loopEvents(r.ID)
	if err != nil {
		return time.Time{}
	}
	for i := len(evs) - 1; i >= 0; i-- {
		switch evs[i].Type {
		case "builders_waiting":
			if b, _ := evs[i].Data["build"].(float64); int(b) != r.Builds {
				return time.Time{}
			}
			s, _ := evs[i].Data["reset"].(string)
			if t, err := time.Parse(time.RFC3339Nano, s); err == nil && t.After(time.Now()) {
				return t
			}
			return time.Time{}
		case "builder_failover", "builder_started", "ready":
			return time.Time{} // a builder runs again, or the build ended
		}
	}
	return time.Time{}
}

// tabReviewers names each reviewer of the current review and whether it has reported: a review
// file appears as each one finishes.
func tabReviewers(r *loopRun) string {
	prefix := fmt.Sprintf("review-r%d-", r.Round)
	if r.State == loopBatchRev {
		prefix = fmt.Sprintf("review-b%d-", r.Revision)
	}
	if r.State == loopScopeRev {
		prefix = fmt.Sprintf("review-s%d-", r.Revision)
	}
	engines, err := parseLoopEngines(r.Review)
	if err != nil {
		return ""
	}
	var parts []string
	if r.State == loopChecking {
		if _, err := os.Stat(loopFile(r.ID, fmt.Sprintf("checks-%d.json", r.Round))); err != nil {
			return "checks running"
		}
		parts = append(parts, "checks done")
	}
	for i, e := range engines {
		mark := "reading"
		if _, err := os.Stat(loopFile(r.ID, fmt.Sprintf("%s%d-%s.json", prefix, i+1, safeFileName(e.String())))); err == nil {
			mark = "done"
		}
		parts = append(parts, e.String()+" "+mark)
	}
	return strings.Join(parts, " · ")
}

// tabLastChange is when the run last changed state, from its event log.
func tabLastChange(r *loopRun) time.Time {
	if evs, err := loopEvents(r.ID); err == nil && len(evs) > 0 {
		return evs[len(evs)-1].At
	}
	return r.Created
}

// tabAge is a short duration: "40s", "8m", "3h", "2d".
func tabAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
