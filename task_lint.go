package main

import (
	"fmt"
	"os"
	"strings"
)

// task_lint.go — the crank advisory (#76 task-authoring aid). Dogfooding proved (docs/DOGFOOD-LOG.md,
// thread decisions #76/#83) that the biggest lever on output quality + review burden is an
// EXECUTABLE acceptance criterion per task: a self-verify command turns "ok ≠ correct" into
// "test-passes ≈ correct". This is a NON-BLOCKING nudge toward that — it never skips or halts a task.

// acceptanceCues are the case-insensitive substrings that signal a task carries a verifiable check
// (a command the worker self-verifies against and the reviewer can trust). See docs/TASK-AUTHORING.md.
var acceptanceCues = []string{
	"go test",
	"npx tsc",
	"tsc --noemit",
	"gofmt",
	"must pass",
	"acceptance:",
	"verify:",
	"passes",
	"assert",
}

// taskHasAcceptanceCue reports whether a task line references a verifiable acceptance check. It's a
// deliberately crude, pure heuristic (case-insensitive substring match) — a hint, not a gate.
func taskHasAcceptanceCue(task string) bool {
	lower := strings.ToLower(task)
	for _, cue := range acceptanceCues {
		if strings.Contains(lower, cue) {
			return true
		}
	}
	return false
}

// warnTasksMissingAcceptanceCue prints a one-line, non-blocking stderr advisory for each task that
// lacks an acceptance cue. It NEVER blocks, skips, or reorders — every task still runs. Advisory only.
func warnTasksMissingAcceptanceCue(tasks []string) {
	for i, task := range tasks {
		if !taskHasAcceptanceCue(task) {
			fmt.Fprintf(os.Stderr, "⚠ task %d has no acceptance check — output can't self-verify (see docs/TASK-AUTHORING.md)\n", i+1)
		}
	}
}
