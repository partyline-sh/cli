package main

import (
	"fmt"
	"time"
)

// Reversing the arbiter: the person overrules a dismiss from the approval popup. Later steps may
// already be built on top of it, so the step is not reopened: the finding goes on the batch's
// Deferred list and is put to the person with the batch's other disputes after its last step, or at
// once when the batch has already ended.

// standingDismissals is how many of b's arbiter dismissals the person has not reversed.
func standingDismissals(b *loopBatch) []loopArbitration {
	var out []loopArbitration
	for _, a := range b.Arbitrated {
		if a.Decision == "dismiss" && !a.Reversed {
			out = append(out, a)
		}
	}
	return out
}

// batchAskDeferred puts the batch's waiting deferred findings to the person when no run will: a
// step still running ends in the deferred review, and a live deferred review picks new ones up (or,
// building its fixes, asks again when they are committed). Otherwise one starts now. Called holding
// prev's lock and the batch's.
func batchAskDeferred(prev *loopRun, b *loopBatch) error {
	waiting := false
	for _, d := range b.Deferred {
		waiting = waiting || d.Decision == "deferred"
	}
	if !waiting {
		return nil
	}
	for _, id := range append(append([]string(nil), b.Runs...), b.Deferral) {
		if id == "" {
			continue
		}
		r := prev
		if id != prev.ID {
			var err error
			if r, err = loadLoopRun(id); err != nil {
				continue
			}
		}
		if r.live() && (id == b.Deferral || !b.Stopped && !b.Failed) {
			return nil
		}
	}
	head, _ := gitLoop(prev.Repo, "rev-parse", "HEAD")
	_, err := startDeferredReview(prev, b, head)
	return err
}

// batchReverseDismiss reverses the first dismiss of batch id that still stands and returns the id
// it was given on the Deferred list.
func batchReverseDismiss(id string) (string, error) { return batchReverse(id, "") }

// batchReverse reverses the dismiss named which (S<step>-<finding>, as the popup confirmed it), or
// with which empty the first that still stands. A dismiss already reversed is refused, so a stale
// confirmation never reverses a different one.
func batchReverse(id, which string) (string, error) {
	b, err := loadBatch(id)
	if err != nil {
		return "", err
	}
	if len(b.Runs) == 0 {
		return "", fmt.Errorf("batch %s has no steps", id)
	}
	last := b.Runs[len(b.Runs)-1]
	if b.Deferral != "" {
		last = b.Deferral
	}
	var listed string
	// The run's lock, then the batch's, as everywhere: a deferred review may have to start.
	err = withRunLock(last, func(prev *loopRun) error {
		return withBatch(id, func(b *loopBatch) error {
			i := -1
			for j, a := range b.Arbitrated {
				if a.Decision == "dismiss" && !a.Reversed && (which == "" || fmt.Sprintf("S%d-%s", a.Step, a.Finding.ID) == which) {
					i = j
					break
				}
			}
			if i < 0 && which != "" {
				return fmt.Errorf("%s is no longer an arbiter dismissal that stands; nothing was reversed", which)
			}
			if i < 0 {
				return fmt.Errorf("batch %s has no arbiter dismissal left to reverse", id)
			}
			a := &b.Arbitrated[i]
			a.Reversed = true
			t := a.Finding
			t.ID = fmt.Sprintf("S%d-%s", a.Step, t.ID)
			t.Decision = "deferred"
			t.Dispute = fmt.Sprintf("%s (the arbiter dismissed it, confidence %.2f: %s; you reversed that on %s)", t.Dispute, a.Confidence, a.Reason, time.Now().Format("Jan 2 15:04"))
			b.Deferred = append(b.Deferred, t)
			listed = t.ID
			return batchAskDeferred(prev, b)
		})
	})
	return listed, err
}
