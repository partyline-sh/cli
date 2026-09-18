package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"partyline.sh/partyline/internal/gate"
	"partyline.sh/partyline/internal/surface"
)

// Against a REAL repository, not a fake. The whole claim is "we can tell whether a lane touched
// the worktree", and a fake git would only prove that the code agrees with my model of git rather
// than with git.

func roRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		// Identity in the REPO and in the env: a runner has no global user.name, which is exactly
		// how a green laptop turned into a red CI run earlier today.
		c.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.name", "t")
	run("config", "user.email", "t@t")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-qm", "base")
	return dir
}

func roWrite(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", dir}, args...)...)
	c.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestReadOnlyPassesWhenNothingChanged(t *testing.T) {
	dir := roRepo(t)
	before := snapshotWorktree(dir)
	// A lane that only READS. Reading a file must not register as a mutation.
	if _, err := os.ReadFile(filepath.Join(dir, "a.txt")); err != nil {
		t.Fatal(err)
	}
	p := compareWorktree(before, snapshotWorktree(dir))
	if !p.Observed {
		t.Fatal("could not observe the worktree — the test cannot make its point")
	}
	if !p.Passed {
		t.Errorf("a read-only lane was reported as mutating: %+v", p)
	}
	if got := readOnlyLane("read-only", p); got.Status != gate.StatusPass {
		t.Errorf("lane status = %q, want pass", got.Status)
	}
}

func TestReadOnlyCatchesAnEdit(t *testing.T) {
	dir := roRepo(t)
	before := snapshotWorktree(dir)
	roWrite(t, dir, "a.txt", "one\ntwo\n") // the lane edited the code it was judging
	p := compareWorktree(before, snapshotWorktree(dir))
	if p.Passed {
		t.Fatal("an edited worktree passed the read-only proof")
	}
	lane := readOnlyLane("read-only", p)
	if lane.Status != gate.StatusFail || lane.Code != surface.CodeReadOnlyMutated {
		t.Errorf("lane = %q/%q, want fail/%s", lane.Status, lane.Code, surface.CodeReadOnlyMutated)
	}
	if !lane.Blocking {
		t.Error("a mutation must BLOCK — a verdict about code the judge edited is not a verdict")
	}
	if !containsPath(p.Changed, "a.txt") {
		t.Errorf("changed files = %v, want a.txt named as evidence", p.Changed)
	}
}

// THE CASE A STATUS-ONLY CHECK MISSES, and the reason this records three signals rather than one.
// A lane that commits its edits leaves a CLEAN worktree: `git status` matches before and after, so
// a status hash alone reports the tree as untouched while the history has moved underneath it.
func TestReadOnlyCatchesALaneThatCommits(t *testing.T) {
	dir := roRepo(t)
	before := snapshotWorktree(dir)
	roWrite(t, dir, "a.txt", "one\nsneaky\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-qm", "the reviewer committed its own edit")

	after := snapshotWorktree(dir)
	if before.statusHash != after.statusHash {
		t.Fatal("precondition broken: the tree should be CLEAN again after committing, " +
			"which is what makes this case invisible to a status-only check")
	}
	p := compareWorktree(before, after)
	if p.Passed {
		t.Fatal("a lane that COMMITTED its edits passed the read-only proof")
	}
	if got := readOnlyLane("read-only", p).Detail; !strings.Contains(got, "committed") {
		t.Errorf("detail = %q, want it to name the commit — 'something changed' is not actionable", got)
	}
}

// The other way to hide an edit: park it. Stash leaves a clean tree and an unmoved HEAD.
func TestReadOnlyCatchesAStash(t *testing.T) {
	dir := roRepo(t)
	before := snapshotWorktree(dir)
	roWrite(t, dir, "a.txt", "one\nparked\n")
	gitRun(t, dir, "stash", "push", "-q", "-m", "hidden")

	after := snapshotWorktree(dir)
	if before.head != after.head {
		t.Fatal("precondition broken: a stash must not move HEAD")
	}
	if compareWorktree(before, after).Passed {
		t.Fatal("a stashed edit passed the read-only proof")
	}
}

// Absence of evidence is not evidence of read-only-ness. If git cannot be read we make NO claim,
// and the lane is reported as skipped rather than as a pass — the same rule the gate applies to a
// repo that configured no checks.
func TestUnobservableWorktreeMakesNoClaim(t *testing.T) {
	notARepo := t.TempDir()
	st := snapshotWorktree(notARepo)
	if st.observed {
		t.Fatal("claimed to observe a directory that is not a git repository")
	}
	p := compareWorktree(st, st)
	if p.Observed || p.Passed {
		t.Errorf("unobservable state reported as observed=%v passed=%v; both must be false", p.Observed, p.Passed)
	}
	lane := readOnlyLane("read-only", p)
	if lane.Status != gate.StatusSkip {
		t.Errorf("lane status = %q, want skip — we could not look, so we assert nothing", lane.Status)
	}
}

func containsPath(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle || strings.HasSuffix(h, "/"+needle) {
			return true
		}
	}
	return false
}
