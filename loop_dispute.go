package main

import (
	"fmt"
	"os"
	"strings"
)

// The review half of a round, and disputes. A P1 must be fixed or disputed; a dispute goes to the
// person, never back to the reviewer, because two models out-voting each other does not establish
// who is right. Decided findings are given to later reviewers so they are not raised again.

type loopTracked struct {
	ID string `json:"id"` // F<round>-<n>
	loopFinding
	Engine   string `json:"engine"`
	Dispute  string `json:"dispute,omitempty"`  // the session's reason
	Decision string `json:"decision,omitempty"` // fix | dismiss, by the person
}

func loopReviewRound(r *loopRun, n int, brief string, back func(note, msg string)) {
	engines, _ := parseLoopEngines(r.Review)
	snap, err := takeSnapshot(r.Repo, r.Base)
	if err != nil {
		back(fmt.Sprintf("round %d: snapshot failed", n), "Partyline could not snapshot the work: "+err.Error())
		return
	}
	defer snap.Cleanup()
	diff, err := snap.Diff()
	if err == nil {
		err = snap.WritePatch(diff)
	}
	if err != nil {
		back(fmt.Sprintf("round %d: diff failed", n), "Partyline could not diff the work: "+err.Error())
		return
	}
	report, _ := os.ReadFile(loopFile(r.ID, fmt.Sprintf("ready-%d.md", n)))
	var decided []loopTracked
	readLoopJSON(r.ID, "decided.json", &decided)
	prompt := loopReviewPromptWith(brief, string(report), diff, qualityStandard(r.Config)) + decidedSection(decided)
	reviews := reviewRunSnapshot(r, snap, engines, prompt, diff, fmt.Sprintf("r%d-", n))

	var open, minor, suppressed []loopTracked
	var failed []string
	unverified := 0
	for _, rv := range reviews {
		if rv.Error != "" {
			failed = append(failed, rv.Engine+": "+termSafe(rv.Error))
			continue
		}
		for _, d := range rv.Dropped {
			if d.Priority == "P1" {
				unverified++
			}
		}
		for _, f := range rv.Findings {
			t := loopTracked{ID: fmt.Sprintf("F%d-%d", n, len(open)+len(minor)+1), loopFinding: f, Engine: rv.Engine}
			// A P1 already ruled on, or deferred to the person, is not raised again whatever the
			// reviewer says: the prompt asks, this enforces.
			if f.Priority == "P1" && sameFinding(settled(decided), f) {
				f.Dropped = "matches a finding the user dismissed or that was deferred to them"
				suppressed = append(suppressed, loopTracked{ID: fmt.Sprintf("F%d-s%d", n, len(suppressed)+1), loopFinding: f, Engine: rv.Engine})
				continue
			}
			if f.Priority == "P1" {
				open = append(open, t)
			} else {
				minor = append(minor, t)
			}
		}
	}
	// Suppressed ones are kept in the record, so a wrong match can be found without the raw reviews.
	writeLoopJSON(r.ID, fmt.Sprintf("findings-%d.json", n), append(append(open, minor...), suppressed...))
	// A P1 from the reviewers that did complete goes back as usual: the next round is reviewed again.
	// Otherwise the round would pass, or be judged by nobody, without every review: it pauses.
	if len(failed) > 0 && len(open) == 0 {
		missing, until := reviewersMissing(reviews)
		note, msg := pauseNote(r.ID, fmt.Sprintf("round %d's review", n), missing, until)
		r.PausedFrom, r.PausedUntil = loopChecking, until
		roundApplyPaused(r, note, missing, msg)
		return
	}
	if len(open) > 0 {
		backTask(r, n, fmt.Sprintf("round %d: %d open P1", n, len(open)),
			"The checks pass. Reviewers found these P1 issues. Each one must be fixed, or disputed with `ptln loop dispute "+r.ID+" <id> --reason \"...\"`, which sends it to the user:\n\n"+formatTracked(open)+minorSection(minor),
			"The checks pass. Reviewers found these P1 issues. Fix each one, or dispute it under ## Disputes in your report:\n\n"+formatTracked(open)+minorSection(minor))
		return
	}
	note := "no new failures, no open P1"
	if len(suppressed) > 0 {
		note += fmt.Sprintf("; %d P1 matched a settled finding and were not raised (see findings-%d.json)", len(suppressed), n)
	}
	if unverified > 0 {
		note += fmt.Sprintf("; %d P1 could not be verified and were not delivered (see %s)", unverified, loopDir(r.ID))
	}
	var msg string
	if r.Batch != "" {
		what := "to start the next step"
		if b, err := loadBatch(r.Batch); err == nil && r.Step >= len(b.Briefs) {
			what = "to close the batch (and put any deferred findings to the user)"
		}
		if deferredReviewRun(r) {
			msg = fmt.Sprintf("The fixes to the batch's deferred findings pass (%s). Commit them; the batch is done. `ptln loop batch status %s` summarises it for the user.%s", note, r.ID, minorSection(minor))
		} else {
			msg = fmt.Sprintf("Step %d of the batch passes (%s). Commit its work now, then run `ptln loop batch next %s` %s; do not open a PR yet, the person reviews the batch's branch.%s", r.Step, note, r.ID, what, minorSection(minor))
		}
	} else {
		msg = "The checks pass and reviewers found no P1 (" + note + "). Commit and open the PR, then record it with `ptln loop pr " + r.ID + " <url>` so the Loops board shows it; partyline does not merge." + minorSection(minor)
	}
	if builderDriven(r) {
		// Partyline commits the step and starts the next; the session is told only if that fails.
		roundApplyTask(r, loopPRReady, note, "pr_ready", nil, "", "")
		builderAdvance(r.ID)
		return
	}
	roundApply(r, loopPRReady, note, "pr_ready", nil, msg)
}

func formatTracked(ts []loopTracked) string {
	var b strings.Builder
	for _, t := range ts {
		fmt.Fprintf(&b, "%s [%s, %s] %s:%d\n  quote: %s\n  claim: %s\n  fix: %s\n\n", t.ID, t.Priority, termSafe(t.Engine),
			termSafe(t.File), t.Line, termSafe(t.Quote), termSafe(t.Claim), termSafe(t.Fix))
	}
	return strings.TrimSpace(b.String())
}

func minorSection(ts []loopTracked) string {
	if len(ts) == 0 {
		return ""
	}
	return "\n\nP2 and P3, for the PR description; fix the cheap ones and say which:\n\n" + formatTracked(ts)
}

func decidedSection(ds []loopTracked) string {
	if len(ds) == 0 {
		return ""
	}
	var b strings.Builder
	var quiet, verify []loopTracked
	for _, d := range ds {
		if d.Decision == "fix" {
			verify = append(verify, d)
		} else {
			quiet = append(quiet, d)
		}
	}
	if len(quiet) > 0 {
		b.WriteString("\nDismissed by the user or the arbiter, or deferred to the user; do not raise these again:\n")
		for _, d := range quiet {
			fmt.Fprintf(&b, "- %s:%d %s\n", d.File, d.Line, d.Claim)
		}
	}
	if len(verify) > 0 {
		b.WriteString("\nThe user, or the arbiter, ruled these must be fixed. Check each one: raise it again as a P1 if it is still not fixed:\n")
		for _, d := range verify {
			fmt.Fprintf(&b, "- %s:%d %s\n", d.File, d.Line, d.Claim)
		}
	}
	return b.String()
}

// loopDispute is `ptln loop dispute <id> <finding> --reason "..."`, run by the session.
func loopDispute(args []string) {
	f, rest := parseLoopFlags(args)
	if len(rest) < 2 || strings.TrimSpace(f.reason) == "" {
		fatal(fmt.Errorf("usage: ptln loop dispute <id> <finding-id> --reason \"why it is wrong\""))
	}
	id, err := loopRaiseDispute(rest[0], rest[1], f.reason)
	if err != nil {
		fatal(err)
	}
	r := mustLoop(rest)
	if arbiterOn(r) {
		if err := spawnArbitrate(rest[0]); err != nil {
			fatal(fmt.Errorf("%s is queued for the arbiter, but it could not start: %v; `ptln loop ready` runs it", id, err))
		}
		fmt.Printf("%s goes to the arbiter (%s); its ruling arrives in your inbox. %s A round cannot start until it has ruled.\n", id, r.Config.Arbiter, loopAdvice(r))
		return
	}
	if r, _ := loadLoopRun(rest[0]); r != nil && r.Batch != "" && !deferredReviewRun(r) {
		fmt.Printf("%s is deferred to the user's review of the batch; it no longer blocks this step. Carry on, then run `ptln loop ready %s --report <file>`.\n", id, rest[0])
		return
	}
	fmt.Printf("%s goes to the user. Tell them it is waiting: ctrl-\\ then a opens the popup. %s\n", id, loopAdvice(r))
}

// loopRaiseDispute sends finding to the user with the session's reason, and returns its id.
func loopRaiseDispute(runID, finding, reason string) (string, error) {
	var id string
	err := withRunLock(runID, func(r *loopRun) error {
		// One dispute waits at a time: replacing one while the person reads it would make their
		// decision apply to a finding they never saw.
		if r.State == loopAwaitYou {
			return fmt.Errorf("a dispute is already waiting for the user; wait for their decision")
		}
		// Only while building: deciding a dispute moves the run to building, so one raised while a
		// brief waits for approval, or after the run stopped, would skip the approval or undo the stop.
		if r.State != loopBuilding {
			return fmt.Errorf("loop %s is %s; findings can be disputed only while building", r.ID, r.State)
		}
		t, ok := findTracked(r, finding)
		if !ok || t.Dropped != "" {
			return fmt.Errorf("no finding %s in loop %s", finding, r.ID)
		}
		var decided []loopTracked
		readLoopJSON(r.ID, "decided.json", &decided)
		for _, d := range decided {
			if d.ID == t.ID && d.Quote == t.Quote {
				return fmt.Errorf("%s was already disputed (%s)", t.ID, d.Decision)
			}
		}
		t.Dispute = reason
		id = t.ID
		if arbiterOn(r) {
			return arbiterQueue(r, t)
		}
		if r.Batch != "" && !deferredReviewRun(r) {
			return batchDefer(r, t)
		}
		writeLoopJSON(r.ID, "dispute.json", t)
		return r.set(loopAwaitYou, "disputed "+t.ID, "disputed", map[string]any{"finding": t.ID})
	})
	return id, err
}

func findTracked(r *loopRun, id string) (loopTracked, bool) {
	var ts []loopTracked
	readLoopJSON(r.ID, fmt.Sprintf("findings-%d.json", r.Round), &ts)
	for _, t := range ts {
		if strings.EqualFold(t.ID, id) {
			return t, true
		}
	}
	return loopTracked{}, false
}

// batchDefer records a P1 disputed during a batch step for the person's morning review, and lets
// the step carry on: an unattended batch does not stop for a ruling only the person can make. The
// finding is marked decided so the reviewers do not raise it again in this step.
func batchDefer(r *loopRun, t loopTracked) error {
	if err := batchListDeferred(r, t); err != nil {
		return err
	}
	r.Recheck = true
	return r.set(loopBuilding, t.ID+" deferred to the user", "deferred", map[string]any{"finding": t.ID})
}

// batchListDeferred marks t deferred in r and puts it on r's batch's list for the person, without
// touching r's state.
func batchListDeferred(r *loopRun, t loopTracked) error {
	t.Decision = "deferred"
	var decided []loopTracked
	readLoopJSON(r.ID, "decided.json", &decided)
	writeLoopJSON(r.ID, "decided.json", append(decided, t))
	// Finding ids restart in every step, so the person's list names the step too.
	listed := t
	listed.ID = fmt.Sprintf("S%d-%s", r.Step, t.ID)
	return withBatch(r.Batch, func(b *loopBatch) error {
		b.Deferred = append(b.Deferred, listed)
		return nil
	})
}

// settled is the decided findings that no longer need fixing: dismissed, or deferred to the person.
// One the person ruled "fix" stays open while a reviewer still finds it.
func settled(ds []loopTracked) []loopTracked {
	var out []loopTracked
	for _, d := range ds {
		if d.Decision == "dismiss" || d.Decision == "deferred" {
			out = append(out, d)
		}
	}
	return out
}

// deferredReviewRun: r is the run after a batch's last step that puts its deferred findings to the
// person. A dispute there goes to the person directly; they are already being asked.
func deferredReviewRun(r *loopRun) bool {
	b, err := loadBatch(r.Batch)
	return err == nil && r.Step > len(b.Briefs)
}

// roundApplyPaused pauses the round, if the run is still where the round found it.
func roundApplyPaused(r *loopRun, note string, missing []string, msg string) {
	_ = withRunLock(r.ID, func(cur *loopRun) error {
		if cur.State != r.State || cur.Round != r.Round {
			return nil
		}
		if r.reviewed {
			cur.LastTree, cur.Recheck = r.LastTree, false
		}
		cur.PausedFrom, cur.PausedUntil = r.PausedFrom, r.PausedUntil
		if err := cur.set(loopPaused, note, "paused", map[string]any{"missing": missing}); err != nil {
			return err
		}
		return loopPost(cur.ID, msg)
	})
}
