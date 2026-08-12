package main

import "testing"

// The teardown table (#641). Four outcomes, and each row is a decision about someone's work — so
// the rows that KEEP things matter as much as the rows that delete.

func TestPlanTeardown(t *testing.T) {
	cases := []struct {
		name       string
		end        taskEnd
		wantWT     bool
		wantBranch bool
	}{
		{
			// The only case where deleting a branch is right, and it is safe precisely because
			// there is no commit on it to lose.
			name:   "no commit — nothing is worth keeping",
			end:    taskEnd{committed: false, mergePolicy: "pr"},
			wantWT: true, wantBranch: true,
		},
		{
			// Retention here is CORRECT: a human has to inspect the branch. The bug this issue is
			// about is retention with no stated reason, not retention itself.
			name: "quarantined — a human needs the branch AND the worktree",
			end:  taskEnd{committed: true, quarantined: true, mergePolicy: "pr"},
		},
		{
			name:   "PR opened — the worktree is a redundant copy of a pushed branch",
			end:    taskEnd{committed: true, prOpened: true, mergePolicy: "pr"},
			wantWT: true,
		},
		{
			// manual leaves a local branch BY DESIGN. Treating "no PR" as failure here would
			// retain a worktree on every single successful manual task.
			name:   "manual policy — a local branch is the intended outcome, not a failure",
			end:    taskEnd{committed: true, mergePolicy: "manual"},
			wantWT: true,
		},
		{
			name:   "no policy set behaves like manual",
			end:    taskEnd{committed: true, mergePolicy: ""},
			wantWT: true,
		},
		{
			// The case that produced the issue: gh lacked PR scope, the PR never opened, and the
			// worktree was orphaned silently with committed work in it.
			name: "pr policy but no PR — the work is real, so nothing is deleted",
			end:  taskEnd{committed: true, mergePolicy: "pr"},
		},
		{
			name: "auto policy but no PR — same as pr",
			end:  taskEnd{committed: true, mergePolicy: "auto"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := planTeardown(c.end)
			if got.removeWorktree != c.wantWT {
				t.Errorf("removeWorktree = %v, want %v", got.removeWorktree, c.wantWT)
			}
			if got.removeBranch != c.wantBranch {
				t.Errorf("removeBranch = %v, want %v", got.removeBranch, c.wantBranch)
			}
			if got.reason == "" {
				t.Error("every outcome must state a reason — a retained orphan with no reason IS the bug")
			}
		})
	}
}

func TestABranchIsOnlyEverDeletedWhenNothingWasCommitted(t *testing.T) {
	// The invariant worth pinning on its own, because it is the one whose violation is
	// unrecoverable: a deleted branch with commits on it is lost work. Everything else this file
	// decides is reversible.
	for _, e := range []taskEnd{
		{committed: true, mergePolicy: "pr"},
		{committed: true, mergePolicy: "manual"},
		{committed: true, mergePolicy: "auto"},
		{committed: true, prOpened: true, mergePolicy: "pr"},
		{committed: true, quarantined: true, mergePolicy: "pr"},
	} {
		if planTeardown(e).removeBranch {
			t.Fatalf("would delete a branch holding commits: %+v", e)
		}
	}
}

func TestQuarantineNeverLosesEitherArtifact(t *testing.T) {
	// A quarantined task exists to be reviewed. Removing either half makes the review impossible,
	// and the merge policy must not change that.
	for _, pol := range []string{"manual", "pr", "auto", ""} {
		p := planTeardown(taskEnd{committed: true, quarantined: true, mergePolicy: pol})
		if p.removeWorktree || p.removeBranch {
			t.Fatalf("policy %q: quarantined task lost an artifact: %+v", pol, p)
		}
	}
}
