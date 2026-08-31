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

// pushWork gets committed work OFF the machine that built it. It is deliberately SEPARATE from
// applyMergePolicy, because pushing is not merging — and conflating the two is how a finished
// feature ended up existing only in a worktree on one laptop:
//
//   - a failed verify skipped applyMergePolicy entirely → never pushed
//   - merge_policy=manual returned early ("a human merges") → never pushed
//   - a rate limit mid-repair took the same quarantine path → never pushed
//
// In every one of those cases the note said "left the branch for a human", but the human had no
// way to see or reach it: no remote branch, no PR, one disk. A rejected branch still deserves to
// be durable and reviewable — that is the whole point of quarantine. So: any branch with commits
// gets pushed, whatever the gate said and whatever the policy is. Merging stays gated; safety
// does not.
//
// Idempotent: re-pushing an unchanged branch is a no-op, and after repair rounds it carries the
// new commits. Returns a human note ("" when there was nothing to say).
func pushWork(run cmdRunner, branch string) string {
	if out, err := run("git", "push", "-u", "origin", branch); err != nil {
		// Say where the work IS, so a failed push is actionable rather than silent data loss.
		return "⚠ push failed (work is only on this machine): " + firstLine(out, err)
	}
	return "pushed " + branch
}

// applyMergePolicy acts on a task's freshly-committed branch. Returns a human note (empty for
// manual / a no-op) and the PR URL when one was opened (#212, empty otherwise). pr/auto push to
// origin; auto additionally asks GitHub to auto-merge — if the repo has no auto-merge or branch
// protection, gh errors and we simply leave the PR open.
//
// base is the project's configured base branch — the branch the PR TARGETS, and the same ref crank
// forked the work from. Empty = the repo's GitHub default (the pre-setting behavior).
func applyMergePolicy(run cmdRunner, branch, title, body, policy, provider, base string, draft bool) (note, prURL string) {
	if policy != "pr" && policy != "auto" {
		// manual (or unset): no PR, no merge — the human does that. The branch is already PUSHED
		// by pushWork before this runs, so "a human merges" is actually possible.
		return "", ""
	}
	// pushWork already ran; this re-push carries any commits the repair rounds added and is a
	// no-op otherwise. A failure here is still worth reporting — it means the PR would be stale.
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
	if body == "" {
		body = "Opened automatically by ptln crank."
	}
	args := []string{"pr", "create", "--head", branch, "--title", title, "--body", body}
	if base != "" {
		args = append(args, "--base", base) // the project's base branch; omitted → gh uses the repo default
	}
	if draft {
		// Review gate ON: open as a DRAFT so it reads "awaiting sign-off". The human's Accept flips it
		// to ready-for-review (web: markPullRequestReadyForReview). A repo that forbids drafts (some
		// plans) makes `gh` error; that's handled by the create-failure path below like any other.
		args = append(args, "--draft")
	}
	out, err := run("gh", args...)
	if err != nil {
		// A CHAIN builds every step on ONE branch, so steps 2..N push to a branch that already has an
		// open PR. That's success, not failure: the push above already updated the PR with this step's
		// commits. Recover the existing PR's URL so the run reports it (and never trips the noPR
		// quarantine) instead of reading as "pr create failed" on every step after the first.
		if existing := existingPRURL(run, branch); existing != "" {
			return "PR updated", existing
		}
		// The branch IS pushed (SSH key) — only the API step failed. Two very different causes, and the
		// fix differs, so classify instead of always saying "connect GitHub":
		//   • "Resource not accessible by integration" → GitHub IS connected, but the Partyline App is
		//     missing the Pull Requests permission (the App is defined by us, not the installer). The
		//     fix is an APP-OWNER action (grant Pull requests: Read & write, then re-approve installs),
		//     NOT "connect GitHub" — that misdirection cost real debugging time.
		//   • otherwise (no token / wrong account / repo not visible) → connect GitHub.
		detail := firstLine(out, err)
		if strings.Contains(strings.ToLower(detail), "not accessible by integration") {
			return "pushed to " + branch + "; pr create failed: " + detail +
				" — GitHub is connected, but the Partyline GitHub App is missing the Pull Requests permission. The app owner must grant it (App settings → Permissions → Pull requests: Read & write), then re-approve the installation. The branch is pushed, so you can also open the PR by hand.", ""
		}
		return "pushed; pr create failed: " + detail + " — connect GitHub for this project's org at partyline.sh/settings/integrations", ""
	}
	prURL = extractPRURL(out) // gh prints the new PR's URL; capture it for the glass box (#212)
	if policy == "auto" {
		// #211 SAFETY: `gh pr merge --auto` merges as soon as nothing BLOCKS the PR. If the base
		// branch has no required status checks, that's immediately — an unreviewed, un-CI'd merge to
		// main, which is NOT what merge_policy=auto promises ("merge when CI passes"). So only enable
		// auto-merge when the base branch actually gates on required checks; otherwise leave the PR
		// open (fail safe). See issue #211 / thread constraint #90.
		if !autoMergeGated(run, base) {
			return "PR opened; auto-merge withheld — base branch has no required status checks (would merge with no CI gate, #211)", prURL
		}
		if out, err := run("gh", "pr", "merge", branch, "--auto", "--squash"); err != nil {
			return "PR opened; auto-merge unavailable: " + firstLine(out, err), prURL
		}
		return "PR opened + auto-merge enabled", prURL
	}
	return "PR opened", prURL
}

// closePRForBranch closes the OPEN GitHub PR for a branch and deletes its remote branch — called on
// --restart so the abandoned attempt's PR is wiped (the fresh run opens a new one) instead of lingering
// open on GitHub. Best-effort and GitHub-only: no PR / no gh / no token just no-ops (gh errors are
// swallowed), exactly like the other gh steps — a restart must never fail because cleanup couldn't run.
func closePRForBranch(run cmdRunner, branch string) {
	if existingPRURL(run, branch) == "" {
		return // nothing open for this branch — the delete below would just error
	}
	// This runs BOTH from a human `crank --restart` and DAEMON-SIDE, where there is no tty and
	// nobody to answer: a prompt there would block the daemon forever. So it only asks when there
	// is actually a terminal on both ends, and keeps the old unattended behaviour otherwise.
	if interactiveTTY() {
		if yes, ok := Confirm("close the open PR for "+branch+" and delete the remote branch?", false); !ok || !yes {
			return
		}
	}
	_, _ = run("gh", "pr", "close", branch, "--delete-branch", "--comment", "Superseded — this task was restarted from scratch; a fresh PR will open.")
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

// autoMergeGated reports whether the branch this PR TARGETS has required status checks — the
// precondition for `gh pr merge --auto` to actually WAIT on CI rather than merge immediately (#211).
// Best-effort and FAIL-SAFE: any error/uncertainty → false (withhold auto-merge, leave the PR). The
// required_status_checks sub-resource 404s when the branch is unprotected or has no required checks,
// so a clean call ≈ "checks are configured".
//
// base is the project's configured base branch. It MUST be the branch checked: protection is
// per-branch, so a project targeting an unprotected `staging` would otherwise inherit `main`'s
// protection in this check and auto-merge into staging with no CI gate — exactly the #211 hole.
// Empty base → ask GitHub for the repo default, as before.
func autoMergeGated(run cmdRunner, base string) bool {
	if base == "" {
		def, err := run("gh", "repo", "view", "--json", "defaultBranchRef", "-q", ".defaultBranchRef.name")
		if err != nil {
			return false
		}
		base = def
	}
	if strings.TrimSpace(base) == "" {
		return false
	}
	_, err := run("gh", "api", "repos/{owner}/{repo}/branches/"+strings.TrimSpace(base)+"/protection/required_status_checks")
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
