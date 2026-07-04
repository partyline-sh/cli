package main

import (
	"fmt"
	"strings"
	"testing"
)

// recRunner records the commands applyMergePolicy issues (and can force a failure at a given step).
func recRunner(failOn string) (cmdRunner, *[]string) {
	var calls []string
	run := func(name string, args ...string) (string, error) {
		line := name + " " + strings.Join(args, " ")
		calls = append(calls, line)
		if failOn != "" && strings.Contains(line, failOn) {
			return "boom\nsecond line", fmt.Errorf("exit 1")
		}
		return "", nil
	}
	return run, &calls
}

func TestApplyMergePolicy(t *testing.T) {
	t.Run("manual is a no-op that never pushes", func(t *testing.T) {
		run, calls := recRunner("")
		if note := applyMergePolicy(run, "crank-01-x", "crank: x", "manual"); note != "" {
			t.Errorf("note = %q, want empty", note)
		}
		if len(*calls) != 0 {
			t.Errorf("manual issued commands %v, want none", *calls)
		}
	})

	t.Run("pr pushes then opens a PR", func(t *testing.T) {
		run, calls := recRunner("")
		note := applyMergePolicy(run, "crank-01-x", "crank: x", "pr")
		if note != "PR opened" {
			t.Errorf("note = %q, want \"PR opened\"", note)
		}
		if len(*calls) != 2 || !strings.HasPrefix((*calls)[0], "git push") || !strings.HasPrefix((*calls)[1], "gh pr create") {
			t.Errorf("pr issued %v, want [git push…, gh pr create…]", *calls)
		}
	})

	t.Run("auto also enables auto-merge", func(t *testing.T) {
		run, calls := recRunner("")
		note := applyMergePolicy(run, "crank-01-x", "crank: x", "auto")
		if note != "PR opened + auto-merge enabled" {
			t.Errorf("note = %q", note)
		}
		if len(*calls) != 3 || !strings.HasPrefix((*calls)[2], "gh pr merge") || !strings.Contains((*calls)[2], "--auto") {
			t.Errorf("auto issued %v, want a final gh pr merge --auto", *calls)
		}
	})

	t.Run("push failure stops early with a note (branch survives)", func(t *testing.T) {
		run, calls := recRunner("git push")
		note := applyMergePolicy(run, "crank-01-x", "crank: x", "pr")
		if !strings.HasPrefix(note, "push failed:") {
			t.Errorf("note = %q, want push-failed", note)
		}
		if len(*calls) != 1 {
			t.Errorf("should stop after the failed push, got %v", *calls)
		}
	})

	t.Run("auto-merge unavailable leaves the PR open", func(t *testing.T) {
		run, _ := recRunner("gh pr merge")
		note := applyMergePolicy(run, "crank-01-x", "crank: x", "auto")
		if !strings.HasPrefix(note, "PR opened; auto-merge unavailable:") {
			t.Errorf("note = %q", note)
		}
	})
}
