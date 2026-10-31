package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	uitheme "partyline.sh/partyline/internal/theme"
)

// The contractor row: a tmux status row above the session tabs, shown only while a contractor runs
// or a finished one has not been seen. Without it the bar is as before: status-format[0] the tabs,
// [1] the memory ribbon (`status 2`). With it: [0] the contractor row, [1] the tabs, [2] the
// ribbon (`status 3`). The tabs' format is whatever [0] held, kept in @ptln_tabs_format so hiding
// the row puts it back exactly.

// contractorShown is whether c belongs on the row: running, or finished and not yet seen.
func contractorShown(c *contractor) bool {
	return c.State == "running" || !c.Seen
}

// contractorKeep is how long a seen, finished record is kept before it is pruned.
const contractorKeep = 7 * 24 * time.Hour

// contractorsReconcile brings the row, the badges and the session in line with the records: a
// running record whose window is gone ended without saying so, and a starting one was abandoned
// (a live start holds the lock until its window is recorded); the session goes with its last
// contractor; the row shows while anything is on it; seen records older than a week are removed
// with their logs. All of it under the records lock, so two reconciles never move the row at once
// and none kills a session a start is opening a window in.
func contractorsReconcile() {
	_ = withContractors(func() error {
		all := loadContractors()
		live, shown := 0, 0
		badges := map[string]int{} // parent pane → running contractors
		for _, c := range all {
			ended := ""
			switch {
			case c.State == "starting":
				ended = "it was never started"
			case c.State == "running" && !contractorAlive(c):
				ended = "its window closed before it finished"
			}
			if ended != "" {
				c.State, c.Ended, c.Exit = "failed", time.Now().UTC(), -1
				if c.Result == "" {
					c.Result = ended
				}
				_ = c.save()
				contractorTellLoop(c)
			}
			if c.Seen && !c.Ended.IsZero() && time.Since(c.Ended) > contractorKeep {
				_ = os.Remove(contractorPath(c.ID))
				_ = os.Remove(contractorLog(c.ID))
				continue
			}
			if c.State == "running" {
				live++
				badges[strings.TrimPrefix(c.Parent, "pane:")]++
			}
			if contractorShown(c) {
				shown++
			}
		}
		// The watcher runs this every tick: with nothing running or shown and no row up, the badges,
		// the row and the session were all put right by the reconcile that saw the last one go.
		if live == 0 && shown == 0 && !contractorRowActive() {
			return nil
		}
		setContractorBadges(badges)
		// The row first: when this runs inside the last contractor's own window, killing the session
		// hangs up this process.
		setContractorRow(shown > 0)
		killContractorSession(live)
		return nil
	})
}

// contractorRowActive is whether row 0 is the contractor row.
var contractorRowActive = func() bool {
	out, _ := tmuxCmd("show-options", "-gv", "status-format[0]").Output()
	return strings.Contains(string(out), "--contractors-row")
}

// setContractorRow shows or hides the row by moving the tabs and the ribbon down or back up. It
// reads what row 0 holds rather than the row count: re-sourcing tmux.conf (every attach) resets
// `status 2` and the ribbon's row while the contractor row may still be in row 0.
var setContractorRow = func(show bool) {
	get := func(opt string) string {
		out, _ := tmuxCmd("show-options", "-gv", opt).Output()
		return strings.TrimRight(string(out), "\n")
	}
	set := func(opt, v string) { _ = tmuxCmd("set-option", "-g", opt, v).Run() }
	active := strings.Contains(get("status-format[0]"), "--contractors-row")
	three := get("status") == "3"
	switch {
	case show && !active:
		set("@ptln_tabs_format", get("status-format[0]"))
		set("status-format[2]", get("status-format[1]"))
		set("status-format[1]", get("@ptln_tabs_format"))
		set("status-format[0]", contractorRowFormat())
		set("status", "3")
	case show && !three: // re-sourced conf: row 1 is the ribbon again, the tabs are saved
		set("status-format[2]", get("status-format[1]"))
		set("status-format[1]", get("@ptln_tabs_format"))
		set("status", "3")
	case !show && active:
		ribbon := get("status-format[1]")
		if three {
			ribbon = get("status-format[2]")
		}
		set("status-format[0]", get("@ptln_tabs_format"))
		set("status-format[1]", ribbon)
		_ = tmuxCmd("set-option", "-gu", "status-format[2]").Run()
		set("status", "2")
	}
	_ = tmuxCmd("refresh-client", "-S").Run()
}

// contractorRowFormat is the row's status-format: the chips, drawn by `ptln tmux --contractors-row`.
func contractorRowFormat() string {
	th := tmuxTheme()
	return "#[bg=" + th.Surface + "]#(" + selfExe() + " tmux --contractors-row)"
}

// setContractorBadges marks each parent's window with how many contractors it has running
// (@ptln_contractors, drawn as ⚙N on its tab) and clears the rest.
var setContractorBadges = func(badges map[string]int) {
	wins := map[string]int{}
	for pane, n := range badges {
		if out, err := tmuxCmd("display-message", "-p", "-t", pane, "#{window_id}").Output(); err == nil {
			wins[strings.TrimSpace(string(out))] += n
		}
	}
	out, err := tmuxCmd("list-windows", "-t", tmuxSessionName, "-F", "#{window_id}").Output()
	if err != nil {
		return
	}
	for _, win := range strings.Fields(string(out)) {
		if n := wins[win]; n > 0 {
			_ = tmuxCmd("set-option", "-w", "-t", win, "@ptln_contractors", fmt.Sprint(n)).Run()
		} else {
			_ = tmuxCmd("set-option", "-w", "-u", "-t", win, "@ptln_contractors").Run()
		}
	}
}

// contractorRow is the row's text: the chips grouped under their parent's tab. Each chip is a
// status range, so a click names it (see the MouseDown1Status binding).
func contractorRow(all []*contractor, parentName func(string) string, now time.Time) string {
	th := tmuxTheme()
	var groups []string
	order, byParent := []string{}, map[string][]*contractor{}
	for _, c := range all {
		if !contractorShown(c) {
			continue
		}
		if _, ok := byParent[c.Parent]; !ok {
			order = append(order, c.Parent)
		}
		byParent[c.Parent] = append(byParent[c.Parent], c)
	}
	for _, p := range order {
		var chips []string
		for _, c := range byParent[p] {
			glyph, colour := contractorGlyph(th, c, now)
			age := tabAge(now.Sub(c.Started))
			if !c.Ended.IsZero() {
				age = tabAge(c.Ended.Sub(c.Started))
			}
			label := termSafe(strings.SplitN(c.Engine, ":", 2)[0] + " · " + c.Title)
			chips = append(chips, "#[range=user|"+c.ID+"]#[fg="+colour+"]"+glyph+" #[fg="+th.Text+"]"+tmuxSafe(label)+" #[fg="+th.Faint+"]"+age+"#[norange]")
		}
		groups = append(groups, "#[fg="+th.Muted+"]"+tmuxSafe(parentName(p))+": "+strings.Join(chips, "   "))
	}
	return " #[fg=" + th.Focus + ",bold]" + uitheme.Gear + "#[nobold] contractors  " + strings.Join(groups, "  #[fg="+th.Faint+"]│  ")
}

// contractorSpinner is the running chip's frames, the board's; tmux redraws the row every
// status-interval.
var contractorSpinner = tabSpinner

// contractorGlyph is a chip's marker: a spinner while it runs, ✓ or ✗ once it ended.
func contractorGlyph(th uitheme.Theme, c *contractor, now time.Time) (string, string) {
	switch c.State {
	case "running":
		return contractorSpinner[int(now.UnixMilli()/500)%len(contractorSpinner)], th.Colour(uitheme.Working)
	case "done":
		return uitheme.Passed.Glyph(), th.Colour(uitheme.Passed)
	}
	return uitheme.Failed.Glyph(), th.Colour(uitheme.Failed)
}

// tmuxSafe keeps text from being read as tmux format: '#' doubled, control characters dropped.
func tmuxSafe(s string) string { return strings.ReplaceAll(termSafe(s), "#", "##") }

// contractorParentName is a parent's tab as the row names it: "12 Partyline Original".
func contractorParentName(parent string) string {
	pane := strings.TrimPrefix(parent, "pane:")
	if out, err := tmuxCmd("display-message", "-p", "-t", pane, "#{window_index} #{window_name}").Output(); err == nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			return s
		}
	}
	return parent
}

// contractorsSeen marks finished contractors seen: one opened from its chip, or every finished one
// whose parent is in window (its tab was selected).
func contractorsSeen(id, window string) {
	for _, c := range loadContractors() {
		if c.State == "running" || c.Seen {
			continue
		}
		mine := c.ID == id
		if !mine && window != "" {
			if out, err := tmuxCmd("display-message", "-p", "-t", strings.TrimPrefix(c.Parent, "pane:"), "#{window_id}").Output(); err == nil {
				mine = strings.TrimSpace(string(out)) == window
			}
		}
		if mine {
			_ = updateContractor(c.ID, func(r *contractor) bool { r.Seen = true; return true })
		}
	}
	contractorsReconcile()
}
