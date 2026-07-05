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
		note, prURL := applyMergePolicy(run, "crank-01-x", "crank: x", "manual")
		if note != "" || prURL != "" {
			t.Errorf("manual = (%q, %q), want empty", note, prURL)
		}
		if len(*calls) != 0 {
			t.Errorf("manual issued commands %v, want none", *calls)
		}
	})

	t.Run("pr pushes, opens a PR, and captures the URL (#212)", func(t *testing.T) {
		run, calls := recRunner("")
		note, prURL := applyMergePolicy(run, "crank-01-x", "crank: x", "pr")
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
		note, _ := applyMergePolicy(run, "crank-01-x", "crank: x", "auto")
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
		note, _ := applyMergePolicy(run, "crank-01-x", "crank: x", "auto")
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
		note, _ := applyMergePolicy(run, "crank-01-x", "crank: x", "pr")
		if !strings.HasPrefix(note, "push failed:") {
			t.Errorf("note = %q, want push-failed", note)
		}
		if len(*calls) != 1 {
			t.Errorf("should stop after the failed push, got %v", *calls)
		}
	})

	t.Run("auto-merge unavailable leaves the PR open", func(t *testing.T) {
		run, _ := recRunner("gh pr merge")
		note, _ := applyMergePolicy(run, "crank-01-x", "crank: x", "auto")
		if !strings.HasPrefix(note, "PR opened; auto-merge unavailable:") {
			t.Errorf("note = %q", note)
		}
	})
}
