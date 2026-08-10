package main

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/gitwt"
)

// rebase_job.go — Slice A2, the RESOLUTION half of conflict-aware review. A hidden preset:"rebase"
// run (dispatched by the ConflictBanner's button, pinned to the run's BUILDER) rebases the target's
// PR branch(es) onto the project base, resolves conflict markers with a WORKER pass when the rebase
// stops, force-pushes with lease, re-scans conflicts, and reports the fresh (usually empty) conflict
// set on the target's task — which is what clears the merge gate.
//
// Pinned to the builder on purpose: a rebase force-pushes, and the builder is the one machine that
// may also hold the branch's worktree — one writer, no stale-resume races. Reference-not-command
// holds throughout: the event carries only the target run id; branches come from our own store via
// ReviewTarget, and every git ref is validated against branchRe shape before it reaches an argv.

// The resolution is verified before it is published, same as any other route to a mergeable branch.
// Generous, because this runs the project's real acceptance checks plus an adversarial reviewer.
const rebaseVerifyTimeout = 20 * time.Minute

const rebaseResolveRounds = 5 // conflict-stop → worker-resolve → continue, at most this many times

func runRebaseJob(d daemonDevice, ev api.RunEvent) error {
	fail := func(stage string, e error) error {
		_ = api.SetRunStatus(d.Base, d.Token, ev.RunID, "failed", stage+": "+e.Error())
		return fmt.Errorf("%s: %w", stage, e)
	}
	reg := loadDaemonRegistry()
	_, dir, err := resolveRun(reg, runRefFromEvent(ev))
	if err != nil {
		return fail("resolve", err)
	}
	if ev.RebaseOf == "" {
		return fail("rebase", fmt.Errorf("no target run"))
	}
	targetID, tasks, _, _, err := api.ReviewTarget(d.Base, d.Token, ev.RunID) // widened route: resolves rebase_of for preset "rebase"; criteria + canon unused here
	if err != nil {
		return fail("target", err)
	}
	base := strings.TrimSpace(ev.BaseBranch)
	if base == "" {
		base = gitwt.DefaultBaseName(dir)
	}

	_ = api.SetRunStatus(d.Base, d.Token, ev.RunID, "running", "")
	logger := newRunLoggerWith(d.Base, d.Token, ev.RunID)
	logln := logger.sink(0)
	defer logger.close()

	tok := mergeGitHubToken(ev.RunID)
	repoRunner := realRunner(dir, tok)

	// Current statuses of the target's tasks — the conflict upsert must echo the task's OWN status
	// (the tasks route requires one), never invent a transition.
	statusByIdx := map[int]string{}
	if cur, err := api.ListRunTasks(d.Base, d.Token, targetID); err == nil {
		for _, t := range cur {
			statusByIdx[t.Idx] = t.Status
		}
	}

	rebased, stuck := 0, 0
	for _, t := range tasks {
		branch := strings.TrimSpace(t.Branch)
		if branch == "" || strings.HasPrefix(branch, "-") {
			continue
		}
		logln(fmt.Sprintf("rebasing %s onto origin/%s…", branch, base))
		wtPath, err := rebaseBranch(dir, branch, base, logln)
		if err != nil {
			// C3: a branch that will not converge BLOCKS ITS OWN TASK; it does not fail the run.
			// Failing meant one stubborn branch discarded the successful rebases beside it and the
			// whole thing read as a crash, when what actually happened is "this one needs a human".
			// Blocked routes to the same review-and-approve path every other quarantine uses.
			logln("⚠ " + branch + ": " + err.Error())
			_ = api.UpsertRunTask(d.Base, d.Token, targetID, api.RunTaskUpdate{
				Idx: t.Idx, Task: t.Task, Status: "blocked",
				Detail: "rebase could not be resolved automatically: " + err.Error() +
					"\n\nThe branch is untouched. Rebase it by hand, or re-plan the task against the current base.",
			})
			stuck++
			continue
		}

		// C3: VERIFY THE RESOLUTION BEFORE PUBLISHING IT. This force-pushes over a branch a human may
		// already have reviewed, and the conflict markers were resolved by an agent that was given no
		// way to run a test. Every other route to a mergeable branch passes T2a/T2b; this one used to
		// skip both and push on the worker's word that the code "looks consistent".
		//
		// A failing gate does NOT push: the remote keeps the pre-rebase branch, so the worst case is
		// the conflict you already had, never a broken branch published over a working one.
		if vr := verifyTask(dir, wtPath, base, t.Task, ev.Engine, rebaseVerifyTimeout, visualCfg{}); vr.ran && !vr.ok {
			logln("⚠ " + branch + ": the rebase resolved, but verify rejected the result — not pushing")
			_ = api.UpsertRunTask(d.Base, d.Token, targetID, api.RunTaskUpdate{
				Idx: t.Idx, Task: t.Task, Status: "blocked",
				Detail: "the conflict was resolved but the result failed the verify gate, so it was NOT pushed " +
					"(the branch on the remote is unchanged):\n\n" + vr.reasons,
			})
			stuck++
			continue
		}

		// Publish: with-lease so a concurrent push (shouldn't exist — one writer) aborts us, not them.
		if out, err := realRunner(wtPath, tok)("git", "push", "--force-with-lease", "origin", branch); err != nil {
			return fail("push "+branch, fmt.Errorf("%s", firstLine(out, err)))
		}
		logln("⤴ force-pushed " + branch)
		rebased++

		// Re-scan against the other open PRs and report on the TARGET task — this is what clears (or
		// honestly re-arms) the merge gate. Echo the task's existing status; never invent one.
		conflicts, checked := scanPRConflicts(repoRunner, branch, base)
		status := statusByIdx[t.Idx]
		if status == "" {
			status = "done"
		}
		_ = api.UpsertRunTask(d.Base, d.Token, targetID, api.RunTaskUpdate{
			Idx: t.Idx, Task: t.Task, Status: status,
			Conflicts: conflicts, ConflictsChecked: checked,
		})
		if checked && len(conflicts) == 0 {
			logln("✓ conflict-free after rebase")
		} else if checked {
			logln(fmt.Sprintf("⚠ still conflicts with %d PR(s) after rebase", len(conflicts)))
		}
	}
	if rebased == 0 && stuck == 0 {
		return fail("rebase", fmt.Errorf("the target run has no pushed branch to rebase"))
	}
	// Done even when some branches are stuck: the repair job itself succeeded at what it could do,
	// and the branches it could not resolve are recorded as blocked on the TARGET run, where a human
	// is already looking. Failing here would bury that detail under a red run with no next step.
	msg := fmt.Sprintf("rebased %d branch(es) onto %s", rebased, base)
	if stuck > 0 {
		msg += fmt.Sprintf(" · %d needs a human", stuck)
	}
	_ = api.SetRunStatus(d.Base, d.Token, ev.RunID, "done", msg)
	return nil
}

// rebaseBranch rebases one branch onto origin/<base> in its worktree, resolving conflict stops with
// a worker pass (read/edit tools, no bash — conflict markers are file edits). Returns the worktree
// path on success; aborts the rebase and errors when the worker can't converge.
func rebaseBranch(repo, branch, base string, logln func(string)) (string, error) {
	// The REMOTE branch is canonical (it's what the PR serves) — a stale local branch or dirty
	// leftover worktree must never win. Create/reuse the worktree, then hard-align it to origin.
	wtPath, _, err := gitwt.CreateFrom(repo, branch, "")
	if err != nil {
		return "", fmt.Errorf("worktree: %w", err)
	}
	git := func(args ...string) (string, error) {
		out, err := exec.Command("git", append([]string{"-C", wtPath, "-c", "core.editor=true"}, args...)...).CombinedOutput()
		return string(out), err
	}
	if out, err := git("fetch", "origin", "refs/heads/"+branch, "refs/heads/"+base); err != nil {
		return "", fmt.Errorf("fetch: %s", firstLine(out, err))
	}
	if out, err := git("reset", "--hard", "origin/"+branch); err != nil {
		return "", fmt.Errorf("align to origin: %s", firstLine(out, err))
	}

	_, err = git("rebase", "origin/"+base)
	for round := 0; err != nil && round < rebaseResolveRounds; round++ {
		conflicted, _ := git("diff", "--name-only", "--diff-filter=U")
		files := strings.Fields(strings.TrimSpace(conflicted))
		if len(files) == 0 {
			// Stopped for some other reason (detached state, hook failure) — not resolvable here.
			_, _ = git("rebase", "--abort")
			return "", fmt.Errorf("rebase stopped without conflict markers — needs a human")
		}
		logln(fmt.Sprintf("resolving %d conflicted file(s): %s", len(files), strings.Join(files, ", ")))
		prompt := "This git worktree is MID-REBASE onto origin/" + base + " and these files contain conflict markers:\n\n  " +
			strings.Join(files, "\n  ") +
			"\n\nResolve every conflict: keep BOTH the branch's intent and the base's changes, remove all conflict markers, and keep the code consistent and compiling. Edit files only — do NOT run git commands; the harness continues the rebase itself."
		if _, werr := runWorker(wtPath, prompt, "claude", "", "", false, 10*time.Minute, logln, ""); werr != nil {
			_, _ = git("rebase", "--abort")
			return "", fmt.Errorf("resolve worker: %w", werr)
		}
		if out, aerr := git("add", "-A"); aerr != nil {
			_, _ = git("rebase", "--abort")
			return "", fmt.Errorf("stage resolution: %s", firstLine(out, aerr))
		}
		_, err = git("rebase", "--continue")
	}
	if err != nil {
		_, _ = git("rebase", "--abort")
		return "", fmt.Errorf("conflicts didn't converge after %d resolve rounds — needs a human", rebaseResolveRounds)
	}
	return wtPath, nil
}
