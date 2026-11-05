package gitwt

import (
	"errors"
	"os/exec"
	"strconv"
	"strings"
)

// Probing what a finished task's branch actually IS (#602).
//
// partyline knows a branch's NAME and nothing about its STATE, so after a task finishes nothing can
// answer "did this produce anything?", "is it still there?" or "has it landed?" — finished work
// looks unfinished indefinitely and nothing downstream can safely reclaim anything.
//
// LOCAL GIT ONLY, deliberately. "Merged" is `git merge-base --is-ancestor <branch> <base>` after a
// fetch, not "a pull request object says merged": no credentials, no host API, identical against
// GitHub, GitLab, Gitea or a bare remote nobody here has heard of. A probe that lags by one fetch is
// acceptable; containment in the base branch is the fact that matters.
//
// This package REPORTS. It deletes nothing — the janitor is a separate job, and it depends on this
// being right first.

// Containment is the tri-state answer to "is this branch already in the base?". Unknown is a real
// answer, not a failure mode to be flattened: "not merged" and "could not tell" are different facts,
// and collapsing them is how a reaper later deletes live work.
type Containment string

const (
	ContainedUnknown Containment = "unknown"  // no remote, fetch failed, ref missing — we cannot say
	ContainedYes     Containment = "merged"   // the branch tip is an ancestor of the base
	ContainedNo      Containment = "unmerged" // the base does not contain the branch tip
)

// BranchState is what a finished task's branch looks like on disk and against its base.
//
// Counted separates "the worker changed nothing" (Counted, CommitsAhead 0 — worth seeing plainly)
// from "we could not compare against a base at all" (!Counted), for exactly the reason Contained
// has an Unknown: a zero that means "we didn't look" is a lie that reads as a fact.
type BranchState struct {
	Base           string // the ref we compared against ("origin/main"); "" = could not resolve one
	CommitsAhead   int
	FilesChanged   int
	Insertions     int
	Deletions      int
	Counted        bool // the ahead-count + diffstat actually ran (else every number above is meaningless)
	WorktreeExists bool // the task's linked worktree is still on disk
	Contained      Containment
}

// ProbeBranchState reports the state of `branch` in `repo` against `base` (the project's configured
// base branch; "" → the remote's default). Never errors: everything it cannot determine comes back
// as Unknown/false, because a caller acting on a wrong "no" is worse than one acting on "I can't
// tell". Read-only — it fetches and asks questions, and mutates nothing.
func ProbeBranchState(repo, branch, base string) BranchState {
	st := BranchState{Contained: ContainedUnknown}
	branch = strings.TrimSpace(branch)
	if repo == "" || branch == "" {
		return st
	}
	st.WorktreeExists = IsLinkedWorktree(Path(repo, branch))

	ref := branchRef(repo, branch)
	if ref == "" {
		return st // the branch is gone (deleted, or never created) — nothing to compare
	}
	// The fetch lives in RemoteBase/defaultBranchRef, so the comparison is against the CURRENT base
	// where the network allows and a cached ref where it doesn't. Offline is a lag, not a lie.
	baseRef := ""
	if strings.TrimSpace(base) != "" {
		baseRef, _ = RemoteBase(repo, base)
	} else {
		baseRef = defaultBranchRef(repo)
	}
	if baseRef == "" {
		return st // no reachable base: UNKNOWN, never "not merged"
	}
	st.Base = baseRef

	// The count and the diffstat are ONE fact, so they are set together or not at all: a
	// CommitsAhead left standing next to Counted=false is a number a caller can read without the
	// flag that says it means nothing.
	if n, ok := commitsAhead(repo, baseRef, ref); ok {
		if files, ins, del, dok := diffStat(repo, baseRef, ref); dok {
			st.CommitsAhead, st.FilesChanged, st.Insertions, st.Deletions, st.Counted = n, files, ins, del, true
		}
	}
	st.Contained = contained(repo, ref, baseRef)
	return st
}

// branchRef resolves the branch to a ref we can ask questions about: the local branch when it
// exists, else the remote-tracking copy (a pushed branch whose worktree was already torn down).
func branchRef(repo, branch string) string {
	if hasLocalBranch(repo, branch) {
		return "refs/heads/" + branch
	}
	if hasRemoteBranch(repo, branch) {
		return "refs/remotes/origin/" + branch
	}
	return ""
}

// commitsAhead counts commits on ref that base does not have. 0 with ok=true is the meaningful
// answer this whole probe exists for: the worker produced nothing.
func commitsAhead(repo, base, ref string) (int, bool) {
	out, err := git(repo, "rev-list", "--count", base+".."+ref)
	if err != nil {
		return 0, false
	}
	n, cerr := strconv.Atoi(strings.TrimSpace(out))
	if cerr != nil {
		return 0, false
	}
	return n, true
}

// diffStat sums the branch's changes since it forked (three-dot: the branch's own work, not the
// base's movement since). A binary file's "-" counts as a changed file with no line counts.
func diffStat(repo, base, ref string) (files, ins, del int, ok bool) {
	out, err := git(repo, "diff", "--numstat", base+"..."+ref)
	if err != nil {
		return 0, 0, 0, false
	}
	for _, ln := range strings.Split(out, "\n") {
		f := strings.Fields(ln)
		if len(f) < 3 {
			continue
		}
		files++
		if n, err := strconv.Atoi(f[0]); err == nil {
			ins += n
		}
		if n, err := strconv.Atoi(f[1]); err == nil {
			del += n
		}
	}
	return files, ins, del, true
}

// contained asks git whether base already holds ref. Exit 1 is git's answer "no"; ANY other failure
// (bad ref, no such object, git itself unhappy) is "I don't know" and must not read as "no".
func contained(repo, ref, base string) Containment {
	err := gitCmd(repo, "merge-base", "--is-ancestor", ref, base).Run()
	if err == nil {
		return ContainedYes
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return ContainedNo
	}
	return ContainedUnknown
}
