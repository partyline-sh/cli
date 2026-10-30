package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// engineAncestor finds a claude or codex process anywhere above this one. It matches the program
// itself, argv[0], or the script node runs, by exact name: a path that merely contains "claude",
// such as ~/.claude or /tmp/claude-501, is not an engine.
func engineAncestor() (int, string) {
	pid := os.Getppid()
	for i := 0; i < 64 && pid > 1; i++ {
		out, err := exec.Command("ps", "-ww", "-o", "ppid=,command=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			return 0, ""
		}
		f := strings.Fields(strings.TrimSpace(string(out)))
		if len(f) < 2 {
			return 0, ""
		}
		// The same recognition chat uses (engineProgram): the program's own name, through node.
		if prog := engineProgram(f[1:]); prog != "" {
			return pid, prog
		}
		pid, _ = strconv.Atoi(f[0])
	}
	return 0, ""
}

// loopStopRun stops a run, and the batch it is a step of, from the popup. For a batch it stops
// whichever step is current; if `batch next` starts a new step while the stop waits for the old
// one's lock, the stop follows it to the new step.
func loopStopRun(id string) error {
	for attempt := 0; attempt < 5; attempt++ {
		target := id
		if r, err := loadLoopRun(id); err == nil && r.Batch != "" {
			if b, err := loadBatch(r.Batch); err == nil && len(b.Runs) > 0 {
				target = b.Runs[len(b.Runs)-1]
			}
		}
		moved := false
		err := withRunLock(target, func(r *loopRun) error {
			if !r.live() {
				if r.Batch != "" {
					if b, err := loadBatch(r.Batch); err == nil && len(b.Runs) > 0 && b.Runs[len(b.Runs)-1] != r.ID {
						moved = true
						return nil
					}
				}
				return fmt.Errorf("loop %s is already %s", r.ID, r.State)
			}
			return stopLocked(r)
		})
		if !moved {
			return err
		}
	}
	return fmt.Errorf("the batch kept starting new steps; stop it again")
}

// stopLocked stops r, under its lock, and its batch.
func stopLocked(r *loopRun) error {
	if r.Batch != "" {
		if err := withBatch(r.Batch, func(b *loopBatch) error {
			b.Stopped = true
			// Findings deferred before the stop are still the person's to rule on.
			if anyDeferred(b) && r.Step <= len(b.Briefs) {
				head, _ := gitMem(r.Repo, "rev-parse", "HEAD")
				_, err := startDeferredReview(r, b, head)
				return err
			}
			return nil
		}); err != nil {
			return err
		}
	}
	if r.finished() {
		// Between steps: the step passed and stays pr_ready; only the batch stops.
		if err := r.set(loopPRReady, r.Note+"; the user stopped the batch", "batch_stopped", nil); err != nil {
			return err
		}
		return loopPost(r.ID, "The user stopped the batch. Do not run `ptln loop batch next`.")
	}
	if err := r.set(loopStopped, "stopped by the user", "stopped", nil); err != nil {
		return err
	}
	stopContractor(r.Builder) // a builder at work stops with its step
	return loopPost(r.ID, "The user stopped this loop. Partyline will not check or review further.")
}
