package main

import (
	"fmt"
	"strings"
)

// Checks that could not run at all say nothing about the work. A blocked .envrc is the case that
// cost loop 295a5b three rounds: `direnv exec` refuses before running anything, every check exited
// 1 in 0 s, the baseline counted that as "already failing", the builder worked blind, and the run
// ended "changed nothing". Such a result pauses the run with what to fix instead of counting.

// checksEnvProblem is why a round's checks could not run here, or "" when they ran. It never fixes
// the environment itself: `direnv allow` approves code partyline has not reviewed.
func checksEnvProblem(root string, res loopCheckRound) string {
	for _, c := range res.Checks {
		if c.Exit == 0 {
			continue
		}
		tail := stripColours(c.Tail)
		switch {
		case strings.Contains(tail, "direnv: error") && strings.Contains(tail, "is blocked"):
			return fmt.Sprintf("direnv blocks %s/.envrc, so no check ran; run `direnv allow %s` if you trust it", root, root)
		case c.Exit == -1 && c.Seconds == 0:
			return fmt.Sprintf("`%s` could not start: %s", c.Cmd, checkOutputLine(tail))
		case c.Exit == 127:
			return fmt.Sprintf("`%s`: command not found here (%s)", c.Cmd, checkOutputLine(tail))
		}
	}
	return ""
}

// pauseForChecksEnv pauses r at state from (the baseline, or a round's checks) because its checks
// could not run, telling the session and noting how to resume.
func pauseForChecksEnv(r *loopRun, from, what, problem string) {
	note := fmt.Sprintf("paused: %s could not run its checks: %s; then `ptln loop resume %s`", what, problem, r.ID)
	msg := fmt.Sprintf("Paused: %s could not run its checks, so nothing was measured and no round was used.\n\n%s\n\nOnce that is fixed, `ptln loop resume %s` runs them again. Nothing for you to do otherwise.", what, problem, r.ID)
	_ = withRunLock(r.ID, func(cur *loopRun) error {
		if cur.State != r.State || cur.Round != r.Round {
			return nil
		}
		cur.PausedFrom = from
		if err := cur.set(loopPaused, note, "paused", map[string]any{"checks_env": problem}); err != nil {
			return err
		}
		return loopPost(cur.ID, msg)
	})
}

// checkOutputLine is a check's first line of output, or a note that it printed nothing.
func checkOutputLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return "no output"
}
