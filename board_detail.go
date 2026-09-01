package main

import (
	"fmt"
	"strings"

	"partyline.sh/partyline/internal/api"
)

// board_detail.go — the detail pane: everything about one card that does not fit on its tile.
//
// This is the screen that decides whether the terminal board can replace the browser for the daily
// loop. A tile can say "failed"; only this can say what failed, what the agent was doing when it
// did, and what the acceptance criteria were that it failed against. Without it every non-trivial
// question still ends in a browser tab, which is the thing the board exists to stop.
//
// Logs are fetched off the event loop and delivered as an event, so opening the pane on a run with
// thousands of log lines never freezes the board.

// detailOverlay shows one card in full, with its live run log.
type detailOverlay struct {
	card    api.BoardCard
	logs    []api.RunLogLine
	logErr  string // last fetch failure, shown as a footer note — never as a replacement for the log
	loading bool
	scroll  int
	tail    bool // stick to the newest line as logs arrive (true until you scroll up)
}

func (o *detailOverlay) title() string { return clipVis(cardTitle(o.card), 60) }

func (o *detailOverlay) footer() string {
	f := boardDim + "↑↓ scroll · a actions · esc close" + reset
	if o.tail {
		f = boardDim + "↑↓ scroll · following · a actions · esc close" + reset
	}
	return f
}

// lines is the card's facts, then its log tail.
func (o *detailOverlay) lines(m *boardModel, w, h int) []string {
	body := append(o.factLines(w), o.logLines(w)...)

	// Following means the newest line, not the top: a run log you open mid-build should show what
	// is happening now, and re-anchoring to the top on every refresh is how a live tail becomes
	// unreadable.
	if o.tail {
		o.scroll = max(0, len(body)-h)
	}
	if o.scroll > max(0, len(body)-h) {
		o.scroll = max(0, len(body)-h)
	}
	if o.scroll < 0 {
		o.scroll = 0
	}
	return body[o.scroll:min(len(body), o.scroll+h)]
}

func (o *detailOverlay) factLines(w int) []string {
	c := o.card
	state, _ := cardState(c)

	var out []string
	add := func(k, v string) {
		if strings.TrimSpace(v) == "" {
			return
		}
		out = append(out, boardDim+padVis(k, 12)+reset+clipVis(v, max(10, w-12)))
	}

	add("state", state)
	add("project", c.Title)
	add("column", c.Column.Title())
	add("machine", c.Machine)
	if c.Total > 0 {
		add("tasks", fmt.Sprintf("%d of %d done", c.Done, c.Total))
	}
	add("engine", c.Engine)
	add("preset", c.Preset)
	add("owner", c.Owner)
	add("parent", c.ParentLabel)
	if c.ReviewGrade != "" {
		add("review", "graded "+c.ReviewGrade)
	}
	if c.Readiness != nil {
		note := fmt.Sprintf("%d of 5", *c.Readiness)
		if c.StartBlockedByReadiness() {
			note += " — under the floor, so starting it is gated"
		}
		add("readiness", note)
	}
	// Whatever the provider sent for this pane, in ITS order — it knows which of its fields a reader
	// looks at first and we do not.
	for _, f := range c.Fields {
		add(f.Label, f.Value)
	}
	add("", "")

	// Why it is not moving, in the same priority the tile uses. Repeating it here in full sentences
	// is deliberate: the tile has one line and has to abbreviate, and an abbreviation of "waiting
	// on a chain member that needs a human" is where the confusion starts.
	switch {
	case c.ChainBlocker != nil:
		out = append(out, wrapPlain(boardWarn+"Held up by an earlier step in its chain: "+
			strings.TrimSpace(c.ChainBlocker.Task)+" ("+c.ChainBlocker.Status+"). That run needs a decision before this one can move."+reset, w)...)
	case c.ChainWaiting:
		out = append(out, wrapPlain(boardDim+"Waiting for an earlier step in its chain. This is idle by design, not stuck."+reset, w)...)
	case c.ConcurrencyWaiting:
		out = append(out, wrapPlain(boardDim+"Waiting for a slot — the team's concurrency cap is full."+reset, w)...)
	case c.MachineLocked:
		out = append(out, wrapPlain(boardWarn+"Its machine is below the minimum supported version, so nothing will dispatch to it. Update that machine."+reset, w)...)
	case c.Stalled:
		out = append(out, wrapPlain(boardBad+"Stalled: the machine stopped reporting. Continue picks up where it stopped; Restart rebuilds from scratch."+reset, w)...)
	case c.Status == "failed" && c.Detail != "":
		out = append(out, wrapPlain(boardBad+"Failed: "+c.Detail+reset, w)...)
	case c.NoPR != nil:
		out = append(out, wrapPlain(boardWarn+"No PR: "+c.NoPR.Detail+reset, w)...)
	}

	if c.Conflict != nil && c.Conflict.Count > 0 {
		msg := fmt.Sprintf("Conflicts with %d other open PR(s).", c.Conflict.Count)
		if c.Conflict.Resolvable {
			msg += " At least one is ours to rebase."
		}
		out = append(out, wrapPlain(boardWarn+msg+reset, w)...)
	}
	if c.PRURL != "" {
		out = append(out, "", boardDim+"PR  "+reset+clipVis(c.PRURL, max(10, w-4)))
	}
	if u := previewURL(c); u != "" {
		out = append(out, boardDim+"preview  "+reset+clipVis(u, max(10, w-9)))
	}

	// A foreign card has no run, so there is no log to head — and a "run log / nothing streamed yet"
	// section under somebody's Odoo task is a heading that will never fill in. Its prose goes here
	// instead, which is the half that makes the pane worth opening.
	if c.Foreign {
		if body := strings.TrimSpace(c.Body); body != "" {
			out = append(out, "", boardMid+"details"+reset, "")
			for _, para := range strings.Split(body, "\n") {
				if strings.TrimSpace(para) == "" {
					out = append(out, "")
					continue
				}
				out = append(out, wrapPlain(para, w)...)
			}
		}
		return out
	}

	out = append(out, "", boardMid+"run log"+reset)
	return out
}

func (o *detailOverlay) logLines(w int) []string {
	if o.card.Foreign {
		return nil // no run behind it; factLines carried the provider's own detail instead
	}
	switch {
	case o.loading && len(o.logs) == 0:
		return []string{boardDim + "  reading…" + reset}
	case len(o.logs) == 0:
		return []string{boardDim + "  nothing streamed yet" + reset}
	}
	var out []string
	if o.logErr != "" {
		out = append(out, boardBad+"  (log refresh failed: "+o.logErr+" — showing the last good read)"+reset)
	}
	for _, l := range o.logs {
		for _, line := range strings.Split(strings.TrimRight(l.Body, "\n"), "\n") {
			prefix := "  "
			if l.Stream == "stderr" {
				prefix = boardBad + "! " + reset
			}
			out = append(out, prefix+clipVis(line, max(10, w-2)))
		}
	}
	return out
}

func (o *detailOverlay) key(b []byte, m *boardModel, c *api.Client) (bool, bool) {
	if len(b) >= 3 && b[0] == 0x1b && b[1] == '[' {
		switch b[2] {
		case 'A':
			o.scroll, o.tail = max(0, o.scroll-1), false
		case 'B':
			o.scroll++
		}
		return false, false
	}
	switch b[0] {
	case 'k':
		o.scroll, o.tail = max(0, o.scroll-1), false
	case 'j':
		o.scroll++
	case 'G':
		o.tail = true
	case 'a':
		acts := boardActions(o.card)
		if len(acts) == 0 {
			m.setToast("nothing to do on this card", false)
			return true, false
		}
		m.openOverlay(&actionOverlay{card: o.card, acts: acts})
		return false, false
	case 'o':
		m.openURL(o.card.PRURL, "PR")
		return false, false
	case 'q':
		return true, false
	}
	return false, false
}

// detailPane opens the focused card's detail and kicks off a log fetch.
func (m *boardModel) detailPane(c *api.Client) {
	card, ok := m.focused()
	if !ok {
		return
	}
	o := &detailOverlay{card: *card, tail: true}
	// An unscheduled item has no run, so there is no log to fetch and "reading…" would hang there
	// forever claiming to be doing something.
	o.loading = !card.Unscheduled
	m.openOverlay(o)
	if o.loading {
		m.fetchLogs(c, card.ID)
	}
}

// fetchLogs reads a run's log off the event loop and posts it back as an event.
func (m *boardModel) fetchLogs(c *api.Client, runID string) {
	events := m.events
	if events == nil {
		return
	}
	go func() {
		logs, err := c.RunLogs(runID)
		ev := boardEvent{logsFor: runID, logs: logs}
		if err != nil {
			ev.logsErr = err
		}
		select {
		case events <- ev:
		case <-m.stop:
		}
	}()
}

// applyLogs delivers a finished log fetch to the detail pane, if it is still showing that run. The
// id check is what stops a slow fetch for one card painting its logs into another card's pane after
// you have moved on.
func (m *boardModel) applyLogs(runID string, logs []api.RunLogLine, err error) {
	o, ok := m.overlay.(*detailOverlay)
	if !ok || o.card.ID != runID {
		return
	}
	o.loading = false
	if err != nil {
		// Report the blip WITHOUT discarding what is already on screen. Replacing the log with an
		// error line blanked the pane someone was reading, and the next poll silently restored it.
		o.logErr = err.Error()
		return
	}
	o.logErr = ""
	o.logs = logs
}

// refreshDetail re-reads an open detail pane from the board that just arrived: the card itself, and
// the log if the run is live.
//
// The card matters as much as the log. The pane used to hold the snapshot taken when it opened, so
// a run watched for ten minutes still showed the state, progress and PR it had at the start — and
// `a` inside the pane computed its actions from that snapshot, offering Accept on a run that had
// since failed. The server refuses it, but being offered the wrong move is the bug.
func (m *boardModel) refreshDetail(c *api.Client) {
	o, ok := m.overlay.(*detailOverlay)
	if !ok {
		return
	}
	if m.data != nil {
		if fresh, found := m.data.Find(o.card.ID); found {
			o.card = fresh
		}
	}
	if o.card.Unscheduled {
		return
	}
	switch o.card.Status {
	case "running", "accepted", "paused":
		m.fetchLogs(c, o.card.ID)
	}
}
