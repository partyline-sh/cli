package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// `ptln loop tab`, and the details a row opens.

// loopTab runs the board full-screen or in a popup. --fixture is accepted and ignored.
func loopTab(fixture bool, args ...string) {
	final, err := tea.NewProgram(contractorTabArgs(newTabModel(loopTabActions(fixture, insidePtlnTmux())), args)).Run()
	if err != nil {
		fatal(err)
	}
	if m, ok := final.(tabModel); ok && m.after != nil {
		if r, err := loadLoopRun(m.after.RunID); err == nil && waitingForSettings(r) {
			m.after, m.settings = nil, true // a on a loop waiting for settings opens them
			final = m
		}
	}
	if m, ok := final.(tabModel); ok && m.settings {
		if path := os.Getenv("PARTYLINE_LOOPS_ACTION"); path != "" {
			if err := os.WriteFile(path, []byte(loopsActionSettings), 0600); err != nil {
				fatal(err)
			}
		} else if err := settingsMain(); err != nil {
			fatal(err)
		}
		return
	}
	if m, ok := final.(tabModel); ok && m.after != nil {
		if path := os.Getenv("PARTYLINE_LOOPS_ACTION"); path != "" {
			if err := os.WriteFile(path, []byte(m.after.RunID), 0600); err != nil {
				fatal(err)
			}
		} else if err := m.act.gateRun(*m.after); err != nil {
			fatal(err)
		}
	}
}

func loopTabActions(fixture, inTmux bool) tabActions {
	return tabActions{rows: tabRows, detail: tabDetailText, now: time.Now, remove: loopDelete, target: loopDeleteTarget,
		gateRun: func(r tabRow) error {
			return tmuxCmd("run-shell", "-b", shQuote(selfExe())+" tmux --gate-popup --gate-run "+shQuote(r.RunID)).Run()
		},
	}
}

// loopsActionSettings is what the board leaves for runLoopsPopup when , asked for the settings; a
// run id never looks like it.
const loopsActionSettings = ":settings"

// runLoopsPopup applies a gate request only after display-popup has exited.
func runLoopsPopup(contractorArgs ...string) error {
	f, err := os.CreateTemp("", "ptln-loops-action-*")
	if err != nil {
		return err
	}
	path := f.Name()
	f.Close()
	defer os.Remove(path)
	cols, rows := tmuxClientSize()
	args := loopsPopupCommand(selfExe(), cols, rows, contractorArgs...)
	args = append(args[:1], append([]string{"-e", "PARTYLINE_LOOPS_ACTION=" + path}, args[1:]...)...)
	if err := tmuxCmd(args...).Run(); err != nil {
		return err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(b) == 0 {
		return nil
	}
	if string(b) == loopsActionSettings {
		openSettingsPopup()
		return nil
	}
	args = gatePopup(selfExe())
	args = append(args, "--run", string(b))
	return tmuxCmd(args...).Run()
}

// tabDetailText is a row's details: each round's checks and findings by priority, brief revisions,
// and for a batch step the batch's summary.
func tabDetailText(row tabRow) string {
	if row.RunID == "" {
		out := "Not started yet: " + termSafe(row.Batch) + " of " + termSafe(row.Detail) + "."
		if row.Goal != "" {
			out = "Goal: " + termSafe(row.Goal) + "\n\n" + out
		}
		return out
	}
	r, err := loadLoopRun(row.RunID)
	if err != nil {
		return err.Error()
	}
	var b strings.Builder
	if row.Goal != "" {
		fmt.Fprintf(&b, "Goal: %s\n\n", termSafe(row.Goal))
	}
	// Who is on it right now: the builder or reviewers working this run, from the contractors.
	var now []string
	for _, c := range loadContractors() {
		if c.Loop == r.ID && c.State == "running" {
			now = append(now, fmt.Sprintf("  %s %s · %s · %s", c.Engine, c.Role, termSafe(c.Title), tabAge(time.Since(c.Started))))
		}
	}
	if len(now) > 0 {
		b.WriteString("Now:\n" + strings.Join(now, "\n") + "\n\n")
	}
	fmt.Fprintf(&b, "loop %s · %s · %s\n%s\n", r.ID, termSafe(row.Repo), r.State, termSafe(r.Note))
	if r.PR != "" {
		fmt.Fprintf(&b, "PR %s\n", termSafe(r.PR))
	}
	// A disputed finding waiting for the user's ruling goes first: it is what the card asks about.
	var d loopTracked
	if r.State == loopAwaitYou {
		readLoopJSON(r.ID, "dispute.json", &d)
	}
	if d.ID != "" {
		fmt.Fprintf(&b, "\nWaiting for your ruling (a opens it; there d dismisses, f fixes):\n%s\n  dispute: %s\n", formatTracked([]loopTracked{d}), termSafe(d.Dispute))
	}
	for n := 1; n <= r.Round; n++ {
		fmt.Fprintf(&b, "\nRound %d\n", n)
		var checks loopCheckRound
		readLoopJSON(r.ID, fmt.Sprintf("checks-%d.json", n), &checks)
		for _, c := range checks.Checks {
			mark := "pass"
			if c.Exit != 0 {
				mark = fmt.Sprintf("exit %d", c.Exit)
			}
			fmt.Fprintf(&b, "  $ %s  %s\n", termSafe(c.Cmd), mark)
		}
		var found []loopTracked
		readLoopJSON(r.ID, fmt.Sprintf("findings-%d.json", n), &found)
		counts := map[string]int{}
		for _, f := range found {
			if f.Dropped == "" {
				counts[f.Priority]++
			}
		}
		if len(found) > 0 {
			fmt.Fprintf(&b, "  findings: %d P1 · %d P2 · %d P3\n", counts["P1"], counts["P2"], counts["P3"])
		}
		for _, f := range found {
			if f.Priority == "P1" && f.Dropped == "" {
				fmt.Fprintf(&b, "    %s %s:%d %s\n", f.ID, termSafe(f.File), f.Line, termSafe(f.Claim))
			}
		}
	}
	if evs, err := loopEvents(r.ID); err == nil {
		for _, e := range evs {
			switch e.Type {
			case "brief_revised", "brief_revised_in_scope", "brief_revised_out_of_scope":
				fmt.Fprintf(&b, "\nbrief revised (%s): %s\n", strings.TrimPrefix(e.Type, "brief_revised"), termSafe(e.Note))
			}
		}
	}
	if r.Batch != "" {
		if bt, err := loadBatch(r.Batch); err == nil {
			b.WriteString("\n" + batchSummary(bt))
		}
	}
	return b.String()
}
