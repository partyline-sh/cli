//go:build darwin && tray

package main

import "fmt"

// finished_runs.go — tell me when a background run ends.
//
// A run finishing on your own laptop is the case email and Slack are both wrong for: you are sitting
// in front of the machine that did the work, and the answer arrives in a mailbox. The tray already
// posts native notifications and posts them AS partyline (notify_darwin.go); it just was not
// watching runs.
//
// EDGE-TRIGGERED, exactly like the peer section: notify on a run id we have NOT seen, never on a
// state that merely persists. `ptln state` republishes the same rows for up to two hours, so a
// level-triggered version would re-notify every poll — which is how a notification feature becomes
// the first thing someone turns off.
//
// The first poll after the tray starts seeds silently. A tray that launches and immediately fires
// six notifications about runs that finished while it was closed is noise, not news.
type finishedWatch struct {
	seen   map[string]bool
	seeded bool
}

func newFinishedWatch() *finishedWatch { return &finishedWatch{seen: map[string]bool{}} }

// notices returns the bodies to post for this poll, and records what it saw.
func (w *finishedWatch) notices(rows []finishedRun) []string {
	var out []string
	for _, r := range rows {
		if r.RunID == "" || w.seen[r.RunID] {
			continue
		}
		w.seen[r.RunID] = true
		if w.seeded {
			out = append(out, finishedBody(r))
		}
	}
	w.seeded = true
	// Bound the memory: the CLI keeps 20 rows and expires them, so anything we are still holding
	// beyond that can never reappear and re-notify.
	if len(w.seen) > 200 {
		w.seen = map[string]bool{}
		for _, r := range rows {
			w.seen[r.RunID] = true
		}
	}
	return out
}

// finishedBody says what happened and, when it needs a person, that it does. The status is the whole
// point — "a run finished" is not worth interrupting someone for if they cannot tell whether it
// worked.
func finishedBody(r finishedRun) string {
	what := r.Project
	if what == "" {
		what = "a run"
	}
	switch r.Status {
	case "done":
		if r.Tasks > 1 {
			return fmt.Sprintf("%s — %d tasks done", what, r.Tasks)
		}
		return what + " — done"
	case "failed":
		return what + " — failed"
	case "needs_approval":
		return what + " — needs you"
	case "killed":
		return what + " — stopped"
	default:
		return fmt.Sprintf("%s — %s", what, r.Status)
	}
}
