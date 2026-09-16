package main

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"partyline.sh/partyline/internal/gitwt"
)

// freshen.go — epic merge-conflicts, slice C2: rebase a task's branch onto the CURRENT base before
// it is pushed or verified.
//
// A crank task forks `origin/<base>` when its worktree is created and never re-syncs. A forty-minute
// task is therefore forty minutes stale by construction, and a task that ran while a teammate merged
// is stale by exactly the thing that will conflict with it.
//
// This is one of only TWO measures that survive the human-work problem (constraint #353). Planning-
// time chaining can only avoid collisions between tasks partyline itself planned; it is blind to a
// teammate's PR and completely blind to a direct push to the base, where no PR exists to scan
// against. Rebasing re-syncs against whatever actually moved the base, regardless of who moved it.
//
// WHERE IT SITS: commit → freshen → push → verify → merge policy. That order is load-bearing.
// Rebasing before the push means the branch is published in its final shape and no force-push is
// ever needed; rebasing before verify means the gate validates the code as it will actually land,
// not as it looked against a base that has since moved.
//
// It does NOT resolve conflicts. On a conflicting rebase it aborts, leaves the branch exactly as the
// agent wrote it, and says so — the work stays intact and reviewable, and resolution is the repair
// ladder's job (slice C3), where it can be verified and escalated to a human. Silently reconciling
// someone else's change here, with no gate and no attribution, is the one thing this must not do.

const freshenTimeout = 90 * time.Second // a fetch on a large repo, not a build

// freshenBranch brings the worktree's branch up to date with the base. Returns a human note (empty
// when the branch was already current and nothing happened) and whether the branch is now fresh.
//
// `ok == false` means the branch could NOT be made fresh — it conflicts with the base. That is not a
// failure of the task: the agent's work is untouched and still gets pushed and reviewed. It is a
// signal that a human (or the repair ladder) has to reconcile it.
func freshenBranch(repo, wtPath, base string) (note string, ok bool) {
	if base == "" {
		base = gitwt.DefaultBaseName(repo)
	}
	// The base can arrive from project settings, so it is untrusted on its way to an argv. Same
	// regex the delta reporter uses: alphanumeric first character (nothing readable as a flag) and
	// no shell metacharacters at all.
	if !branchDeltaRe.MatchString(base) {
		return "", true // unusable base name: skip silently rather than fail a task over it
	}

	git := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), freshenTimeout)
		defer cancel()
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", wtPath}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}

	// Fetch into FETCH_HEAD rather than relying on refs/remotes/origin/<base> being configured or
	// current. This is exactly the mistake that shipped in envdelta: reading a remote-tracking ref
	// that nothing had refreshed, and reporting the stale answer with total confidence.
	if out, err := git("fetch", "origin", base); err != nil {
		// Offline, or no remote. The task is unaffected — it just builds on the base it forked from,
		// which is the behaviour before this existed.
		return "could not fetch " + base + " (" + firstLine(out, err) + ") — building on the forked base", true
	}

	behind, err := git("rev-list", "--count", "HEAD..FETCH_HEAD")
	if err != nil {
		return "", true
	}
	n, _ := strconv.Atoi(strings.TrimSpace(behind))
	if n == 0 {
		return "", true // already current — the common case, and it should cost nothing to say
	}

	if out, rerr := git("rebase", "FETCH_HEAD"); rerr != nil {
		// Abort restores the branch to exactly what the agent produced. Leaving a worktree mid-rebase
		// would strand the task in a state nothing downstream understands.
		_, _ = git("rebase", "--abort")
		return fmt.Sprintf("conflicts with %s (moved %d commit(s) while this task ran) — needs a rebase before it can merge: %s",
			base, n, firstLine(out, rerr)), false
	}
	return fmt.Sprintf("rebased onto %s (%d new commit(s) while this task ran)", base, n), true
}
