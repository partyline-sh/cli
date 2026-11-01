package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Reviewer failover: a slot's models are tried in order, skipping the limited ones, and a review
// that hits a limit runs again with the next. A round whose every slot is limited waits for the
// earliest reset instead of failing the step.

// reviewSlot runs one slot's review with the first model that is not limited.
func reviewSlot(slot []string, failover bool, dir, prompt, diff string, at reviewAt) loopReview {
	var reset time.Time
	var replaced []string
	for _, model := range slot {
		e, _ := parseLoopEngines(model)
		until, err := reviewerLimited(model, time.Now())
		if err != nil {
			return loopReview{Engine: model, Error: err.Error()}
		}
		var rv loopReview
		if !until.IsZero() {
			rv = loopReview{Engine: model, Error: "model limited until " + until.Format(time.RFC3339), LimitedUntil: until}
		} else {
			rv = reviewWith(e[0], dir, prompt, diff, true, at)
		}
		if rv.LimitedUntil.IsZero() {
			rv.Replaced = replaced
			return rv
		}
		if reset.IsZero() || rv.LimitedUntil.Before(reset) {
			reset = rv.LimitedUntil
		}
		if !failover {
			rv.Replaced = replaced
			return rv
		}
		replaced = append(replaced, model)
	}
	return loopReview{Engine: strings.Join(slot, ","), Error: "every model in slot is limited", LimitedUntil: reset, Replaced: replaced}
}

// Configured reviews retry when every slot failed and a limit has a reset. Polling observes stops
// without holding the run lock while waiting for a reset.
func reviewRunSnapshot(r *loopRun, snap *loopSnapshot, engines []loopEngine, prompt, diff, prefix string) []loopReview {
	if err := r.checkConfig(); err != nil {
		return []loopReview{{Engine: "partyline", Error: err.Error()}}
	}
	for waits := 0; ; waits++ {
		reviews := reviewSnapshot(snap, engines, prompt, diff, loopDir(r.ID), prefix, r.Config, r)
		var earliest time.Time
		blocked := r.Config != nil && r.Config.Failover && len(reviews) > 0
		for i, rv := range reviews {
			if rv.Error == "" {
				blocked = false
			}
			if !rv.LimitedUntil.IsZero() && (earliest.IsZero() || rv.LimitedUntil.Before(earliest)) {
				earliest = rv.LimitedUntil
			}
			if len(rv.Replaced) > 0 {
				reviewEvent(r, "reviewer_failover", map[string]any{"round": prefix, "slot": i + 1, "replaced": rv.Replaced, "model": rv.Engine})
			}
		}
		// A limit that keeps coming back after its reset is not waited on forever.
		// A reset beyond the horizon (a weekly limit) fails the round to the person rather than
		// holding a snapshot and the round's process for days.
		if !blocked || earliest.IsZero() || waits == maxReviewerWaits || earliest.After(time.Now().Add(maxReviewerWait)) {
			return reviews
		}
		reviewEvent(r, "reviewers_waiting", map[string]any{"round": prefix, "reset": earliest})
		if !waitReviewerReset(r, earliest) {
			return reviews
		}
	}
}

// maxReviewerWaits is how many limit resets one round waits through before it fails.
const maxReviewerWaits = 3

// maxReviewerWait is the furthest reset a round waits for: past the 12-hour hold for a limit with
// no reset, short of a weekly limit.
const maxReviewerWait = 13 * time.Hour

func reviewEvent(r *loopRun, kind string, data map[string]any) {
	// Append an event without rewriting the state a stop may have changed during the review.
	f, err := os.OpenFile(filepath.Join(loopDir(r.ID), "events.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	line, _ := json.Marshal(loopEvent{At: time.Now().UTC(), Type: kind, Data: data})
	_, _ = f.Write(append(line, '\n'))
}

// reviewModelsSummary lists which model did each review of a run, for the batch summary.
func reviewModelsSummary(r *loopRun) string {
	files, _ := filepath.Glob(loopFile(r.ID, "review-*.json"))
	var s strings.Builder
	for _, file := range files {
		var rv loopReview
		data, err := os.ReadFile(file)
		if err != nil || json.Unmarshal(data, &rv) != nil {
			continue
		}
		label := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(file), "review-"), ".json")
		if rv.Engine == "" && rv.Verdict == "" && rv.Error == "" {
			continue // a reviewer's raw result file, not a review record
		}
		outcome := rv.Verdict
		if rv.Error != "" {
			outcome = "failed: " + rv.Error
		}
		fmt.Fprintf(&s, "   review %s: %s (%s)\n", termSafe(label), termSafe(rv.Engine), termSafe(outcome))
	}
	return s.String()
}

// waitReviewerReset waits until reset, or returns false as soon as the run moves on (a stop).
var waitReviewerReset = func(r *loopRun, reset time.Time) bool {
	for time.Now().Before(reset) {
		current, err := loadLoopRun(r.ID)
		if err != nil || current.State != r.State || current.Revision != r.Revision || current.Round != r.Round {
			return false
		}
		time.Sleep(min(time.Second, time.Until(reset)))
	}
	return true
}
