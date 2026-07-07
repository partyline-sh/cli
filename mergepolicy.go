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
// manual / a no-op) and the PR URL when one was opened (#212, empty otherwise). pr/auto push to
// origin; auto additionally asks GitHub to auto-merge — if the repo has no auto-merge or branch
// protection, gh errors and we simply leave the PR open.
func applyMergePolicy(run cmdRunner, branch, title, policy string) (note, prURL string) {
	if policy != "pr" && policy != "auto" {
		return "", "" // manual (or unset): never push — leave the branch, a human merges
	}
	if out, err := run("git", "push", "-u", "origin", branch); err != nil {
		return "push failed: " + firstLine(out, err), ""
	}
	out, err := run("gh", "pr", "create", "--head", branch, "--title", title, "--body", "Opened automatically by ptln crank.")
	if err != nil {
		return "pushed; pr create failed: " + firstLine(out, err), ""
	}
	prURL = extractPRURL(out) // gh prints the new PR's URL; capture it for the glass box (#212)
	if policy == "auto" {
		// #211 SAFETY: `gh pr merge --auto` merges as soon as nothing BLOCKS the PR. If the base
		// branch has no required status checks, that's immediately — an unreviewed, un-CI'd merge to
		// main, which is NOT what merge_policy=auto promises ("merge when CI passes"). So only enable
		// auto-merge when the base branch actually gates on required checks; otherwise leave the PR
		// open (fail safe). See issue #211 / thread constraint #90.
		if !autoMergeGated(run) {
			return "PR opened; auto-merge withheld — base branch has no required status checks (would merge with no CI gate, #211)", prURL
		}
		if out, err := run("gh", "pr", "merge", branch, "--auto", "--squash"); err != nil {
			return "PR opened; auto-merge unavailable: " + firstLine(out, err), prURL
		}
		return "PR opened + auto-merge enabled", prURL
	}
	return "PR opened", prURL
}

// extractPRURL pulls the PR URL out of `gh pr create` output — gh prints the created PR's URL
// (github.com/owner/repo/pull/N) on its own line. Returns "" if none is found.
func extractPRURL(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "http") && strings.Contains(line, "/pull/") {
			return line
		}
	}
	return ""
}

// autoMergeGated reports whether the repo's base (default) branch has required status checks — the
// precondition for `gh pr merge --auto` to actually WAIT on CI rather than merge immediately (#211).
// Best-effort and FAIL-SAFE: any error/uncertainty → false (withhold auto-merge, leave the PR). The
// required_status_checks sub-resource 404s when the branch is unprotected or has no required checks,
// so a clean call ≈ "checks are configured".
func autoMergeGated(run cmdRunner) bool {
	base, err := run("gh", "repo", "view", "--json", "defaultBranchRef", "-q", ".defaultBranchRef.name")
	if err != nil || strings.TrimSpace(base) == "" {
		return false
	}
	_, err = run("gh", "api", "repos/{owner}/{repo}/branches/"+strings.TrimSpace(base)+"/protection/required_status_checks")
	return err == nil
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
