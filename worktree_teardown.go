package main

import (
	"fmt"

	"partyline.sh/partyline/internal/gitwt"
)

// What happens to a crank task's worktree and branch when the task ends (#641).
//
// crank creates a worktree AND a branch per task and never removed either. When a PR opens the
// branch at least has a lifecycle (GitHub deletes it on merge); when the PR does NOT open, both
// orphan permanently. A cleanup pass found 21 of 66 remaining branches were crank worktree
// branches, with 35 worktrees live on one machine — and a second-order effect where every orphan
// becomes a top-level project in the session switchboard.
//
// The mechanics already existed in gitwt. What was missing is the DECISION, which is why the pure
// part of it lives here on its own and is unit-tested: it is a small table with four outcomes and
// one absolute rule, and getting a row wrong means either accumulating junk forever or deleting
// someone's work.

// taskEnd is everything the teardown decision depends on. A struct rather than five bools in a
// signature because the call site reads better and a new outcome dimension does not silently
// re-order arguments.
type taskEnd struct {
	committed   bool   // the worker produced at least one commit on the branch
	quarantined bool   // a verify gate rejected it (Trust · T2)
	prOpened    bool   // merge policy opened a PR (its URL is on the result)
	mergePolicy string // manual | pr | auto — "no PR" means different things per policy
}

// teardownPlan is the decision, separated from the doing so it can be tested without a git repo.
type teardownPlan struct {
	removeWorktree bool
	removeBranch   bool
	reason         string // always populated — a retained orphan with no stated reason is the bug
}

// planTeardown maps a finished task to what should survive it.
//
// THE ONE ABSOLUTE RULE lives in tearDownTask, not here: never remove a worktree with uncommitted
// changes, whatever this plan says. A cleanup pass found a worktree holding 23 uncommitted files,
// and gitwt.Remove force-removes — so the dirty check is a veto over this table, not a row in it.
func planTeardown(e taskEnd) teardownPlan {
	switch {
	// Nothing was produced. The branch is an empty pointer at the base commit and the worktree is a
	// checkout of it — neither is evidence of anything. This is the only case where deleting the
	// BRANCH is right, and it is safe precisely because there is no commit to lose.
	case !e.committed:
		return teardownPlan{removeWorktree: true, removeBranch: true, reason: "no commit — nothing to keep"}

	// Quarantined by a verify gate. The whole point of quarantine is that a human inspects the
	// branch, so retention here is CORRECT rather than a leak — but it has to be stated, or it is
	// indistinguishable from the orphaning this issue is about.
	case e.quarantined:
		return teardownPlan{reason: "quarantined by the verify gate — worktree and branch kept for review"}

	// A PR opened: the work has a home and a reviewer can see it on GitHub. The worktree is now a
	// redundant local copy of a pushed branch.
	case e.prOpened:
		return teardownPlan{removeWorktree: true, reason: "PR opened — worktree removed, branch lives until the PR merges"}

	// merge_policy manual leaves a reviewable local branch BY DESIGN — "no PR" is not a failure
	// here, so this is a clean success and the worktree is redundant.
	case e.mergePolicy == "manual" || e.mergePolicy == "":
		return teardownPlan{removeWorktree: true, reason: "committed on a local branch (merge policy: manual) — worktree removed"}

	// pr/auto policy, committed, but no PR: push or `gh pr create` failed. The work is REAL, so
	// nothing is deleted — and the worktree stays too, because the recovery someone will want is to
	// go into it and push by hand. Retained with a reason, and the run separately routes to review
	// via crankResult.noPR.
	default:
		return teardownPlan{reason: "committed but no PR opened (push or gh failed) — everything kept so the work can be recovered"}
	}
}

// tearDownTask applies the plan, and refuses to delete anything that still holds uncommitted work.
// Returns a note for the task's record — always non-empty, because "what happened to my worktree"
// should never require inspecting the disk to answer.
func tearDownTask(repo, wtPath, branch string, e taskEnd) string {
	plan := planTeardown(e)

	// The veto. gitwt.Remove force-removes and falls back to os.RemoveAll, so this check is the
	// only thing between an automated reaper and someone's uncommitted work. gitwt.Dirty fails
	// dirty on error — before an irreversible delete, "I can't tell" must mean "don't".
	if plan.removeWorktree && gitwt.Dirty(wtPath) {
		return "worktree kept — it has uncommitted changes (" + plan.reason + ")"
	}

	note := plan.reason
	if plan.removeWorktree {
		if err := gitwt.Remove(repo, wtPath); err != nil {
			// Never fatal: the task's actual work is already decided, and a worktree we could not
			// remove is a disk-hygiene problem for `ptln wt prune`, not a reason to fail a build.
			return fmt.Sprintf("%s · could not remove the worktree: %v", note, err)
		}
		if plan.removeBranch {
			// Best-effort and deliberately after Remove: git refuses to delete a branch that is
			// still checked out in a worktree.
			_ = gitwt.DeleteBranch(repo, branch)
		}
	}
	return note
}
