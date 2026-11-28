package main

import (
	"fmt"
	"os"
	"strings"
)

// Deleting a loop for good: `ptln loop delete <id>` and d on a Loops card. A run still going is
// stopped first (no deferred findings are put to anyone: the work is cancelled), its builder,
// reviewers and arbiter stop, and its records are removed, so it leaves the board and can never be
// resumed. A batch step deletes its whole batch: every step's run and the batch itself.

// loopDeleteTarget is what deleting id removes, in words, for the confirmation.
func loopDeleteTarget(id string) (string, error) {
	if b, err := loadBatch(strings.TrimPrefix(id, "batch-")); err == nil {
		return fmt.Sprintf("batch %s (%d steps)", b.ID, len(b.Briefs)), nil
	}
	r, err := loadLoopRun(id)
	if err != nil {
		return "", err
	}
	if r.Batch != "" {
		if b, err := loadBatch(r.Batch); err == nil {
			return fmt.Sprintf("batch %s (%d steps)", b.ID, len(b.Briefs)), nil
		}
	}
	return "loop " + r.ID, nil
}

// loopDelete stops and removes run id, or its whole batch; id may also name a batch.
func loopDelete(id string) error {
	var runs []string
	batch := ""
	if b, err := loadBatch(strings.TrimPrefix(id, "batch-")); err == nil {
		batch, runs = b.ID, b.Runs
	} else {
		r, err := loadLoopRun(id)
		if err != nil {
			return err
		}
		runs = []string{r.ID}
		if r.Batch != "" {
			if b, err := loadBatch(r.Batch); err == nil {
				batch, runs = b.ID, b.Runs
			}
		}
	}
	if batch != "" {
		// Stopped first, so `batch next` cannot start another step while the runs go.
		_ = withBatch(batch, func(b *loopBatch) error { b.Stopped = true; return nil })
	}
	for _, rid := range runs {
		_ = withRunLock(rid, func(r *loopRun) error {
			if r.live() && !r.finished() {
				_ = r.set(loopStopped, "deleted by the user", "deleted", nil)
				_ = loopPost(r.ID, "The user deleted this loop. Partyline will not check, review or build it further; do not run any more `ptln loop` commands for it.")
			}
			return nil
		})
		for _, c := range loadContractors() {
			if c.Loop == rid && (c.State == "running" || c.State == "starting") {
				stopContractor(c.ID)
			}
		}
	}
	for _, rid := range runs {
		if err := os.RemoveAll(loopDir(rid)); err != nil {
			return err
		}
	}
	if batch != "" {
		if err := os.RemoveAll(batchDir(batch)); err != nil {
			return err
		}
	}
	loopRefreshIndicators()
	return nil
}

// loopDeleteCmd is `ptln loop delete <id>`.
func loopDeleteCmd(args []string) {
	if len(args) != 1 {
		fatal(fmt.Errorf("usage: ptln loop delete <loop or batch id>"))
	}
	what, err := loopDeleteTarget(args[0])
	if err != nil {
		fatal(err)
	}
	if err := loopDelete(args[0]); err != nil {
		fatal(err)
	}
	fmt.Println("deleted " + what)
}
