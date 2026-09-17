package gitwt

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// gitrun runs git in dir with a deterministic identity, failing the test on error.
func gitrun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// mkclone builds a REAL origin: a bare repo with one commit on main, cloned to a working repo.
// No network and no host API — exactly the shape the probe has to work against.
func mkclone(t *testing.T) (repo string) {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	seed := filepath.Join(root, "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	gitrun(t, root, "init", "-q", "--bare", "-b", "main", origin)
	gitrun(t, root, "init", "-q", "-b", "main", seed)
	writeFile(t, seed, "tracked.txt", "hi\n")
	gitrun(t, seed, "add", ".")
	gitrun(t, seed, "commit", "-q", "-m", "init")
	gitrun(t, seed, "remote", "add", "origin", origin)
	gitrun(t, seed, "push", "-q", "origin", "main")

	repo = filepath.Join(root, "repo")
	gitrun(t, root, "clone", "-q", origin, repo)
	return repo
}

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A branch with work on it reports its commits, its diffstat, its live worktree — and that the base
// does NOT contain it yet.
func TestProbeBranchStateAhead(t *testing.T) {
	repo := mkclone(t)
	wt, branch, err := Create(repo, "add feature")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	writeFile(t, wt, "new.txt", "a\nb\nc\n")
	gitrun(t, wt, "add", ".")
	gitrun(t, wt, "commit", "-q", "-m", "work")

	st := ProbeBranchState(repo, branch, "main")
	if !st.Counted {
		t.Fatalf("expected a counted comparison, got %+v", st)
	}
	if st.CommitsAhead != 1 {
		t.Errorf("commits ahead = %d, want 1", st.CommitsAhead)
	}
	if st.FilesChanged != 1 || st.Insertions != 3 || st.Deletions != 0 {
		t.Errorf("diffstat = %d files +%d -%d, want 1 file +3 -0", st.FilesChanged, st.Insertions, st.Deletions)
	}
	if !st.WorktreeExists {
		t.Error("worktree exists on disk but was reported gone")
	}
	if st.Contained != ContainedNo {
		t.Errorf("contained = %q, want %q", st.Contained, ContainedNo)
	}
	if st.Base != "origin/main" {
		t.Errorf("base = %q, want origin/main", st.Base)
	}

	// And once the worktree is torn down, the branch's commits are still reportable — only the
	// worktree flag flips.
	if err := Remove(repo, wt); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if st := ProbeBranchState(repo, branch, "main"); st.WorktreeExists || st.CommitsAhead != 1 {
		t.Errorf("after teardown: %+v, want worktree gone and 1 commit ahead", st)
	}
}

// A branch whose commits landed on the base reports merged — via local git containment, with no
// pull request anywhere in the picture.
func TestProbeBranchStateMerged(t *testing.T) {
	repo := mkclone(t)
	wt, branch, err := Create(repo, "landed work")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	writeFile(t, wt, "landed.txt", "x\n")
	gitrun(t, wt, "add", ".")
	gitrun(t, wt, "commit", "-q", "-m", "work")
	// Land it on origin/main the way a merge does, then let the probe's own fetch see it.
	gitrun(t, repo, "merge", "--no-ff", "-m", "merge", branch)
	gitrun(t, repo, "push", "-q", "origin", "main")

	st := ProbeBranchState(repo, branch, "main")
	if st.Contained != ContainedYes {
		t.Fatalf("contained = %q, want %q (%+v)", st.Contained, ContainedYes, st)
	}
	if st.CommitsAhead != 0 || !st.Counted {
		t.Errorf("merged branch: commits ahead = %d (counted=%v), want 0 counted", st.CommitsAhead, st.Counted)
	}
}

// A task that produced nothing reports zero commits — and says so as a FACT (Counted), which is
// what makes it distinguishable from a probe that could not run.
func TestProbeBranchStateNoCommits(t *testing.T) {
	repo := mkclone(t)
	_, branch, err := Create(repo, "did nothing")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	st := ProbeBranchState(repo, branch, "main")
	if !st.Counted || st.CommitsAhead != 0 || st.FilesChanged != 0 {
		t.Fatalf("empty branch: %+v, want counted with zero commits and no files", st)
	}
	if st.Contained != ContainedYes {
		t.Errorf("an empty branch IS contained in its base; got %q", st.Contained)
	}
}

// No reachable remote: UNKNOWN, never "not merged". Collapsing those two is how a janitor later
// deletes live work.
func TestProbeBranchStateNoRemoteIsUnknown(t *testing.T) {
	repo := mkrepo(t) // a plain repo with no origin at all
	wt, branch, err := Create(repo, "offline work")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	writeFile(t, wt, "new.txt", "a\n")
	gitrun(t, wt, "add", ".")
	gitrun(t, wt, "commit", "-q", "-m", "work")

	for _, base := range []string{"", "main"} {
		st := ProbeBranchState(repo, branch, base)
		if st.Contained != ContainedUnknown {
			t.Errorf("base %q: contained = %q, want %q", base, st.Contained, ContainedUnknown)
		}
		if st.Counted {
			t.Errorf("base %q: reported counts with no base to compare against: %+v", base, st)
		}
		if st.Base != "" {
			t.Errorf("base %q: resolved base %q with no remote", base, st.Base)
		}
	}
}

// A branch that no longer exists is unknown too — there is nothing left to compare.
func TestProbeBranchStateMissingBranch(t *testing.T) {
	repo := mkclone(t)
	st := ProbeBranchState(repo, "never-existed", "main")
	if st.Contained != ContainedUnknown || st.Counted || st.WorktreeExists {
		t.Fatalf("missing branch: %+v, want unknown/uncounted/no worktree", st)
	}
}
