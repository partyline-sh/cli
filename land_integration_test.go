package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// land_integration_test.go — the train against REAL git.
//
// The unit tests in land_test.go script a fake runner. They prove the DECISIONS (refuse unverified
// work, abort on conflict, serialise) and they prove nothing at all about whether the git commands
// are correct — whether `push origin HEAD:<base>` from a worktree does what the code assumes,
// whether `rebase FETCH_HEAD` picks up a base that moved a moment ago. Those are exactly the
// assumptions that only fail in front of a customer.
//
// So this drives the same code against temp repositories: a bare "origin", a clone, and real
// worktrees with real divergent commits. Slow by this package's standards, hermetic, no network.

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// execRunner is the production shape of cmdRunner: actually run the command.
func execRunner(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// scratchRepo builds a bare origin with one commit on `main`, plus a clone to make worktrees from.
func scratchRepo(t *testing.T) (origin, clone string) {
	t.Helper()
	root := t.TempDir()
	origin = filepath.Join(root, "origin.git")
	git(t, root, "init", "--bare", "--initial-branch=main", origin)

	seed := filepath.Join(root, "seed")
	git(t, root, "clone", origin, seed)
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, seed, "add", "-A")
	git(t, seed, "commit", "-m", "seed")
	git(t, seed, "push", "origin", "main")

	clone = filepath.Join(root, "work")
	git(t, root, "clone", origin, clone)
	return origin, clone
}

// worktreeWithCommit forks a branch off origin/main and commits one file into it — a stand-in for
// what a crank worker leaves behind.
func worktreeWithCommit(t *testing.T, clone, branch, file, body string) string {
	t.Helper()
	wt := filepath.Join(filepath.Dir(clone), "wt-"+branch)
	git(t, clone, "worktree", "add", "-b", branch, wt, "origin/main")
	if err := os.WriteFile(filepath.Join(wt, file), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, wt, "add", "-A")
	git(t, wt, "commit", "-m", "work on "+branch)
	return wt
}

func originHead(t *testing.T, origin string) string {
	t.Helper()
	return git(t, origin, "rev-parse", "main")
}

// The case the train exists for: two tasks that both forked from the same commit, touching
// DIFFERENT files. Sequentially they must both land — the second replayed onto the first.
func TestTrainLandsTwoIndependentBranches(t *testing.T) {
	origin, clone := scratchRepo(t)
	before := originHead(t, origin)

	a := worktreeWithCommit(t, clone, "task-a", "a.txt", "A\n")
	b := worktreeWithCommit(t, clone, "task-b", "b.txt", "B\n")

	q := &landQueue{}
	if got := q.land(execRunner, landCandidate{branch: "task-a", wtPath: a, base: "main", verified: true, hasWork: true}); got.outcome != landed {
		t.Fatalf("first branch: %q — %s", got.outcome, got.note)
	}
	// task-b forked BEFORE task-a landed. It is now stale by exactly one commit — the condition
	// that produces a conflict later when nobody is watching. The train must replay it.
	got := q.land(execRunner, landCandidate{branch: "task-b", wtPath: b, base: "main", verified: true, hasWork: true})
	if got.outcome != landed {
		t.Fatalf("second branch (stale by one commit): %q — %s", got.outcome, got.note)
	}

	after := originHead(t, origin)
	if after == before {
		t.Fatal("origin/main never moved — nothing actually landed")
	}
	// Both files present on the base is the real proof: the second landing did not clobber the first.
	log := git(t, origin, "log", "--name-only", "--pretty=format:", "main")
	for _, f := range []string{"a.txt", "b.txt"} {
		if !strings.Contains(log, f) {
			t.Errorf("%s is missing from origin/main after both landed:\n%s", f, log)
		}
	}
}

// The other half: two tasks editing the SAME file. The first lands; the second must NOT, must abort
// cleanly, must leave the base exactly as the first one left it, and must name the conflicted file.
func TestTrainRefusesAConflictAndLeavesTheBaseIntact(t *testing.T) {
	origin, clone := scratchRepo(t)

	a := worktreeWithCommit(t, clone, "task-a", "shared.txt", "written by A\n")
	b := worktreeWithCommit(t, clone, "task-b", "shared.txt", "written by B\n")

	q := &landQueue{}
	if got := q.land(execRunner, landCandidate{branch: "task-a", wtPath: a, base: "main", verified: true, hasWork: true}); got.outcome != landed {
		t.Fatalf("first branch: %q — %s", got.outcome, got.note)
	}
	headAfterA := originHead(t, origin)

	got := q.land(execRunner, landCandidate{branch: "task-b", wtPath: b, base: "main", verified: true, hasWork: true})
	if got.outcome != landConflict {
		t.Fatalf("second branch: %q — want %q (%s)", got.outcome, landConflict, got.note)
	}
	if len(got.conflicts) == 0 || !strings.Contains(strings.Join(got.conflicts, ","), "shared.txt") {
		t.Errorf("conflicts = %v, want shared.txt named as measured evidence", got.conflicts)
	}
	if originHead(t, origin) != headAfterA {
		t.Fatal("a conflicting branch CHANGED THE BASE — it must never reach origin")
	}
	// The branch has to survive intact for a human, and the worktree must not be left mid-rebase.
	if st := git(t, b, "status", "--porcelain"); st != "" {
		t.Errorf("worktree left dirty after the aborted rebase:\n%s", st)
	}
	if body := git(t, b, "show", "HEAD:shared.txt"); body != "written by B" {
		t.Errorf("the agent's work was altered: %q", body)
	}
}
