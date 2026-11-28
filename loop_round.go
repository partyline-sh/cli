package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// A round: the session says it is ready, partyline runs the approved checks on the work, then (only
// if no check newly fails) has other models review it. Whatever needs the session goes to its
// inbox. Rounds run detached (`ptln loop _round`), so they outlive the command and the turn.

// loopReady is `ptln loop ready <id> --report <file>`.
func loopReady(args []string) {
	f, rest := parseLoopFlags(args)
	r := mustLoop(rest)
	if f.report == "" {
		fatal(fmt.Errorf("usage: ptln loop ready %s --report <file>\n\nreport template:\n\n%s", r.ID, loopReportTemplate))
	}
	if err := validateReport(f.report); err != nil {
		fatal(err)
	}
	for _, line := range arbitratePending(r.ID) { // a dispute whose ruling was left unfinished
		fmt.Println(line)
	}
	round, err := loopStartRound(r.ID, f.report)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("round %d started: checks, then review. Do not edit files until it reports. %s\n", round, loopAdvice(r))
}

// loopStartRound moves a building run to checking and starts the round, under the run's lock, so a
// brief revision cannot land between the check and the transition (the revision would otherwise be
// overwritten and the round would run against an unapproved brief).
func loopStartRound(id, report string) (int, error) {
	round := 0
	err := withRunLock(id, func(r *loopRun) error {
		if r.State != loopBuilding {
			return fmt.Errorf("loop %s is %s; ready is for when it is building. %s", r.ID, r.State, loopNextStep(r))
		}
		if err := r.checkConfig(); err != nil {
			return err
		}
		if n := len(pendingRulings(r.ID)); n > 0 {
			return fmt.Errorf("loop %s: the arbiter has not ruled on %d disputed finding(s) yet; a round reviews only decided disputes", r.ID, n)
		}
		if r.Round >= loopMaxRounds {
			_ = r.set(loopFailed, fmt.Sprintf("%d rounds without passing", r.Round), "failed", nil)
			if r.Batch != "" {
				batchStepFailed(r)
			}
			return fmt.Errorf("loop %s: %s; the user decides what happens next", r.ID, r.Note)
		}
		r.Round++
		r.BuildFails = 0
		round = r.Round
		if err := os.WriteFile(loopFile(r.ID, fmt.Sprintf("ready-%d.md", r.Round)), []byte(report), 0o600); err != nil {
			return err
		}
		if err := r.set(loopChecking, fmt.Sprintf("round %d: checks, then review", r.Round), "ready", nil); err != nil {
			return err
		}
		return spawnRound(r.ID, r.Round)
	})
	return round, err
}

// loopRoundMain is the hidden `ptln loop _round <id> <n>`; round 0 is the baseline.
func loopRoundMain(args []string) {
	if len(args) != 2 {
		fatal(fmt.Errorf("usage: ptln loop _round <id> <n>"))
	}
	r := mustLoop(args[:1])
	n, _ := strconv.Atoi(args[1])
	// A round runs only from the state that legitimately starts it: the baseline after the person's
	// approval, round n after the session's n-th `ready`. Otherwise this hidden command would be a
	// way for a session to skip approval.
	switch {
	case n == 0 && r.State == loopBaseline:
	case n > 0 && r.State == loopChecking && r.Round == n:
	default:
		fatal(fmt.Errorf("loop %s is %s at round %d; round %d cannot run now", r.ID, r.State, r.Round, n))
	}
	if n == 0 {
		loopBaselineRound(r)
		return
	}
	loopRunRound(r, n)
}

func loopBaselineRound(r *loopRun) {
	res := runLoopChecksFor(r, r.Checks, loopFile(r.ID, "checks-0.log"))
	writeLoopJSON(r.ID, "baseline.json", res)
	if problem := checksEnvProblem(r.Repo, res); problem != "" {
		pauseForChecksEnv(r, loopBaseline, "the baseline", problem)
		return
	}
	var failing []string
	for _, c := range res.Checks {
		if c.Exit != 0 {
			failing = append(failing, fmt.Sprintf("`%s` (exit %d)", c.Cmd, c.Exit))
		}
	}
	msg := "The user approved the brief. Start building now.\n\n"
	if len(failing) > 0 {
		msg += "Before your change, these checks already fail; they do not count against this run:\n- " + strings.Join(failing, "\n- ") + "\n\n"
	}
	msg += fmt.Sprintf("When the work meets the brief, write a report with these sections and run `ptln loop ready %s --report <file>`:\n\n%s", r.ID, loopReportTemplate)
	task := "Build the step described by the brief."
	if len(failing) > 0 {
		task += "\n\nBefore your change, these checks already fail; they do not count against this step:\n- " + strings.Join(failing, "\n- ")
	}
	roundApplyTask(r, loopBuilding, "building", "baseline_done", map[string]any{"already_failing": len(failing)}, msg, task)
}

// roundApply saves a round's outcome, and posts msg, only if the run is still where the round
// found it: the round ran for minutes on its own copy, and a stop made meanwhile must stand.
func roundApply(r *loopRun, state, note, ev string, data map[string]any, msg string) {
	roundApplyTask(r, state, note, ev, data, msg, msg)
}

// roundApplyTask is roundApply where a builder-driven step gets task, for its next builder, instead
// of the session getting msg.
func roundApplyTask(r *loopRun, state, note, ev string, data map[string]any, msg, task string) {
	_ = withRunLock(r.ID, func(cur *loopRun) error {
		if cur.State != r.State || cur.Round != r.Round {
			return nil
		}
		if state == loopBuilding && builderDriven(cur) {
			msg = builderHandOff(cur, note, task)
		}
		// The recheck is used up only by a round that reviewed a tree.
		if r.reviewed {
			cur.LastTree, cur.Recheck = r.LastTree, false
		}
		if err := cur.set(state, note, ev, data); err != nil {
			return err
		}
		if state == loopFailed && cur.Batch != "" {
			batchStepFailed(cur)
		}
		if msg != "" {
			return loopPost(cur.ID, msg)
		}
		return nil
	})
}

// batchStepFailed ends the batch a failed step belongs to, and puts any findings deferred so far to
// the person for their ruling.
func batchStepFailed(r *loopRun) {
	_ = withBatch(r.Batch, func(b *loopBatch) error {
		b.Failed = true
		if anyDeferred(b) && r.Step <= len(b.Briefs) {
			head, _ := gitMem(r.Repo, "rev-parse", "HEAD")
			_, err := startDeferredReview(r, b, head)
			return err
		}
		return nil
	})
}

func loopRunRound(r *loopRun, n int) {
	back := func(note, msg string) {
		backTask(r, n, note, msg, msg)
	}
	log := loopFile(r.ID, fmt.Sprintf("checks-%d.log", n))
	res := runLoopChecksFor(r, r.Checks, log)
	if res.Stale {
		res = runLoopChecksFor(r, r.Checks, log)
	}
	if res.Stale {
		back(fmt.Sprintf("round %d: files changed during the checks", n), "Files changed while the checks were running, so the results do not describe any one version of the work. Do not edit files after `ready`.")
		return
	}
	if problem := checksEnvProblem(r.Repo, res); problem != "" {
		pauseForChecksEnv(r, loopChecking, fmt.Sprintf("round %d", n), problem)
		return
	}
	if res.Tree == r.LastTree && !r.Recheck {
		roundApply(r, loopFailed, fmt.Sprintf("round %d changed nothing since round %d", n, n-1), "failed", nil,
			"This round's work is identical to the previous round's, so the loop stopped. The user decides what happens next.")
		return
	}
	r.LastTree, r.reviewed = res.Tree, true
	writeLoopJSON(r.ID, fmt.Sprintf("checks-%d.json", n), res)

	var baseline loopCheckRound
	readLoopJSON(r.ID, "baseline.json", &baseline)
	brief := latestBrief(r)
	failures := newFailures(res.Checks, baseline.Checks)
	passed := map[string]bool{}
	for _, c := range res.Checks {
		if c.Exit == 0 {
			passed[strings.TrimSpace(c.Cmd)] = true
		}
	}
	failures = append(failures, acceptanceFailures(r, brief, log, passed)...)
	if len(failures) > 0 {
		back(fmt.Sprintf("round %d: %d check(s) failing", n, len(failures)), "These checks fail and did not fail before your change:\n\n"+formatLoopChecks(failures))
		return
	}
	loopReviewRound(r, n, brief, back)
}

// backTask sends round n's work back: the session gets msg with how to submit again, a
// builder-driven step's next builder gets task.
func backTask(r *loopRun, n int, note, msg, task string) {
	roundApplyTask(r, loopBuilding, note, "round_done", nil, msg+fmt.Sprintf("\n\nFix this, then run `ptln loop ready %s --report <file>` again (round %d of %d used).", r.ID, n, loopMaxRounds),
		task+fmt.Sprintf("\n\n(Round %d of %d used.)", n, loopMaxRounds))
}

// acceptanceFailures runs the brief's `run:` lines and checks its `file:` lines. These must pass
// whatever the baseline did: a feature's acceptance command fails before the feature exists.
//
// A run: line identical to a check that already passed this round, on this same tree, is not run
// again: briefs often list `make check-go` as acceptance too, and running it twice cost every round
// a minute or two. One that failed is run again, since acceptance must pass whatever the baseline
// did.
func acceptanceFailures(r *loopRun, brief, log string, passed map[string]bool) []loopCheck {
	var cmds []string
	var out []loopCheck
	for _, a := range briefAcceptance(brief) {
		switch a.Kind {
		case "run":
			if passed[strings.TrimSpace(a.Value)] {
				continue
			}
			cmds = append(cmds, a.Value)
		case "file":
			if _, err := os.Stat(filepath.Join(r.Repo, a.Value)); err != nil {
				out = append(out, loopCheck{Cmd: "file: " + a.Value, Exit: 1, Tail: "missing"})
			}
		}
	}
	for _, c := range runLoopChecksFor(r, cmds, log).Checks {
		if c.Exit != 0 {
			out = append(out, c)
		}
	}
	return out
}

func formatLoopChecks(cs []loopCheck) string {
	var b strings.Builder
	for _, c := range cs {
		fmt.Fprintf(&b, "$ %s   (exit %d)\n%s\n\n", c.Cmd, c.Exit, c.Tail)
	}
	return strings.TrimSpace(b.String())
}

func latestBrief(r *loopRun) string {
	b, _ := os.ReadFile(loopFile(r.ID, fmt.Sprintf("brief-%d.md", r.Revision)))
	return string(b)
}

// writeLoopJSON replaces a run's file whole: a reader in another process (a contractor reading
// the pending rulings while its claimer updates them) never sees it half written.
func writeLoopJSON(id, name string, v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	f, err := os.CreateTemp(loopDir(id), "."+name+".*")
	if err != nil {
		return
	}
	_, werr := f.Write(b)
	if cerr := f.Close(); werr == nil && cerr == nil {
		_ = os.Rename(f.Name(), loopFile(id, name))
		return
	}
	_ = os.Remove(f.Name())
}

func readLoopJSON(id, name string, v any) {
	if b, err := os.ReadFile(loopFile(id, name)); err == nil {
		_ = json.Unmarshal(b, v)
	}
}
