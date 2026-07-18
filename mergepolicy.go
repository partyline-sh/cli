package main

import (
	"os"
	"os/exec"
	"strings"

	"partyline.sh/partyline/internal/api"
)

// mergepolicy.go — #77 slice 3 (decision #86). After a crank task's worker commits its branch,
// act on it per the run's merge policy. manual (default) leaves the branch for a human; pr pushes
// + opens a PR; auto pushes + opens a PR + enables GitHub auto-merge (GitHub merges once the repo's
// required checks pass). Everything here is NON-FATAL: the branch always survives for manual review,
// so a push/PR/merge failure just becomes a note on the task — never a failed task.

// cmdRunner runs an external command (git/gh) in the repo dir, returning combined output. It's the
// seam that lets applyMergePolicy be tested without touching git or the network.
type cmdRunner func(name string, args ...string) (string, error)

// mergeGitHubToken fetches this run's short-lived GitHub App installation token for the PR step, using
// the device credentials the daemon passed to this crank child via env (the same ones the run-logger
// uses). Every miss — no run id, no device creds, or the endpoint 404ing because the org hasn't
// connected GitHub — returns "" so realRunner falls back to the operator's local token. Never fatal:
// the PR step degrades to the pre-App behaviour, and the failure note points the customer at
// /settings/integrations to fix it themselves.
func mergeGitHubToken(runID string) string {
	if runID == "" {
		return ""
	}
	token := strings.TrimSpace(os.Getenv("PARTYLINE_DAEMON_TOKEN"))
	if token == "" {
		return ""
	}
	base := strings.TrimSpace(os.Getenv("PARTYLINE_API"))
	if base == "" {
		base = api.Base()
	}
	tok, err := api.RunGitHubToken(base, token, runID)
	if err != nil {
		return "" // fall back to the local token (the endpoint 404s when the org hasn't connected GitHub)
	}
	return tok
}

func realRunner(dir, tokenOverride string) cmdRunner {
	// tokenOverride is this run's short-lived GitHub App installation token (minted by the control
	// plane when the org connected GitHub) — the productized path. When it's empty we fall back to the
	// operator's LOCAL token so machines not yet on the App path keep working. Either becomes GH_TOKEN
	// so `gh pr create` works even though this daemon (a background service) doesn't inherit the login
	// shell's gh auth. "" from both → let gh use whatever it finds. Harmless for the git calls.
	tok := tokenOverride
	if tok == "" {
		tok = resolveGitHubToken()
	}
	return func(name string, args ...string) (string, error) {
		cmd := exec.Command(name, args...)
		cmd.Dir = dir // git and gh both act on this repo (git via cwd, gh via its cwd repo detection)
		if tok != "" {
			cmd.Env = append(os.Environ(), "GH_TOKEN="+tok) // last-wins over any stale ambient token
		}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
}

// applyMergePolicy acts on a task's freshly-committed branch. Returns a human note (empty for
// manual / a no-op) and the PR URL when one was opened (#212, empty otherwise). pr/auto push to
// origin; auto additionally asks GitHub to auto-merge — if the repo has no auto-merge or branch
// protection, gh errors and we simply leave the PR open.
func applyMergePolicy(run cmdRunner, branch, title, policy, provider string) (note, prURL string) {
	if policy != "pr" && policy != "auto" {
		return "", "" // manual (or unset): never push — leave the branch, a human merges
	}
	if out, err := run("git", "push", "-u", "origin", branch); err != nil {
		return "push failed: " + firstLine(out, err), ""
	}
	// Non-GitHub providers: partyline has no PR-API integration (a deliberate no-vault decision), so the
	// branch is pushed over SSH and the human opens the merge/pull request. Return here — never call
	// `gh` (it's GitHub-only) and never emit the "connect GitHub" note to a GitLab/Bitbucket customer.
	// The empty prURL flips noPR upstream, so the run lands in Review with this note as the to-do.
	switch provider {
	case "gitlab":
		return "pushed branch " + branch + " — open the merge request in GitLab", ""
	case "bitbucket":
		return "pushed branch " + branch + " — open the pull request in Bitbucket", ""
	}
	out, err := run("gh", "pr", "create", "--head", branch, "--title", title, "--body", "Opened automatically by ptln crank.")
	if err != nil {
		// A CHAIN builds every step on ONE branch, so steps 2..N push to a branch that already has an
		// open PR. That's success, not failure: the push above already updated the PR with this step's
		// commits. Recover the existing PR's URL so the run reports it (and never trips the noPR
		// quarantine) instead of reading as "pr create failed" on every step after the first.
		if existing := existingPRURL(run, branch); existing != "" {
			return "PR updated", existing
		}
		// The branch IS pushed (SSH key) — only the API step failed, usually because the daemon's gh
		// can't see the repo (no token / wrong account). Point at the fix instead of a bare gh error.
		return "pushed; pr create failed: " + firstLine(out, err) + " — connect GitHub for this project's org at partyline.sh/settings/integrations", ""
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

// existingPRURL asks gh for the OPEN PR whose head is this branch, returning its URL or "". Used when
// `gh pr create` fails because a PR already exists — the chain case, where every step pushes to the
// same branch and only the first one opens the PR. Any error (no gh, no auth, genuinely no PR) yields
// "" so the caller falls through to its normal failure handling.
func existingPRURL(run cmdRunner, branch string) string {
	out, err := run("gh", "pr", "view", branch, "--json", "url", "--jq", ".url")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if u := strings.TrimSpace(line); strings.HasPrefix(u, "http") {
			return u
		}
	}
	return ""
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
