package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"partyline.sh/partyline/internal/clispec"
)

// The batch commands the session runs: start, revise (after the batch review or a rejection), next
// (after a step passes) and status.

func loopBatchMain(args []string) {
	if len(args) == 0 {
		fatal(fmt.Errorf("usage: ptln loop batch start|revise|next|status"))
	}
	switch args[0] {
	case clispec.LoopStart:
		loopBatchStart(args[1:])
	case "revise":
		loopBatchRevise(args[1:])
	case "next":
		loopBatchNext(args[1:])
	case "status":
		loopBatchStatus(args[1:])
	default:
		fatal(fmt.Errorf("unknown loop batch command %q", args[0]))
	}
}

func loopBatchStart(args []string) {
	f, _ := parseLoopFlags(args)
	if len(f.briefs) < 2 || len(f.checks) == 0 {
		fatal(fmt.Errorf("usage: ptln loop batch start --brief <file> --brief <file> [...] --checks \"<cmd>\" [--review claude,codex] [--config <file>]\n(a batch has at least two steps; one brief is `ptln loop start`)"))
	}
	root, keys := loopPrecheck(f, f.briefs)
	if what := askForSettings(); what != "" {
		_, r, err := recordBatch(root, keys, f.checks, f.review, f.briefs, f.config)
		if err == nil {
			err = waitForSettings(r, f.config == nil && !f.reviewSet, what)
		}
		if err != nil {
			fatal(err)
		}
		fmt.Println(settingsWaitAdvice(r, what))
		return
	}
	config, err := startConfig(f)
	if err != nil {
		fatal(err)
	}
	b, r, err := createBatch(root, keys, f.checks, f.review, f.briefs, config)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("batch %s started as loop %s: the reviewers read the %d briefs as a set first. %s\n", b.ID, r.ID, len(b.Briefs), loopAdvice(r))
}

// startConfig is a new batch's config: --config's, none when --review names the reviewers, else
// the settings (ctrl-\ ,).
func startConfig(f loopFlags) (*loopConfig, error) {
	if f.config != nil || f.reviewSet {
		return f.config, nil
	}
	s, err := loadSettings()
	if err == nil {
		err = s.validate() // a hand-edited file is checked as the popup checks it
	}
	if err == nil {
		var c *loopConfig
		if c, err = batchConfigFromSettings(s, f.review); err == nil {
			return c, nil
		}
	}
	return nil, fmt.Errorf("loop settings: %v; fix them with ctrl-\\ then , or pass --config", err)
}

// createBatch records a batch and its first step's run, and sends the briefs to the reviewers.
func createBatch(root string, keys, checks []string, review string, briefs []string, config *loopConfig) (*loopBatch, *loopRun, error) {
	b, r, err := recordBatch(root, keys, checks, review, briefs, config)
	if err != nil {
		return nil, nil, err
	}
	if err := r.set(loopBatchRev, "the reviewers are reading the batch", "batch_started", map[string]any{"batch": b.ID, "steps": len(b.Briefs), "checks": checks, "review": review}); err != nil {
		return nil, nil, err
	}
	if err := spawnBatchReview(r.ID); err != nil {
		_ = r.set(loopFailed, "could not start the batch review: "+err.Error(), "failed", nil)
		return nil, nil, err
	}
	return b, r, nil
}

// recordBatch writes a batch and its first step's run, without starting anything.
func recordBatch(root string, keys, checks []string, review string, briefs []string, config *loopConfig) (*loopBatch, *loopRun, error) {
	b := &loopBatch{ID: newLoopID(), Briefs: briefs, Created: time.Now().UTC(), Config: config}
	_, b.Engine = engineAncestor() // the default builder: the engine the session runs on
	if b.Config != nil {
		if err := b.Config.validate(); err != nil {
			return nil, nil, err
		}
	}
	pid, engine := engineAncestor()
	r := &loopRun{SessionEngine: engine, SessionPID: pid, SessionStarted: processStarted(pid), Config: b.Config, ID: newLoopID(), Repo: root, Session: keys, Checks: checks, Review: review,
		Created: time.Now().UTC(), Revision: 1, Batch: b.ID, Step: 1}
	b.Runs = []string{r.ID}
	if err := b.save(); err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(loopDir(r.ID), 0o700); err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(loopFile(r.ID, "brief-1.md"), []byte(batchDocument(b, "")), 0o600); err != nil {
		return nil, nil, err
	}
	return b, r, nil
}

// loopBatchRevise replaces the batch's briefs after the review found P1s or the person rejected it,
// records the session's disputes, and sends the batch back to the reviewers.
func loopBatchRevise(args []string) {
	f, rest := parseLoopFlags(args)
	if len(rest) == 0 || len(f.briefs) < 2 {
		fatal(fmt.Errorf("usage: ptln loop batch revise <loop> --brief <file> --brief <file> [...] [--dispute <finding>=<reason>]"))
	}
	for _, br := range f.briefs {
		if err := validateBrief(br); err != nil {
			fatal(err)
		}
		if err := briefIsText(br); err != nil {
			fatal(err)
		}
	}
	if err := batchRevise(rest[0], f.briefs, f.disputes); err != nil {
		fatal(err)
	}
	fmt.Printf("revised batch sent to the reviewers. %s\n", loopAdvice(mustLoop(rest)))
}

// batchRevise replaces a batch's briefs before the person has approved it. After approval the
// batch's briefs are fixed: a step's own brief is revised with `ptln loop brief`.
func batchRevise(id string, briefs []string, disputes map[string]string) error {
	err := withRunLock(id, func(r *loopRun) error {
		if !batchPending(r) || r.State != loopRevising {
			return fmt.Errorf("loop %s is %s; a batch is revised only after its review or a rejection, before it is approved (a step's brief is revised with `ptln loop brief %s --file <file>`)", r.ID, r.State, r.ID)
		}
		var found []loopTracked
		readLoopJSON(r.ID, fmt.Sprintf("batch-findings-%d.json", r.Revision), &found)
		return withBatch(r.Batch, func(b *loopBatch) error {
			ids := make([]string, 0, len(disputes))
			for id := range disputes {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			for _, id := range ids {
				reason := disputes[id]
				t, ok := trackedByID(found, id)
				if !ok {
					return fmt.Errorf("no finding %s in the last batch review", id)
				}
				t.Dispute = reason
				b.Disputed = append(b.Disputed, t)
			}
			b.Briefs = briefs
			r.Revision++
			if err := os.WriteFile(loopFile(r.ID, fmt.Sprintf("brief-%d.md", r.Revision)), []byte(batchDocument(b, "")), 0o600); err != nil {
				return err
			}
			return r.set(loopBatchRev, "the reviewers are reading the revised batch", "batch_revised", map[string]any{"steps": len(b.Briefs), "disputed": len(disputes)})
		})
	})
	if err != nil {
		return err
	}
	// Only once the revised briefs are saved: the reviewer reads them from disk.
	if err := spawnBatchReview(id); err != nil {
		if r, lerr := loadLoopRun(id); lerr == nil {
			_ = r.set(loopFailed, "could not start the batch review: "+err.Error(), "failed", nil)
		}
		return err
	}
	return nil
}

func trackedByID(ts []loopTracked, id string) (loopTracked, bool) {
	for _, t := range ts {
		if strings.EqualFold(t.ID, id) {
			return t, true
		}
	}
	return loopTracked{}, false
}

// loopBatchNext starts the step after r, which must have passed, from the brief the person approved.
func loopBatchNext(args []string) {
	prev := mustLoop(args)
	id, err := batchNextStep(prev)
	if err != nil {
		fatal(err)
	}
	if id == "" {
		fmt.Printf("that was the last step: the batch is done. `ptln loop batch status %s` summarises it for the user.\n", prev.ID)
		return
	}
	if b, err := loadBatch(prev.Batch); err == nil && b.Deferral == id {
		fmt.Printf("that was the last step. The findings deferred during the batch now wait for the user's rulings (loop %s, ctrl-\\ then a); %s\n", id, loopAdvice(mustLoop([]string{id})))
		return
	}
	fmt.Printf("step %d started as loop %s; the baseline checks run first. %s\n", prev.Step+1, id, loopAdvice(mustLoop([]string{id})))
}

// batchNextStep holds the finished step's run lock, then the batch's, in the order a stop takes
// them: a stop either lands first and is seen here, or waits until the next step exists and stops it.
func batchNextStep(prev *loopRun) (string, error) {
	var next string
	err := withRunLock(prev.ID, func(cur *loopRun) error {
		var err error
		next, err = batchNextStepLocked(cur)
		return err
	})
	if b, berr := loadBatch(prev.Batch); berr == nil {
		writeBatchSummary(b)
	}
	return next, err
}

func batchNextStepLocked(prev *loopRun) (string, error) {
	if prev.Batch == "" {
		return "", fmt.Errorf("loop %s is not a batch step (a partyline older than batches may have rewritten it); start the batch again", prev.ID)
	}
	if prev.State != loopPRReady {
		return "", fmt.Errorf("loop %s is %s; the next step starts after a batch step reaches pr_ready", prev.ID, prev.State)
	}
	// The next baseline starts from HEAD: work left uncommitted would count as the next step's.
	// memory/ and .ptln/ are not part of any step's work (snapshotExcludes), so facts written there
	// wait for the person to commit without holding the batch up.
	if out, err := gitLoop(prev.Repo, append([]string{"status", "--porcelain", "--", "."}, snapshotExcludes...)...); err != nil || strings.TrimSpace(out) != "" {
		return "", fmt.Errorf("commit step %d's work first; the working tree has changes", prev.Step)
	}
	head, err := gitMem(prev.Repo, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	// Still the batch's branch, and still on top of the step that just passed.
	if prev.Base != "" {
		if err := exec.Command("git", "-C", prev.Repo, "merge-base", "--is-ancestor", prev.Base, head).Run(); err != nil {
			return "", fmt.Errorf("HEAD %s does not descend from step %d's base %s; check out the batch's branch", shortSHA(head), prev.Step, shortSHA(prev.Base))
		}
	}
	branch, _ := gitMem(prev.Repo, "rev-parse", "--abbrev-ref", "HEAD")
	var next string
	err = withBatch(prev.Batch, func(b *loopBatch) error {
		if err := prev.checkConfig(); err != nil {
			return err
		}
		k := prev.Step // index of the next step's brief
		if b.Stopped {
			return fmt.Errorf("the user stopped batch %s; nothing was started", b.ID)
		}
		if b.Branch != "" && branch != b.Branch {
			return fmt.Errorf("the batch runs on branch %s, but %s is checked out; nothing was started", b.Branch, branch)
		}
		if prev.Step > len(b.Briefs) {
			return fmt.Errorf("loop %s reviews the batch's deferred findings; the batch is done", prev.ID)
		}
		if len(b.Runs) != k || b.Runs[k-1] != prev.ID {
			return fmt.Errorf("loop %s is not the batch's latest step", prev.ID)
		}
		// The repository is the batch's until it ends: refuse if anything else started in it.
		if other := activeLoopFor(prev.Repo); other != nil && other.ID != prev.ID {
			return fmt.Errorf("loop %s is running in this repository (%s); the batch cannot continue beside it", other.ID, other.Repo)
		}
		b.Ends = append(b.Ends[:min(len(b.Ends), k-1)], head)
		if k >= len(b.Briefs) {
			if !anyDeferred(b) {
				return nil
			}
			id, err := startDeferredReview(prev, b, head)
			next = id
			return err
		}
		if len(b.Hashes) != len(b.Briefs) || briefHash(b.Briefs[k]) != b.Hashes[k] {
			return fmt.Errorf("step %d's brief is not the one the user approved; nothing was started", k+1)
		}
		r := &loopRun{ID: newLoopID(), Repo: prev.Repo, Session: prev.Session, SessionEngine: prev.SessionEngine, SessionPID: prev.SessionPID, SessionStarted: prev.SessionStarted, Checks: prev.Checks, Review: prev.Review, Config: b.Config,
			Created: time.Now().UTC(), Revision: 1, Batch: b.ID, Step: k + 1, Base: head}
		if err := os.MkdirAll(loopDir(r.ID), 0o700); err != nil {
			return err
		}
		for _, name := range []string{"brief-1.md", approvedBriefFile} {
			if err := os.WriteFile(loopFile(r.ID, name), []byte(b.Briefs[k]), 0o600); err != nil {
				return err
			}
		}
		if err := r.set(loopBaseline, "running the checks before any work", "approved", map[string]any{"base": head, "revision": 1, "batch": b.ID, "hash": b.Hashes[k], "by": "batch approval"}); err != nil {
			return err
		}
		if err := spawnRound(r.ID, 0); err != nil {
			// Nothing will run it: leave it failed, not live, so the worktree is not held by it.
			_ = r.set(loopFailed, "could not start the baseline: "+err.Error(), "failed", nil)
			return err
		}
		b.Runs = append(b.Runs, r.ID)
		next = r.ID
		return nil
	})
	return next, err
}

// startDeferredReview ends a batch whose steps deferred P1s: a last run, after every step, puts
// them to the person one at a time in the approval popup, as ordinary disputes. A finding they
// rule "fix" goes back to the session, which fixes it on the same branch.
func startDeferredReview(prev *loopRun, b *loopBatch, head string) (string, error) {
	r := &loopRun{ID: newLoopID(), Repo: prev.Repo, Session: prev.Session, SessionEngine: prev.SessionEngine, SessionPID: prev.SessionPID, SessionStarted: prev.SessionStarted, Checks: prev.Checks, Review: prev.Review, Config: b.Config,
		Created: time.Now().UTC(), Revision: 1, Batch: b.ID, Step: len(b.Briefs) + 1, Base: head}
	if err := os.MkdirAll(loopDir(r.ID), 0o700); err != nil {
		return "", err
	}
	waiting := 0
	for _, d := range b.Deferred {
		if d.Decision == "deferred" {
			waiting++
		}
	}
	brief := fmt.Sprintf("# Deferred findings of batch %s\n\n## Goal\nRule on the %d P1s disputed during the batch; fix those ruled fix.\n\n%s", b.ID, waiting, formatTracked(b.Deferred))
	_ = os.WriteFile(loopFile(r.ID, "brief-1.md"), []byte(brief), 0o600)
	// Fixes made here are checked against the last step's baseline: what already failed before the
	// batch still does not count against it.
	if base, err := os.ReadFile(loopFile(prev.ID, "baseline.json")); err == nil {
		_ = os.WriteFile(loopFile(r.ID, "baseline.json"), base, 0o600)
	}
	writeLoopJSON(r.ID, "dispute.json", firstDeferred(b))
	writeLoopJSON(r.ID, fmt.Sprintf("findings-%d.json", r.Round), b.Deferred)
	if err := r.set(loopAwaitYou, fmt.Sprintf("%d deferred findings wait for the user", len(b.Deferred)), "deferred_review", map[string]any{"batch": b.ID}); err != nil {
		return "", err
	}
	b.Deferral = r.ID
	return r.ID, nil
}

// batchDeferredDecided records the person's ruling on a deferred finding and puts the next one to
// them, or, when none is left, sends the fixes to the session. Called under the run's lock from
// loopDecideDispute.
func batchDeferredDecided(r *loopRun, t loopTracked) error {
	var nextOne *loopTracked
	var fixes []loopTracked
	err := withBatch(r.Batch, func(b *loopBatch) error {
		for i := range b.Deferred {
			if b.Deferred[i].ID == t.ID && b.Deferred[i].Decision == "deferred" {
				b.Deferred[i].Decision = t.Decision
				break
			}
		}
		for i := range b.Deferred {
			switch d := b.Deferred[i]; {
			case d.Decision == "deferred" && nextOne == nil:
				nextOne = &b.Deferred[i]
			case d.Decision == "fix":
				fixes = append(fixes, d)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if nextOne != nil {
		writeLoopJSON(r.ID, "dispute.json", *nextOne)
		return r.set(loopAwaitYou, t.ID+" decided: "+t.Decision+"; next "+nextOne.ID, "decided", map[string]any{"finding": t.ID, "decision": t.Decision, "by": decisionProvenance()})
	}
	if b, berr := loadBatch(r.Batch); berr == nil && (b.Stopped || b.Failed) {
		// The batch ended early: the rulings are recorded and the fixes listed for the morning;
		// nothing more is built.
		return r.set(loopPRReady, fmt.Sprintf("rulings recorded; %d to fix, listed in the summary", len(fixes)), "decided", map[string]any{"finding": t.ID, "decision": t.Decision, "by": decisionProvenance()})
	}
	if len(fixes) == 0 {
		err = r.set(loopPRReady, "every deferred finding dismissed", "decided", map[string]any{"finding": t.ID, "decision": t.Decision, "by": decisionProvenance()})
		return err
	}
	if builderDriven(r) {
		if msg := builderHandOff(r, "deferred findings to fix", "The user ruled that these findings, deferred during the batch, must be fixed:\n\n"+formatTracked(fixes)); msg != "" {
			_ = loopPost(r.ID, msg)
		}
		return r.set(loopBuilding, fmt.Sprintf("%d deferred findings to fix", len(fixes)), "decided", map[string]any{"finding": t.ID, "decision": t.Decision, "by": decisionProvenance()})
	}
	err = r.set(loopBuilding, fmt.Sprintf("%d deferred findings to fix", len(fixes)), "decided", map[string]any{"finding": t.ID, "decision": t.Decision, "by": decisionProvenance()})
	if err == nil {
		err = loopPost(r.ID, "The user ruled on the batch's deferred findings. Fix these, then run `ptln loop ready "+r.ID+" --report <file>`:\n\n"+formatTracked(fixes))
	}
	return err
}

// loopBatchStatus is the morning summary: every step, where it got to, and what waits for the person.
func loopBatchStatus(args []string) {
	if len(args) == 0 {
		fatal(fmt.Errorf("usage: ptln loop batch status <batch or loop id>"))
	}
	b, err := batchByID(args[0])
	if err != nil {
		fatal(err)
	}
	fmt.Print(batchSummary(b))
}

// batchByID finds a batch by its own id or by the id of any of its steps' runs.
func batchByID(id string) (*loopBatch, error) {
	if b, err := loadBatch(id); err == nil {
		return b, nil
	}
	r, err := loadLoopRun(id)
	if err != nil || r.Batch == "" {
		return nil, fmt.Errorf("no batch or batch step %q", id)
	}
	return loadBatch(r.Batch)
}

func batchSummary(b *loopBatch) string {
	var s strings.Builder
	fmt.Fprintf(&s, "batch %s: %d steps", b.ID, len(b.Briefs))
	if b.Stopped {
		s.WriteString(", stopped by the user")
	}
	s.WriteString("\n")
	for i, br := range b.Briefs {
		fmt.Fprintf(&s, "%d. %s — ", i+1, briefTitle(br))
		r := (*loopRun)(nil)
		if i < len(b.Runs) {
			r, _ = loadLoopRun(b.Runs[i])
		}
		if r == nil {
			s.WriteString("not started\n")
			continue
		}
		fmt.Fprintf(&s, "%s after %d review rounds (loop %s)\n", r.State, r.Round, r.ID)
		if i < len(b.Ends) {
			fmt.Fprintf(&s, "   commits %s..%s\n", shortSHA(r.Base), shortSHA(b.Ends[i]))
		}
		if r.Note != "" {
			fmt.Fprintf(&s, "   %s\n", r.Note)
		}
		if r.Round > 0 {
			fmt.Fprintf(&s, "   report: %s\n", loopFile(r.ID, fmt.Sprintf("ready-%d.md", r.Round)))
		}
		s.WriteString(reviewModelsSummary(r))
		evs, _ := loopEvents(r.ID)
		for _, e := range evs {
			switch e.Type {
			case "brief_revised_in_scope":
				fmt.Fprintf(&s, "   brief revised, accepted by the scope review: %s\n", e.Note)
			case "brief_revised_out_of_scope":
				fmt.Fprintf(&s, "   brief revised beyond its scope, sent to you:\n%s\n", indent(termSafeLines(fmt.Sprint(e.Data["scope"])), "     "))
			case "brief_revised":
				fmt.Fprintf(&s, "   brief revised and sent to you\n")
			}
		}
	}
	if b.Deferral != "" {
		if r, err := loadLoopRun(b.Deferral); err == nil {
			fmt.Fprintf(&s, "Deferred findings — %s after %d review rounds (loop %s)\n", r.State, r.Round, r.ID)
			if r.Note != "" {
				fmt.Fprintf(&s, "   %s\n", r.Note)
			}
		}
	}
	if len(b.Arbitrated) > 0 {
		fmt.Fprintf(&s, "\nRuled by the arbiter (%d; it decides at confidence %.2f or more):\n", len(b.Arbitrated), arbiterBar)
		for _, a := range b.Arbitrated {
			what := map[string]string{"fix": "ruled fix", "dismiss": "dismissed", "": "not sure: sent to you"}[a.Decision]
			if a.Reversed {
				what += ", reversed by you"
			}
			fmt.Fprintf(&s, "\nstep %d %s [%s] %s: %s (confidence %.2f, bar %.2f, %s)\n  the dispute: %s\n  the arbiter: %s\n",
				a.Step, a.Finding.ID, a.Finding.Priority, termSafe(a.Finding.Claim), what, a.Confidence, a.Bar, termSafe(a.Engine), termSafe(a.Finding.Dispute), termSafe(a.Reason))
		}
	}
	if len(b.Deferred) > 0 {
		fmt.Fprintf(&s, "\nDisputed during the batch (%d):\n", len(b.Deferred))
		for _, d := range b.Deferred {
			ruling := map[string]string{"deferred": "waiting for you", "fix": "you ruled fix", "fixed": "you ruled fix; fixed and committed in the deferred review", "dismiss": "you dismissed it"}[d.Decision]
			fmt.Fprintf(&s, "\n%s\n  the session says: %s\n  %s\n", formatTracked([]loopTracked{d}), termSafe(d.Dispute), ruling)
		}
	}
	return s.String()
}

// writeBatchSummary keeps the summary on disk beside the batch, for the morning.
func writeBatchSummary(b *loopBatch) {
	_ = os.WriteFile(filepath.Join(batchDir(b.ID), "summary.txt"), []byte(batchSummary(b)), 0o600)
}

func anyDeferred(b *loopBatch) bool {
	for _, d := range b.Deferred {
		if d.Decision == "deferred" {
			return true
		}
	}
	return false
}

func firstDeferred(b *loopBatch) loopTracked {
	for _, d := range b.Deferred {
		if d.Decision == "deferred" {
			return d
		}
	}
	return loopTracked{}
}

func termSafeLines(s string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = termSafe(lines[i])
	}
	return strings.Join(lines, "\n")
}

func indent(s, by string) string { return by + strings.ReplaceAll(s, "\n", "\n"+by) }
