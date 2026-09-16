package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// P0 of provisioned workers (docs/plans/provisioned-workers.md): the push/PR gate must fire on a
// branch that is AHEAD of the base, regardless of who committed. The old gate only passed when
// crank's own commitWorktree landed a commit — a worker agent that committed its work itself left
// commitWorktree seeing a clean tree ("no changes") and the whole verify+push+PR block was
// skipped, stranding a finished branch locally. branchAhead is the branch-deliverability test.

// tgit runs git in dir with a fixed identity, failing the test on error.
func tgit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return string(out)
}

// seedCloneWithOrigin builds a seed repo with one commit, bare-clones it as the origin, and
// clones that origin into a working clone (which is what gives us refs/remotes/origin/HEAD —
// the same shape a crank worktree sees through the shared .git).
func seedCloneWithOrigin(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	seed := filepath.Join(tmp, "seed")
	if err := os.Mkdir(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	tgit(t, seed, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(seed, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tgit(t, seed, "add", "-A")
	tgit(t, seed, "commit", "-m", "init")
	origin := filepath.Join(tmp, "origin.git")
	tgit(t, tmp, "clone", "--bare", seed, origin)
	work := filepath.Join(tmp, "work")
	tgit(t, tmp, "clone", origin, work)
	return work
}

func TestBranchAheadZeroOnCleanClone(t *testing.T) {
	work := seedCloneWithOrigin(t)
	if n := branchAhead(work); n != 0 {
		t.Fatalf("clean clone: branchAhead = %d, want 0", n)
	}
}

func TestBranchAheadCountsAgentCommits(t *testing.T) {
	work := seedCloneWithOrigin(t)
	// The agent-committed scenario: a task branch with a commit crank did NOT make, tree clean.
	tgit(t, work, "checkout", "-b", "crank-01-thing")
	if err := os.WriteFile(filepath.Join(work, "b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tgit(t, work, "add", "-A")
	tgit(t, work, "commit", "-m", "agent did this itself")
	if got := commitWorktree(work, "crank: thing"); got != "no changes" {
		t.Fatalf("commitWorktree on clean tree = %q, want \"no changes\" (the stranding precondition)", got)
	}
	if n := branchAhead(work); n != 1 {
		t.Fatalf("agent-committed branch: branchAhead = %d, want 1", n)
	}
}

func TestBranchAheadFailSafeWithoutOrigin(t *testing.T) {
	// No origin at all (e.g. a bare local project) → 0, so the gate degrades to the old
	// commit-note-only behavior instead of pushing somewhere that doesn't exist.
	tmp := t.TempDir()
	tgit(t, tmp, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(tmp, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tgit(t, tmp, "add", "-A")
	tgit(t, tmp, "commit", "-m", "init")
	if n := branchAhead(tmp); n != 0 {
		t.Fatalf("repo without origin: branchAhead = %d, want 0", n)
	}
}
