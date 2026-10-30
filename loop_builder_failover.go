package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// Builder failover: a builder that stops on a usage or rate limit hands over to the next builder in
// the batch's approved list (its config, never the live settings) that is not limited, in the same
// contractor, on the same checkout and run. When every builder is limited, the builder waits for the
// earliest reset as reviewers do (maxReviewerWaits, maxReviewerWait); past that the step fails to the
// person. With failover off a limit is waited out on the same model.

// errBuildersLimited is every builder limited past what a step waits for.
type errBuildersLimited struct{ reset time.Time }

func (e errBuildersLimited) Error() string {
	return "every builder is at its usage limit until " + e.reset.Local().Format("Jan 2 15:04")
}

// builderHandover is added to the prompt of a builder that takes over from a limited one.
const builderHandover = "\n\nThe previous builder hit its usage limit; continue from the working tree as it is."

// builderFor is the builder to run now: the first of r's batch's builders that is not limited (only
// the first with failover off). When none is free it returns the zero engine and the earliest reset.
func builderFor(r *loopRun, now time.Time) (loopEngine, time.Time, error) {
	b, err := loadBatch(r.Batch)
	if err != nil {
		return loopEngine{}, time.Time{}, err
	}
	builders := batchBuilders(b)
	if b.Config == nil || !b.Config.Failover { // as for reviewers: no config, no failover
		builders = builders[:1]
	}
	var earliest time.Time
	for _, e := range builders {
		until, err := reviewerLimited(e.String(), now)
		if err != nil {
			return loopEngine{}, time.Time{}, err
		}
		if until.IsZero() {
			return e, time.Time{}, nil
		}
		if earliest.IsZero() || until.Before(earliest) {
			earliest = until
		}
	}
	return loopEngine{}, earliest, nil
}

// waitBuilderReset waits until reset, or returns false once the run moves on (a stop). A variable so
// tests need not wait.
var waitBuilderReset = func(r *loopRun, reset time.Time) bool { return waitReviewerReset(r, reset) }

// runBuildFailover runs build n of r with the builder builderFor picks, handing over on a limit and
// waiting when every builder is limited. It returns the final message and the engine that wrote it.
func runBuildFailover(r *loopRun, n int, contractorID, prompt string, out io.Writer) (string, loopEngine, error) {
	waits := 0
	var limitedOn loopEngine // the builder that last hit its limit
	for {
		e, reset, err := builderFor(r, time.Now())
		if err != nil {
			return "", loopEngine{}, err
		}
		if e.Name == "" {
			if waits == maxReviewerWaits || reset.After(time.Now().Add(maxReviewerWait)) {
				return "", loopEngine{}, errBuildersLimited{reset}
			}
			waits++
			reviewEvent(r, "builders_waiting", map[string]any{"build": n, "reset": reset})
			fmt.Fprintf(out, "\nevery builder is at its usage limit; waiting until %s\n", reset.Local().Format("15:04"))
			stop := heartbeat(out, 10*time.Minute, "still waiting for the usage limit to lift at "+reset.Local().Format("15:04"))
			ok := waitBuilderReset(r, reset)
			stop()
			if !ok {
				return "", loopEngine{}, fmt.Errorf("the step moved on while its builders were limited")
			}
			continue
		}
		builderUse(r.ID, n, contractorID, e)
		p := prompt
		if limitedOn.Name != "" && limitedOn != e { // a different model takes over
			p += builderHandover
		}
		text, err := runBuilder(context.Background(), e, r.Repo, p, builderFile(r.ID, n, "last.txt"), out)
		var failed *reviewerFailure
		if errors.As(err, &failed) {
			if until, limited := reviewerLimit(e.Name, failed.diag, time.Now()); limited {
				if rerr := recordReviewerLimit(e.String(), until); rerr != nil {
					return text, e, rerr
				}
				reviewEvent(r, "builder_limited", map[string]any{"build": n, "model": e.String(), "reset": until})
				fmt.Fprintf(out, "\n%s is at its usage limit until %s\n", e, until.Local().Format("Jan 2 15:04"))
				limitedOn = e
				continue
			}
		}
		return text, e, err
	}
}

// heartbeat writes line to out every interval until stopped, so a long wait is not mistaken for a
// silent builder (builderQuietAfter).
func heartbeat(out io.Writer, every time.Duration, line string) func() {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				fmt.Fprintln(out, line)
			}
		}
	}()
	return func() { close(done) }
}

// builderUse records which builder runs build n: on the run (for the card and the commit's author)
// and on its contractor (for the chip).
func builderUse(id string, n int, contractorID string, e loopEngine) {
	_ = withRunLock(id, func(cur *loopRun) error {
		if cur.Builds != n || cur.BuilderEngine == e.String() {
			return nil
		}
		cur.BuilderEngine = e.String()
		return cur.save(loopEvent{Type: "builder_failover", Data: map[string]any{"build": n, "model": e.String()}})
	})
	_ = updateContractor(contractorID, func(c *contractor) bool {
		if c.Engine == e.String() {
			return false
		}
		c.Engine = e.String()
		return true
	})
}

// builderLimitedOut fails the step when every builder stayed limited past what it waits for.
func builderLimitedOut(id string, n int, err errBuildersLimited) {
	_ = withRunLock(id, func(cur *loopRun) error {
		if cur.State != loopBuilding || cur.Builds != n {
			return nil
		}
		if serr := cur.set(loopFailed, err.Error(), "failed", map[string]any{"reset": err.reset}); serr != nil {
			return serr
		}
		batchStepFailed(cur)
		return loopPost(cur.ID, fmt.Sprintf("Step %d failed: %s, past what a step waits for. The user decides what happens next.", cur.Step, err.Error()))
	})
}
