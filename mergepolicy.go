package main

import (
	"os/exec"
	"strings"
)

// mergepolicy.go — #77 slice 3 (decision #86). After a crank task's worker commits its branch,
// act on it per the run's merge policy. manual (default) leaves the branch for a human; pr pushes
// + opens a PR; auto pushes + opens a PR + enables GitHub auto-merge (GitHub merges once the repo's
// required checks pass). Everything here is NON-FATAL: the branch always survives for manual review,
// so a push/PR/merge failure just becomes a note on the task — never a failed task.

// cmdRunner runs an external command (git/gh) in the repo dir, returning combined output. It's the
// seam that lets applyMergePolicy be tested without touching git or the network.
type cmdRunner func(name string, args ...string) (string, error)

func realRunner(dir string) cmdRunner {
	return func(name string, args ...string) (string, error) {
		cmd := exec.Command(name, args...)
		cmd.Dir = dir // git and gh both act on this repo (git via cwd, gh via its cwd repo detection)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
}

// applyMergePolicy acts on a task's freshly-committed branch. Returns a human note (empty for
// manual / a no-op). pr/auto push to origin; auto additionally asks GitHub to auto-merge — if the
// repo has no auto-merge or branch protection, gh errors and we simply leave the PR open.
func applyMergePolicy(run cmdRunner, branch, title, policy string) string {
	if policy != "pr" && policy != "auto" {
		return "" // manual (or unset): never push — leave the branch, a human merges
	}
	if out, err := run("git", "push", "-u", "origin", branch); err != nil {
		return "push failed: " + firstLine(out, err)
	}
	if out, err := run("gh", "pr", "create", "--head", branch, "--title", title, "--body", "Opened automatically by ptln crank."); err != nil {
		return "pushed; pr create failed: " + firstLine(out, err)
	}
	if policy == "auto" {
		if out, err := run("gh", "pr", "merge", branch, "--auto", "--squash"); err != nil {
			return "PR opened; auto-merge unavailable: " + firstLine(out, err)
		}
		return "PR opened + auto-merge enabled"
	}
	return "PR opened"
}

// firstLine returns the first non-empty line of command output (a concise reason for the note),
// falling back to the error text when there's no output.
func firstLine(out string, err error) string {
	s := strings.TrimSpace(out)
	if s == "" {
		return err.Error()
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}
