package main

import (
	"fmt"
	"strings"
	"testing"
)

// recRunner records the commands applyMergePolicy issues (and can force a failure at a given step).
// The default runner answers the #211 gate checks as "checks configured" (repo view → a branch
// name, protection api → ok) so pr/auto tests exercise the happy path.
func recRunner(failOn string) (cmdRunner, *[]string) {
	var calls []string
	run := func(name string, args ...string) (string, error) {
		line := name + " " + strings.Join(args, " ")
		calls = append(calls, line)
		if failOn != "" && strings.Contains(line, failOn) {
			return "boom\nsecond line", fmt.Errorf("exit 1")
		}
		if strings.Contains(line, "defaultBranchRef") {
			return "main\n", nil // base branch for the gate check
		}
		if strings.HasPrefix(line, "gh pr create") {
			return "https://github.com/acme/repo/pull/42\n", nil // gh prints the new PR URL
		}
		return "", nil
	}
	return run, &calls
}

func TestApplyMergePolicy(t *testing.T) {
	t.Run("manual is a no-op that never pushes", func(t *testing.T) {
		run, calls := recRunner("")
		note, prURL := applyMergePolicy(run, "crank-01-x", "crank: x", "manual", "", "")
		if note != "" || prURL != "" {
			t.Errorf("manual = (%q, %q), want empty", note, prURL)
		}
		if len(*calls) != 0 {
			t.Errorf("manual issued commands %v, want none", *calls)
		}
	})

	t.Run("pr pushes, opens a PR, and captures the URL (#212)", func(t *testing.T) {
		run, calls := recRunner("")
		note, prURL := applyMergePolicy(run, "crank-01-x", "crank: x", "pr", "", "")
		if note != "PR opened" {
			t.Errorf("note = %q, want \"PR opened\"", note)
		}
		if prURL != "https://github.com/acme/repo/pull/42" {
			t.Errorf("prURL = %q, want the created PR URL", prURL)
		}
		if len(*calls) != 2 || !strings.HasPrefix((*calls)[0], "git push") || !strings.HasPrefix((*calls)[1], "gh pr create") {
			t.Errorf("pr issued %v, want [git push…, gh pr create…]", *calls)
		}
	})

	t.Run("auto enables auto-merge when the base branch has required checks", func(t *testing.T) {
		run, calls := recRunner("")
		note, _ := applyMergePolicy(run, "crank-01-x", "crank: x", "auto", "", "")
		if note != "PR opened + auto-merge enabled" {
			t.Errorf("note = %q", note)
		}
		last := (*calls)[len(*calls)-1]
		if !strings.HasPrefix(last, "gh pr merge") || !strings.Contains(last, "--auto") {
			t.Errorf("auto's final call = %q, want gh pr merge --auto", last)
		}
	})

	t.Run("auto is WITHHELD when the base branch has no required checks (#211)", func(t *testing.T) {
		// The required_status_checks probe fails → no CI gate → must NOT auto-merge.
		run, calls := recRunner("required_status_checks")
		note, _ := applyMergePolicy(run, "crank-01-x", "crank: x", "auto", "", "")
		if !strings.HasPrefix(note, "PR opened; auto-merge withheld") {
			t.Errorf("note = %q, want auto-merge withheld", note)
		}
		for _, c := range *calls {
			if strings.HasPrefix(c, "gh pr merge") {
				t.Errorf("merge was attempted despite no CI gate: %v", *calls)
			}
		}
	})

	t.Run("push failure stops early with a note (branch survives)", func(t *testing.T) {
		run, calls := recRunner("git push")
		note, _ := applyMergePolicy(run, "crank-01-x", "crank: x", "pr", "", "")
		if !strings.HasPrefix(note, "push failed:") {
			t.Errorf("note = %q, want push-failed", note)
		}
		if len(*calls) != 1 {
			t.Errorf("should stop after the failed push, got %v", *calls)
		}
	})

	t.Run("auto-merge unavailable leaves the PR open", func(t *testing.T) {
		run, _ := recRunner("gh pr merge")
		note, _ := applyMergePolicy(run, "crank-01-x", "crank: x", "auto", "", "")
		if !strings.HasPrefix(note, "PR opened; auto-merge unavailable:") {
			t.Errorf("note = %q", note)
		}
	})

	t.Run("gitlab pushes then stops — never calls gh, emits an MR note", func(t *testing.T) {
		run, calls := recRunner("")
		note, prURL := applyMergePolicy(run, "crank-01-x", "crank: x", "pr", "gitlab", "")
		if !strings.Contains(note, "merge request in GitLab") {
			t.Errorf("note = %q, want a GitLab MR instruction", note)
		}
		if prURL != "" {
			t.Errorf("prURL = %q, want empty (partyline doesn't open the MR)", prURL)
		}
		if len(*calls) != 1 || !strings.HasPrefix((*calls)[0], "git push") {
			t.Errorf("gitlab issued %v, want [git push…] only (no gh)", *calls)
		}
		for _, c := range *calls {
			if strings.HasPrefix(c, "gh ") {
				t.Errorf("gitlab must never call gh: %v", *calls)
			}
		}
	})

	t.Run("bitbucket pushes then stops — never calls gh, emits a PR note", func(t *testing.T) {
		run, calls := recRunner("")
		note, prURL := applyMergePolicy(run, "crank-01-x", "crank: x", "auto", "bitbucket", "")
		if !strings.Contains(note, "pull request in Bitbucket") {
			t.Errorf("note = %q, want a Bitbucket PR instruction", note)
		}
		if prURL != "" {
			t.Errorf("prURL = %q, want empty", prURL)
		}
		if len(*calls) != 1 || !strings.HasPrefix((*calls)[0], "git push") {
			t.Errorf("bitbucket issued %v, want [git push…] only (no gh, no auto-merge)", *calls)
		}
	})
}

// The project's base branch must reach BOTH gh calls that care about it: the PR's --base (what the
// PR targets) and the #211 protection probe (protection is per-branch, so probing `main` while
// merging into an unprotected `staging` would auto-merge with no CI gate — the exact hole #211
// closed for the default branch).
func TestApplyMergePolicyBaseBranch(t *testing.T) {
	t.Run("pr targets the configured base", func(t *testing.T) {
		run, calls := recRunner("")
		applyMergePolicy(run, "crank-01-x", "crank: x", "pr", "", "staging")
		create := (*calls)[1]
		if !strings.Contains(create, "--base staging") {
			t.Errorf("pr create = %q, want --base staging", create)
		}
	})

	t.Run("no base leaves gh on the repo default", func(t *testing.T) {
		run, calls := recRunner("")
		applyMergePolicy(run, "crank-01-x", "crank: x", "pr", "", "")
		if create := (*calls)[1]; strings.Contains(create, "--base") {
			t.Errorf("pr create = %q, want no --base (gh uses the repo default)", create)
		}
	})

	t.Run("auto probes protection on the base it merges into, not the repo default", func(t *testing.T) {
		run, calls := recRunner("")
		applyMergePolicy(run, "crank-01-x", "crank: x", "auto", "", "staging")
		joined := strings.Join(*calls, "\n")
		if !strings.Contains(joined, "branches/staging/protection") {
			t.Errorf("calls = %v, want the protection probe on staging", *calls)
		}
		if strings.Contains(joined, "defaultBranchRef") {
			t.Errorf("calls = %v, want NO repo-default lookup when a base is configured", *calls)
		}
	})
}
