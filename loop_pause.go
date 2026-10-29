package main

import (
	"fmt"
	"strings"
	"time"
)

// Paused runs: a review that would decide a pass, or a batch's reading of its briefs, waits when a
// reviewer could not run, instead of passing on the others' word. A limit with a known reset
// resumes by itself once it lifts (pausedResume, on the watcher's tick); any other failure waits
// for `ptln loop resume <id>`. Either way the run's note names the reviewer and its error.

// reviewersMissing lists the reviews that did not complete, and when the last limit among them
// lifts: zero when any failure has no known reset, so only a person resumes it.
func reviewersMissing(reviews []loopReview) (failed []string, until time.Time) {
	manual := false
	for _, rv := range reviews {
		if rv.Error == "" {
			continue
		}
		what := rv.Engine + ": " + termSafe(rv.Error)
		if rv.LimitedUntil.IsZero() {
			manual = true
		} else {
			what += " until " + rv.LimitedUntil.Local().Format("Jan 2 15:04")
			if rv.LimitedUntil.After(until) {
				until = rv.LimitedUntil
			}
		}
		failed = append(failed, what)
	}
	if manual {
		until = time.Time{}
	}
	return failed, until
}

// pauseNote is the run's note and the session's message for a pause from what (a round, the batch
// review).
func pauseNote(id, what string, failed []string, until time.Time) (string, string) {
	note := fmt.Sprintf("paused: %s waits for %s", what, strings.Join(failed, "; "))
	resume := fmt.Sprintf("`ptln loop resume %s` runs it again", id)
	if !until.IsZero() {
		resume = fmt.Sprintf("it resumes by itself after %s; `ptln loop resume %s` runs it sooner", until.Local().Format("Jan 2 15:04"), id)
	}
	msg := fmt.Sprintf("Paused: %s could not finish, because a reviewer did not complete:\n- %s\n\nNothing passed on the other reviewers' word alone; %s. Nothing for you to do.", what, strings.Join(failed, "\n- "), resume)
	return note + "; " + resume, msg
}

// loopResumeRun puts a paused run back where it paused and runs that review again.
func loopResumeRun(id, why string) error {
	return withRunLock(id, func(r *loopRun) error {
		if r.State != loopPaused {
			return fmt.Errorf("loop %s is %s, not paused", r.ID, r.State)
		}
		from := r.PausedFrom
		r.PausedFrom, r.PausedUntil = "", time.Time{}
		switch from {
		case loopChecking:
			// The same round runs again on the same tree, which is why it may be unchanged.
			r.Recheck = true
			if err := r.set(loopChecking, fmt.Sprintf("round %d: checks, then review (%s)", r.Round, why), "resumed", nil); err != nil {
				return err
			}
			return spawnRound(r.ID, r.Round)
		case loopBatchRev:
			if err := r.set(loopBatchRev, "the reviewers are reading the batch ("+why+")", "resumed", nil); err != nil {
				return err
			}
			return spawnBatchReview(r.ID)
		}
		return fmt.Errorf("loop %s paused from %q, which cannot be resumed", r.ID, from)
	})
}

// loopResume is `ptln loop resume <id>`. It only runs a review again, so anyone may run it.
func loopResume(args []string) {
	r := mustLoop(args)
	if err := loopResumeRun(r.ID, "resumed by hand"); err != nil {
		fatal(err)
	}
	fmt.Printf("loop %s resumed. Run `ptln loop listen %s` in the background and end your turn.\n", r.ID, r.ID)
}

// pausedResume, on the watcher's tick, resumes each run paused on a limit that has lifted.
func pausedResume() {
	now := time.Now()
	for _, r := range loopRuns() {
		if r.State == loopPaused && !r.PausedUntil.IsZero() && now.After(r.PausedUntil) {
			_ = loopResumeRun(r.ID, "the reviewer's limit lifted")
		}
	}
}
