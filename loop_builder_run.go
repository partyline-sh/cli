package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"time"
)

// A builder contractor's command, and what happens when it ends: its report starts the round, and
// anything else starts the next builder (loop_builder.go starts them).

// loopBuildMain is the hidden `ptln loop _build <id> <n> <engine> <contractor>`: a builder
// contractor's command. It runs the n-th builder of run id, then hands its result to the loop.
func loopBuildMain(args []string) {
	if err := runLoopBuild(args); err != nil {
		fatal(err)
	}
	fmt.Println("\nbuilder finished; partyline runs the round")
}

func runLoopBuild(args []string) error {
	if len(args) != 4 {
		return fmt.Errorf("usage: ptln loop _build <id> <n> <engine> <contractor>")
	}
	id, me := args[0], args[3]
	n, _ := strconv.Atoi(args[1])
	es, err := parseLoopEngines(args[2])
	if err != nil {
		return err
	}
	// Under the lock: the starter holds it until this builder is recorded as the run's current one.
	var r *loopRun
	if err := withRunLock(id, func(cur *loopRun) error {
		if cur.State != loopBuilding || cur.Builds != n || cur.Builder != me {
			return fmt.Errorf("loop %s is %s at build %d; build %d is not the current one", cur.ID, cur.State, cur.Builds, n)
		}
		r = cur
		return nil
	}); err != nil {
		return err
	}
	prompt, err := os.ReadFile(builderFile(id, n, "prompt.md"))
	if err != nil {
		return err
	}
	fmt.Printf("partyline builder %d for loop %s, step %d (%s)\n\n", n, id, r.Step, es[0])
	out := newQuietWriter(os.Stdout, builderQuietAfter, func() { builderQuiet(id, n, me) })
	text, _, err := runBuildFailover(r, n, me, string(prompt), out)
	out.stop()
	var limited errBuildersLimited
	if errors.As(err, &limited) {
		builderLimitedOut(id, n, limited)
		return err
	}
	loopBuilderDone(id, n, text, err)
	return err
}

// loopBuilderDone hands a finished builder's result to the loop: a report starts the round as
// `ready` does, after raising the disputes it lists; anything else starts another builder.
func loopBuilderDone(id string, n int, text string, runErr error) {
	_ = os.WriteFile(builderFile(id, n, "report.md"), []byte(text), 0o600)
	r, err := loadLoopRun(id)
	if err != nil || r.State != loopBuilding || r.Builds != n {
		return // stopped, or superseded, meanwhile
	}
	verr := validateReport(text)
	switch {
	case r.Revision != r.BuildRevision:
		builderAgain(id, n, false, false, "brief revised", revisedTask(n))
	case runErr != nil:
		builderAgain(id, n, true, false, "build failed", fmt.Sprintf("Build %d ended with an error: %v\n\nIts last message:\n%s\n\nThe work it did is in the checkout. Find out what went wrong, finish the step and report.", n, runErr, clipTail(text, 2000)))
	case verr != nil && !r.ReportOnly:
		builderAgain(id, n, false, true, "report missing", fmt.Sprintf("Build %d finished without a complete report (%v). The work it did is in the checkout. Check it, finish anything left, and end with the complete report. Its final message was:\n\n%s", n, reportProblem(verr), clipTail(text, 4000)))
	case verr != nil:
		builderAgain(id, n, true, false, "report missing again", fmt.Sprintf("Build %d was asked only for the report and still ended without one (%v). Finish the step and end with the complete report.", n, reportProblem(verr)))
	default:
		var raised []builderDispute // only these can come back as "fix": an old ruling is not resent
		for _, d := range builderDisputes(text) {
			if _, err := loopRaiseDispute(id, d.id, d.reason); err != nil {
				_ = loopPost(id, fmt.Sprintf("Builder %d disputed %s, but it could not be raised: %v", n, d.id, err))
				continue
			}
			raised = append(raised, d)
		}
		// With the arbiter on, its rulings come before the round, so the reviewers see them decided.
		// One that rules a disputed finding must be fixed goes straight to the next builder.
		arbitratePending(id)
		if fixes := arbiterFixes(id, raised); len(fixes) > 0 {
			builderAgain(id, n, false, false, "the arbiter ruled a dispute must be fixed",
				"The arbiter ruled that these findings you disputed must be fixed:\n\n"+formatTracked(fixes)+"\n\nFix them, then report.")
			return
		}
		// Outside a batch's steps a dispute goes to the person and the run waits; their decision
		// starts the next builder (loopDecideDispute).
		if r, err := loadLoopRun(id); err == nil && r.State == loopAwaitYou {
			return
		}
		if _, err := loopStartRound(id, text); err != nil {
			_ = loopPost(id, fmt.Sprintf("Builder %d finished, but partyline could not start the round: %v", n, err))
		}
	}
}

// builderAgain starts the next builder after build n, if n is still the current one. failed counts
// it against loopMaxBuildFails; reportOnly marks the next one as asked only for the report.
func builderAgain(id string, n int, failed, reportOnly bool, note, task string) {
	_ = withRunLock(id, func(cur *loopRun) error {
		if cur.State != loopBuilding || cur.Builds != n {
			return nil
		}
		if failed {
			cur.BuildFails++
		}
		if cur.BuildFails >= loopMaxBuildFails {
			err := cur.set(loopFailed, fmt.Sprintf("%d builder runs in a row ended without a report", cur.BuildFails), "failed", nil)
			if cur.Batch != "" {
				batchStepFailed(cur)
			}
			_ = loopPost(cur.ID, fmt.Sprintf("Step %d failed: %s. The user decides what happens next.", cur.Step, cur.Note))
			return err
		}
		if err := startLoopBuilder(cur, task, reportOnly); err != nil {
			cur.Builder = "" // as builderHandOff: the watcher must not count the last one's end again
			_ = loopPost(cur.ID, fmt.Sprintf("Step %d: partyline could not start a builder after build %d: %v", cur.Step, n, err))
			return cur.set(loopBuilding, "the builder could not start: "+err.Error(), "builder_failed", nil)
		}
		return cur.set(loopBuilding, fmt.Sprintf("build %d: %s", cur.Builds, note), "builder_started",
			map[string]any{"contractor": cur.Builder, "engine": cur.BuilderEngine, "build": cur.Builds})
	})
}

// buildersResume starts again any builder that ended without handing over: its window closed, the
// server went, the runner crashed. The watcher runs it every tick; a builder hands over before its
// record says it ended, so a current builder whose record has ended was lost.
func buildersResume() {
	for _, r := range loopRuns() {
		if r.State != loopBuilding || !builderDriven(r) || r.Builder == "" {
			continue
		}
		c, err := loadContractor(r.Builder)
		if err != nil || c.State == "running" || c.State == "starting" {
			continue
		}
		if r.BuilderStopped || r.Revision != r.BuildRevision {
			builderAgain(r.ID, r.Builds, false, false, "brief revised", revisedTask(r.Builds))
			continue
		}
		builderAgain(r.ID, r.Builds, true, false, "build stopped",
			fmt.Sprintf("Build %d stopped before it finished (%s). The work it did is in the checkout. Continue the step and report.", r.Builds, c.Result))
	}
}

func revisedTask(n int) string {
	return fmt.Sprintf("The step's brief was revised after build %d started; the brief above is the revision. The work so far is in the checkout. Build to the revised brief and report.", n)
}

// builderQuiet tells the person a builder has printed nothing for builderQuietAfter.
func builderQuiet(id string, n int, contractorID string) {
	_ = withRunLock(id, func(cur *loopRun) error {
		if cur.Builds != n || cur.State != loopBuilding {
			return nil
		}
		_ = loopPost(id, fmt.Sprintf("Builder %s (build %d of step %d) has printed nothing for %s. It was not stopped; ctrl-\\ k shows it.", contractorID, n, cur.Step, builderQuietAfter))
		return cur.save(loopEvent{Type: "builder_quiet", Note: "builder quiet for " + builderQuietAfter.String(), Data: map[string]any{"contractor": contractorID}})
	})
}

// quietWriter passes writes through and calls quiet once each time nothing has been written for after.
type quietWriter struct {
	w     io.Writer
	mu    sync.Mutex
	last  time.Time
	told  bool
	done  chan struct{}
	after time.Duration
}

func newQuietWriter(w io.Writer, after time.Duration, quiet func()) *quietWriter {
	q := &quietWriter{w: w, last: time.Now(), done: make(chan struct{}), after: after}
	go func() {
		t := time.NewTicker(min(after/4, time.Minute))
		defer t.Stop()
		for {
			select {
			case <-q.done:
				return
			case <-t.C:
				q.mu.Lock()
				due := !q.told && time.Since(q.last) >= q.after
				if due {
					q.told = true
				}
				q.mu.Unlock()
				if due {
					quiet()
				}
			}
		}
	}()
	return q
}

func (q *quietWriter) Write(b []byte) (int, error) {
	q.mu.Lock()
	q.last, q.told = time.Now(), false
	q.mu.Unlock()
	return q.w.Write(b)
}

func (q *quietWriter) stop() { close(q.done) }
