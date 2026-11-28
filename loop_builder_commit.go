package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// A builder cannot commit in a linked worktree (its git directory is outside the sandbox), so when
// a builder-driven step reaches pr_ready partyline commits it and starts the next step. This is the
// one exception to "partyline never commits" (CLAUDE.md): batch-step commits, in the batch's own
// checkout, never pushed.

// builderAdvance commits a passed step and starts the next one, or closes the batch. Run by the
// round process right after the step reached pr_ready.
var builderAdvance = func(id string) {
	var sha string
	stopped := false
	err := withRunLock(id, func(r *loopRun) error {
		if r.State != loopPRReady {
			return nil
		}
		// Under the run's lock, which a stop takes before the batch's: a stop that completed is seen.
		if b, err := loadBatch(r.Batch); err != nil || b.Stopped {
			stopped = true
			return nil
		}
		var err error
		if sha, err = commitBatchStep(r); err != nil {
			return err
		}
		return r.save(loopEvent{Type: "committed", Data: map[string]any{"commit": sha}})
	})
	if err != nil {
		_ = loopPost(id, fmt.Sprintf("The step passed, but partyline could not commit it: %v\nCommit the reviewed work yourself, then run `ptln loop batch next %s`.", err, id))
		return
	}
	if stopped {
		return // the person stopped the batch: nothing is committed or started
	}
	r, err := loadLoopRun(id)
	if err != nil || r.State != loopPRReady {
		return
	}
	if deferredReviewRun(r) {
		// The fixes are committed: those findings are done, and any deferred since are asked now.
		_ = withRunLock(id, func(cur *loopRun) error {
			return withBatch(cur.Batch, func(b *loopBatch) error {
				for j := range b.Deferred {
					if b.Deferred[j].Decision == "fix" {
						b.Deferred[j].Decision = "fixed"
					}
				}
				return batchAskDeferred(cur, b)
			})
		})
		if b, err := loadBatch(r.Batch); err == nil {
			writeBatchSummary(b)
		}
		_ = loopPost(id, fmt.Sprintf("The fixes to the batch's deferred findings pass and are committed (%s). The batch is done; `ptln loop batch status %s` summarises it for the user.", shortSHA(sha), id))
		return
	}
	next, err := batchNextStep(r)
	switch {
	case err != nil:
		_ = loopPost(id, fmt.Sprintf("Step %d is committed (%s), but the next step could not start: %v", r.Step, shortSHA(sha), err))
	case next == "":
		_ = loopPost(id, fmt.Sprintf("The last step is committed (%s): the batch is done. `ptln loop batch status %s` summarises it for the user.", shortSHA(sha), id))
	default:
		if b, err := loadBatch(r.Batch); err == nil && b.Deferral == next {
			_ = loopPost(id, fmt.Sprintf("The last step is committed (%s). The findings deferred during the batch now wait for the user's rulings (loop %s, ctrl-\\ then a).", shortSHA(sha), next))
		}
	}
}

// commitBatchStep commits the tree round r.Round reviewed, exactly, on top of HEAD, and returns the
// commit. Its author is the builder and its committer the person (git config). memory/ and .ptln/
// are not part of the review; they go into the commit as HEAD has them, so the commit changes
// nothing the reviewers did not see. commit-tree rather than commit: hooks that rewrite files would
// make the commit differ from the reviewed tree.
func commitBatchStep(r *loopRun) (string, error) {
	now, err := workTreeID(r.Repo)
	if err != nil {
		return "", err
	}
	if now != r.LastTree {
		return "", fmt.Errorf("the working tree changed since round %d reviewed it", r.Round)
	}
	b, err := loadBatch(r.Batch)
	if err != nil {
		return "", err
	}
	if branch, _ := gitLoop(r.Repo, "rev-parse", "--abbrev-ref", "HEAD"); b.Branch != "" && branch != b.Branch {
		return "", fmt.Errorf("the batch runs on branch %s, but %s is checked out", b.Branch, branch)
	}
	head, err := gitLoop(r.Repo, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	tree, err := treeWithHeadMemory(r.Repo, r.LastTree)
	if err != nil {
		return "", err
	}
	if headTree, _ := gitLoop(r.Repo, "rev-parse", "HEAD^{tree}"); tree == headTree {
		return head, nil // the step changed nothing to commit
	}
	cmd := exec.Command("git", "-C", r.Repo, "commit-tree", tree, "-p", head, "-F", "-")
	cmd.Stdin = strings.NewReader(batchStepMessage(r, b))
	cmd.Env = append(loopGitEnv(), "GIT_AUTHOR_NAME="+builderAuthor(r), "GIT_AUTHOR_EMAIL=partyline@localhost")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git commit-tree: %v %s", err, engineStderr(err))
	}
	sha := strings.TrimSpace(string(out))
	// Moves the branch only if it is still where this commit's parent is.
	if _, err := gitLoop(r.Repo, "update-ref", "-m", "partyline: batch step "+fmt.Sprint(r.Step), "HEAD", sha, head); err != nil {
		return "", err
	}
	// The index to the new HEAD; the files already are. memory/ and .ptln/ are left as staged.
	if _, err := gitLoop(r.Repo, append([]string{"reset", "-q", "--", "."}, snapshotExcludes...)...); err != nil {
		return sha, err
	}
	return sha, nil
}

// treeWithHeadMemory is the reviewed tree with HEAD's memory/ and .ptln/ put back.
func treeWithHeadMemory(repo, reviewed string) (string, error) {
	tmp, err := os.MkdirTemp("", "ptln-commit-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	index := filepath.Join(tmp, "index")
	if _, err := gitIndex(repo, index, "read-tree", reviewed); err != nil {
		return "", err
	}
	kept, err := gitLoop(repo, "ls-tree", "-r", "--full-tree", "HEAD", "--", "memory", ".ptln")
	if err != nil {
		return "", err
	}
	if kept != "" {
		cmd := exec.Command("git", "-C", repo, "update-index", "--index-info")
		cmd.Env = append(loopGitEnv(), "GIT_INDEX_FILE="+index)
		cmd.Stdin = strings.NewReader(kept + "\n")
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("git update-index: %v %s", err, out)
		}
	}
	return gitIndex(repo, index, "write-tree")
}

// builderAuthor is "<engine> <model> via partyline".
func builderAuthor(r *loopRun) string {
	e := r.BuilderEngine
	if e == "" {
		e = "builder"
	}
	return strings.ReplaceAll(e, ":", " ") + " via partyline"
}

// batchStepMessage is the step's title and the trailers that tie the commit to its loop.
func batchStepMessage(r *loopRun, b *loopBatch) string {
	title := briefTitle(latestBrief(r))
	if deferredReviewRun(r) {
		title = "Fix the batch's deferred findings"
	}
	return fmt.Sprintf("%s\n\nLoop: %s\nBatch: %s step %d\nRound: %d\n", title, r.ID, b.ID, r.Step, r.Round)
}
