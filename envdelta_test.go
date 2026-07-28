package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"partyline.sh/partyline/internal/api"
)

// A real git repo shaped like a real pipeline: `main` is production, `staging` is ahead of it by two
// commits, one of which came from a partyline task branch that was merged as far as staging.
//
// Tested against actual git rather than a mock, because every interesting failure in this code is a
// git semantics failure — direction of `A..B`, whether a merged branch still reads as an ancestor,
// whether a subject with a newline can forge a record. A mock would agree with whatever the code
// already does.
func envRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com",
			"GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	commit := func(name, msg string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(msg), 0o644); err != nil {
			t.Fatal(err)
		}
		run("add", ".")
		run("commit", "-m", msg)
	}

	run("init", "-q", "-b", "main")
	commit("base.txt", "base")

	// A partyline task branch, merged into staging but not into main — the exact state "built but
	// not live yet" describes.
	run("checkout", "-q", "-b", "crank-01-add-widget")
	commit("widget.txt", "add the widget")
	run("checkout", "-q", "main")
	run("checkout", "-q", "-b", "staging")
	run("merge", "-q", "--no-ff", "-m", "merge widget", "crank-01-add-widget")

	// A commit pushed straight to staging by a human — counted, but not one of ours.
	commit("hotfix.txt", "hotfix the header")
	run("checkout", "-q", "main")
	return dir
}

func TestEnvGapsMeasuresPendingWork(t *testing.T) {
	dir := envRepo(t)
	q := api.EnvQuestion{
		Label: "demo",
		Environments: []api.EnvStep{
			{Name: "staging", Branch: "staging"},
			{Name: "production", Branch: "main"},
		},
		Branches: []api.EnvBranchRef{{Branch: "crank-01-add-widget", RunID: "run-1"}},
	}

	gaps := envGapsFor(dir, q)
	if len(gaps) != 1 {
		t.Fatalf("want 1 gap for a 2-environment chain, got %d", len(gaps))
	}
	g := gaps[0]
	if g.FromName != "staging" || g.ToName != "production" {
		t.Fatalf("direction is backwards: %s → %s", g.FromName, g.ToName)
	}
	// Two non-merge commits are on staging and not main: the task's own commit and the hotfix.
	if g.CommitCount != 2 {
		t.Fatalf("want 2 commits waiting, got %d (%+v)", g.CommitCount, g.Commits)
	}
	if len(g.Items) != 1 || g.Items[0].RunID != "run-1" {
		t.Fatalf("the merged task branch should map back to its run, got %+v", g.Items)
	}
	if len(g.Authors) != 1 || g.Authors[0] != "Ada" {
		t.Fatalf("want Ada as the sole author, got %+v", g.Authors)
	}
}

// The reverse direction must be empty: production has nothing staging lacks. This is the assertion
// that catches an `A..B` flip, which is otherwise invisible — both directions return plausible
// numbers, just the wrong one.
func TestEnvGapsIsDirectional(t *testing.T) {
	dir := envRepo(t)
	gaps := envGapsFor(dir, api.EnvQuestion{
		Environments: []api.EnvStep{
			{Name: "production", Branch: "main"},
			{Name: "staging", Branch: "staging"},
		},
	})
	if len(gaps) != 1 {
		t.Fatalf("want 1 gap, got %d", len(gaps))
	}
	if gaps[0].CommitCount != 0 {
		t.Fatalf("production is behind staging, so the reverse gap must be empty, got %d", gaps[0].CommitCount)
	}
}

// An environment naming a branch this clone has never seen is NOT an error and NOT a zero — it is
// unmeasurable, and reporting "0 commits waiting" there would read as "everything is shipped".
func TestEnvGapsSkipsUnknownBranch(t *testing.T) {
	dir := envRepo(t)
	gaps := envGapsFor(dir, api.EnvQuestion{
		Environments: []api.EnvStep{
			{Name: "staging", Branch: "staging"},
			{Name: "uat", Branch: "never-fetched"},
		},
	})
	if len(gaps) != 0 {
		t.Fatalf("an unresolvable branch must yield no gap, got %+v", gaps)
	}
}

// A single environment has no next one. Nothing to compare, and no reason to shell out to git.
func TestEnvGapsNeedsTwoEnvironments(t *testing.T) {
	dir := envRepo(t)
	if gaps := envGapsFor(dir, api.EnvQuestion{
		Environments: []api.EnvStep{{Name: "production", Branch: "main"}},
	}); gaps != nil {
		t.Fatalf("want no gaps for a one-environment chain, got %+v", gaps)
	}
}

// The control plane supplies branch names, so they are untrusted input on their way to a command
// line. Anything that is not a plain branch name must never reach git.
func TestBranchDeltaReRejectsHostileNames(t *testing.T) {
	for _, bad := range []string{
		"", "--upload-pack=touch /tmp/x", "--output=/etc/passwd", "main;rm -rf /",
		"main branch", "$(whoami)", "`id`", "main\nstaging", "/absolute", ".hidden",
	} {
		if branchDeltaRe.MatchString(bad) {
			t.Fatalf("branch name %q must be rejected", bad)
		}
	}
	for _, ok := range []string{"main", "staging", "release/1.2", "feat/env-delta", "crank-01-add-widget", "v1.0.0"} {
		if !branchDeltaRe.MatchString(ok) {
			t.Fatalf("branch name %q must be accepted", ok)
		}
	}
}

func TestEnvDeltaSummaryReadsNaturally(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "staging → production: in sync"},
		{1, "staging → production: 1 commit waiting"},
		{11, "staging → production: 11 commits waiting"},
	}
	for _, c := range cases {
		got := envDeltaSummary(api.EnvGap{FromName: "staging", ToName: "production", CommitCount: c.n})
		if got != c.want {
			t.Fatalf("summary(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}
