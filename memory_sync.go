package main

// Git is the transport. Not a metaphor: the memory repo is an ordinary repository, so a fact
// written on one machine reaches the other by the same mechanism the team already trusts for
// code, with the same review, history and offline behaviour. There is no partyline server in
// this path at all.
//
// Sync is best-effort and never blocks the work. A push that fails (offline, no remote yet,
// credentials elsewhere) leaves the fact committed locally and says so; the next sync carries it.

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// gitMem is git for the memory repo: combined output (a push failure explains itself on stderr)
// and no short timeout, because a network push is not a local read. Distinct from gitIn in
// board_review_overlay.go, which is a 5-second local-diff helper.
func gitMem(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// memoryPull fast-forwards the memory clone. Returns false when there is nothing to pull from
// (no remote configured — a project that has not been shared yet is perfectly valid).
// isRepoLocalMemory reports whether dir is a user repo rather than a partyline memory clone.
//
// Decided by LOCATION, not by a flag passed in: every caller of memorySync would otherwise have to
// remember to thread the flag through, and the one that forgot would commit someone's repo. A
// partyline memory clone lives under ~/.partyline/projects/; anything else is the user's.
func isRepoLocalMemory(dir string) bool {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return true // cannot prove it is ours, so treat it as yours and do nothing
	}
	root, err := filepath.Abs(projectsRoot())
	if err != nil {
		return true
	}
	return !strings.HasPrefix(abs+string(filepath.Separator), root+string(filepath.Separator))
}

func memoryPull(dir string) (bool, error) {
	if out, err := gitMem(dir, "remote"); err != nil || strings.TrimSpace(out) == "" {
		return false, nil
	}
	// No upstream yet — a project created here and not yet pushed. There is nothing to take, and
	// treating that as an error made the very first `add-repo` report failure on a healthy setup.
	if !hasUpstream(dir) {
		return false, nil
	}
	if out, err := gitMem(dir, "pull", "--ff-only", "--quiet"); err != nil {
		return false, fmt.Errorf("%s", firstLineOf(out))
	}
	return true, nil
}

// memoryCommit stages everything under the memory clone and commits it. Empty commits are not an
// error: two agents recording the same fact twice must not fail the second one.
func memoryCommit(dir, message string) error {
	if _, err := gitMem(dir, "add", "-A"); err != nil {
		return err
	}
	out, err := gitMem(dir, "commit", "-m", message)
	if err != nil && !strings.Contains(out, "nothing to commit") {
		return fmt.Errorf("%s", firstLineOf(out))
	}
	return nil
}

// memoryPush sends it. A failure is REPORTED, never swallowed: a fact that exists only on this
// machine is exactly the problem this whole design exists to end, so the human has to know.
func memoryPush(dir string) error {
	if out, err := gitMem(dir, "remote"); err != nil || strings.TrimSpace(out) == "" {
		return nil // nothing to push to yet
	}
	args := []string{"push", "--quiet"}
	if !hasUpstream(dir) {
		// First push of a project: set the upstream in the same breath, so every later sync is
		// a plain push and the human never has to know this step existed.
		args = append(args, "--set-upstream", "origin", "HEAD")
	}
	if out, err := gitMem(dir, args...); err != nil {
		return fmt.Errorf("%s", firstLineOf(out))
	}
	return nil
}

// hasUpstream reports whether this branch tracks a remote one.
func hasUpstream(dir string) bool {
	_, err := gitMem(dir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	return err == nil
}

// memorySync is the whole round trip for a write: take what others wrote, add ours, send it.
// Pull first so a concurrent write from a teammate is merged before we push rather than after.
// memorySync pulls, commits and pushes the MEMORY REPO. It is a no-op when the facts live in the
// user's own repo.
//
// This guard is the whole reason repoLocal exists. In the side-repo model the clone was ours to
// commit; your code repo is not. An agent that committed and pushed your working tree because it
// recorded a fact would be a far worse defect than the one repo-local memory fixes — it could
// publish half-finished work, or a secret you had staged but not reviewed. Facts are written and
// left for you.
func memorySync(dir, message string) error {
	if isRepoLocalMemory(dir) {
		return nil
	}
	if _, err := memoryPull(dir); err != nil {
		return fmt.Errorf("could not take teammates' memory first: %w", err)
	}
	if err := memoryCommit(dir, message); err != nil {
		return err
	}
	if err := memoryPush(dir); err != nil {
		return err
	}
	return nil
}
