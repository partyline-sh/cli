package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Real origin + real clone, and the remote MOVES between fork and rebase. That is the only state
// that exists in production, and the one no self-contained fixture reproduces — the exact blind spot
// that let envdelta ship reading refs nothing had refreshed.

func gitAt(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com",
		"GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func fWrite(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// origin with `main`, plus a clone whose task branch forked before the remote moved.
func freshenFixture(t *testing.T) (origin, clone string) {
	t.Helper()
	origin = t.TempDir()
	gitAt(t, origin, "init", "-q", "-b", "main")
	fWrite(t, origin, "base.txt", "base\n")
	gitAt(t, origin, "add", ".")
	gitAt(t, origin, "commit", "-m", "base")

	clone = filepath.Join(t.TempDir(), "clone")
	gitAt(t, t.TempDir(), "clone", "-q", origin, clone)
	gitAt(t, clone, "checkout", "-q", "-b", "crank-01-task")
	// The identity has to live in the REPO, not just in gitAt's environment. gitAt controls the
	// env of the git commands the TEST runs — but the thing under test, freshenBranch, shells out
	// to `git rebase` itself and inherits the test process's environment instead. On a developer
	// laptop that carries a global user.name and the rebase succeeds; on a CI runner it does not,
	// and the rebase fails with "Committer identity unknown", which freshenBranch faithfully
	// reports as a conflict. The test then fails for a reason that has nothing to do with rebasing.
	//
	// So configure the repos themselves and the fixture stops depending on who is running it.
	for _, dir := range []string{origin, clone} {
		gitAt(t, dir, "config", "user.name", "Ada")
		gitAt(t, dir, "config", "user.email", "ada@example.com")
	}
	return origin, clone
}

func TestFreshenRebasesOntoAMovedBase(t *testing.T) {
	origin, clone := freshenFixture(t)

	// The agent's work, committed on the task branch.
	fWrite(t, clone, "feature.txt", "the agent's work\n")
	gitAt(t, clone, "add", ".")
	gitAt(t, clone, "commit", "-m", "add the feature")

	// Meanwhile someone merges to main — a different file, so no conflict.
	fWrite(t, origin, "hotfix.txt", "someone else\n")
	gitAt(t, origin, "add", ".")
	gitAt(t, origin, "commit", "-m", "hotfix on main")

	note, ok := freshenBranch(clone, clone, "main")
	if !ok {
		t.Fatalf("a non-overlapping change must rebase cleanly, got %q", note)
	}
	if !strings.Contains(note, "rebased onto main") {
		t.Fatalf("note should say what happened, got %q", note)
	}
	// The branch now contains BOTH — which is the whole point: the PR is against current main.
	if _, err := os.Stat(filepath.Join(clone, "hotfix.txt")); err != nil {
		t.Fatalf("the base's commit should now be in the branch: %v", err)
	}
	if _, err := os.Stat(filepath.Join(clone, "feature.txt")); err != nil {
		t.Fatalf("the agent's work must survive the rebase: %v", err)
	}
}

// The case that matters most: a real conflict must NOT destroy the agent's work, and must not leave
// the worktree mid-rebase where nothing downstream understands it.
func TestFreshenAbortsOnConflictAndKeepsTheWork(t *testing.T) {
	origin, clone := freshenFixture(t)

	fWrite(t, clone, "shared.txt", "the agent's version\n")
	gitAt(t, clone, "add", ".")
	gitAt(t, clone, "commit", "-m", "agent edits shared")
	head := gitAt(t, clone, "rev-parse", "HEAD")

	fWrite(t, origin, "shared.txt", "someone else's version\n")
	gitAt(t, origin, "add", ".")
	gitAt(t, origin, "commit", "-m", "human edits shared")

	note, ok := freshenBranch(clone, clone, "main")
	if ok {
		t.Fatalf("a genuine conflict must report not-fresh, got ok with %q", note)
	}
	if !strings.Contains(note, "conflicts with main") {
		t.Fatalf("the note must name the problem, got %q", note)
	}
	// Untouched: same commit, agent's content intact, no rebase in progress.
	if now := gitAt(t, clone, "rev-parse", "HEAD"); now != head {
		t.Fatalf("the branch moved on a failed rebase: %s → %s", head, now)
	}
	body, _ := os.ReadFile(filepath.Join(clone, "shared.txt"))
	if !strings.Contains(string(body), "the agent's version") {
		t.Fatalf("the agent's work was lost: %q", body)
	}
	if _, err := os.Stat(filepath.Join(clone, ".git", "rebase-merge")); err == nil {
		t.Fatal("worktree left mid-rebase — abort did not run")
	}
}

// The common case must cost nothing and say nothing.
func TestFreshenIsSilentWhenAlreadyCurrent(t *testing.T) {
	_, clone := freshenFixture(t)
	fWrite(t, clone, "feature.txt", "work\n")
	gitAt(t, clone, "add", ".")
	gitAt(t, clone, "commit", "-m", "work")

	note, ok := freshenBranch(clone, clone, "main")
	if !ok || note != "" {
		t.Fatalf("an up-to-date branch should be a silent no-op, got ok=%v note=%q", ok, note)
	}
}

// No remote (or offline) degrades to the old behaviour — build on the forked base — rather than
// failing a task that is otherwise complete.
func TestFreshenSurvivesNoRemote(t *testing.T) {
	dir := t.TempDir()
	gitAt(t, dir, "init", "-q", "-b", "main")
	fWrite(t, dir, "a.txt", "a\n")
	gitAt(t, dir, "add", ".")
	gitAt(t, dir, "commit", "-m", "a")

	note, ok := freshenBranch(dir, dir, "main")
	if !ok {
		t.Fatalf("a repo with no remote must not block the task, got %q", note)
	}
	if !strings.Contains(note, "could not fetch") {
		t.Fatalf("it should say why it skipped, got %q", note)
	}
}

// The base name reaches an argv, and it comes from project settings — anything that isn't a plain
// branch name must never get there.
func TestFreshenRejectsAHostileBaseName(t *testing.T) {
	_, clone := freshenFixture(t)
	for _, bad := range []string{"--upload-pack=touch /tmp/x", "main;rm -rf /", "-x", "main branch"} {
		note, ok := freshenBranch(clone, clone, bad)
		if !ok || note != "" {
			t.Fatalf("base %q should be skipped silently, got ok=%v note=%q", bad, ok, note)
		}
	}
}
