package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/gitwt"
)

// #602: the completion report has to CARRY the branch state, not just be able to compute it. These
// tests run against real git repositories the test builds itself — no mocks, no network, no host API.

func bsGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// bsRepo builds a working repo cloned from a real (bare, local) origin, with one commit on main.
func bsRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	origin, seed := filepath.Join(root, "origin.git"), filepath.Join(root, "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	bsGit(t, root, "init", "-q", "--bare", "-b", "main", origin)
	bsGit(t, root, "init", "-q", "-b", "main", seed)
	if err := os.WriteFile(filepath.Join(seed, "seed.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bsGit(t, seed, "add", ".")
	bsGit(t, seed, "commit", "-q", "-m", "init")
	bsGit(t, seed, "remote", "add", "origin", origin)
	bsGit(t, seed, "push", "-q", "origin", "main")
	repo := filepath.Join(root, "repo")
	bsGit(t, root, "clone", "-q", origin, repo)
	return repo
}

// A finished task's report carries commits ahead, a diffstat, whether the worktree survived, and
// whether the base contains the branch — and it survives the trip through the run-task payload.
func TestTaskReportCarriesBranchState(t *testing.T) {
	repo := bsRepo(t)
	wt, branch, err := gitwt.Create(repo, "report state")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wt, "work.txt"), []byte("1\n2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bsGit(t, wt, "add", ".")
	bsGit(t, wt, "commit", "-q", "-m", "work")

	var got []api.RunTaskUpdate
	report := runReporter{post: func(tr api.RunTaskUpdate) { got = append(got, tr) }}
	report.emitResult(0, "done", crankResult{
		task: "report state", branch: branch, ok: true,
		branchState: probeBranchState(repo, branch, "main"),
	})

	if len(got) != 1 || got[0].BranchState == nil {
		t.Fatalf("completion report carried no branch state: %+v", got)
	}
	bs := got[0].BranchState
	if !bs.Counted || bs.CommitsAhead != 1 || bs.FilesChanged != 1 || bs.Insertions != 2 {
		t.Errorf("branch state = %+v, want 1 commit ahead, 1 file, +2", bs)
	}
	if !bs.WorktreeExists {
		t.Error("the worktree is on disk; the report says it isn't")
	}
	if bs.Merged != string(gitwt.ContainedNo) {
		t.Errorf("merged = %q, want %q", bs.Merged, gitwt.ContainedNo)
	}
	// It has to reach the control plane as data, under its own key, leaving every existing field
	// untouched.
	raw, err := json.Marshal(bs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"commits_ahead", "files_changed", "insertions", "deletions", "counted", "worktree_exists", "merged"} {
		if _, ok := wire[k]; !ok {
			t.Errorf("branch_state payload is missing %q: %s", k, raw)
		}
	}
}

// The distinction the whole probe exists for: a branch that has NOT merged and a branch whose state
// could not be determined must not report the same thing.
func TestBranchStateUnmergedIsNotUnknown(t *testing.T) {
	withRemote := bsRepo(t)
	wt, branch, err := gitwt.Create(withRemote, "has a base")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wt, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bsGit(t, wt, "add", ".")
	bsGit(t, wt, "commit", "-q", "-m", "work")

	// Same shape of repo and the same commit — but no remote, so nothing can be resolved.
	noRemote := t.TempDir()
	bsGit(t, noRemote, "init", "-q", "-b", "main", ".")
	if err := os.WriteFile(filepath.Join(noRemote, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bsGit(t, noRemote, "add", ".")
	bsGit(t, noRemote, "commit", "-q", "-m", "init")
	bsGit(t, noRemote, "branch", "orphan-work")

	known := probeBranchState(withRemote, branch, "main")
	unknown := probeBranchState(noRemote, "orphan-work", "main")
	if known.Merged != string(gitwt.ContainedNo) {
		t.Errorf("branch with a reachable base: merged = %q, want %q", known.Merged, gitwt.ContainedNo)
	}
	if unknown.Merged != string(gitwt.ContainedUnknown) {
		t.Fatalf("no reachable remote must report %q, got %q — collapsing it into 'not merged' is how a janitor deletes live work", gitwt.ContainedUnknown, unknown.Merged)
	}
	if known.Merged == unknown.Merged {
		t.Fatal("'not merged' and 'could not tell' must be distinguishable")
	}
}
