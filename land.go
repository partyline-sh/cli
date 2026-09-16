package main

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"partyline.sh/partyline/internal/gitwt"
)

// land.go — the merge train.
//
// WHY THIS EXISTS. A fleet produces N branches at once, all forked from the same commit, and then
// every one of them waits for a human. By the time anyone merges the first, the rest are stale, and
// the staleness surfaces as a conflict hours later with nobody around to resolve it. That is not a
// planning failure and no amount of task ordering fixes it: the tasks genuinely did not depend on
// each other, they just aged.
//
// Human teams avoid this by landing work continuously — small branches, merged in hours, everyone
// rebasing onto what just landed. The train is that, mechanised: tasks are built in PARALLEL, but
// they LAND ONE AT A TIME, each one replayed onto whatever landed before it and re-checked before
// it goes in.
//
// TWO RULES THAT MAKE IT SAFE TO AUTOMATE.
//
//  1. Only verified work lands. A task whose gates did not run, or did not pass, is never a
//     candidate — the train is not a way around the verify gate, it is what makes passing it worth
//     something. This is checked by the caller AND re-asserted here, because a landing path that
//     trusts its caller is one refactor away from merging unverified work.
//
//  2. A conflict never lands and never blocks. If replaying onto the new base fails, that branch
//     drops out with its conflicting paths recorded and the train moves to the next one. One
//     unmergeable task cannot stop the other five.
//
// OFF BY DEFAULT. This is the only code in crank that writes to the base branch, so it ships behind
// an explicit flag. Everything else in this design makes conflicts rarer; this is the piece that
// requires you to trust your gates, and that is a decision an operator makes, not a default.

// landOutcome is what the train did with one branch.
type landOutcome string

const (
	landed        landOutcome = "landed"     // replayed onto the base and pushed
	landConflict  landOutcome = "conflict"   // base moved incompatibly — branch intact, left for a human
	landSkipped   landOutcome = "skipped"    // not a candidate (unverified, nothing to land, train off)
	landPushError landOutcome = "push-error" // rebase was clean, the push was rejected
)

type landResult struct {
	outcome   landOutcome
	conflicts []string // measured evidence of which files really collided
	note      string
}

// landQueue serialises landings within one crank process. Workers build concurrently and then queue
// here; the lock is what makes "rebase onto current base, then push" atomic with respect to the
// other workers. Without it, two workers can both rebase onto the same base and the second push is
// rejected — which is the exact race the train exists to remove.
//
// Process-local by design: one crank owns one run. Two cranks landing into the same repo at once
// would still race at the remote, and git's own non-fast-forward rejection is the backstop there —
// landPushError, not a corrupted base.
type landQueue struct{ mu sync.Mutex }

// landCandidate is everything the train needs to decide, kept explicit so the safety rules are
// visible at the call site rather than buried in a struct the caller half-fills.
type landCandidate struct {
	branch   string
	wtPath   string
	base     string // the base branch NAME (not a remote ref) — what we push into
	verified bool   // the gates RAN and PASSED
	hasWork  bool   // the branch actually has commits to land
}

// land replays one branch onto the current base and pushes it, holding the queue lock so no other
// worker can land in between. Returns what happened; never returns an error, because a branch that
// cannot land is a note on a task, not a failed run.
func (q *landQueue) land(run cmdRunner, c landCandidate) landResult {
	if !c.verified {
		// The load-bearing refusal. Re-checked here even though the caller checks it too: this is
		// the only function that writes to the base, so it does not delegate its own precondition.
		return landResult{outcome: landSkipped, note: "not landed — gates did not pass"}
	}
	if !c.hasWork {
		return landResult{outcome: landSkipped, note: "not landed — nothing to land"}
	}
	if c.base == "" || !branchDeltaRe.MatchString(c.base) {
		// Same untrusted-argv rule freshenBranch applies: the base can arrive from project settings.
		return landResult{outcome: landSkipped, note: "not landed — unusable base name"}
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	// Replay onto whatever landed while this task was building — including anything the train itself
	// landed a moment ago. FETCH_HEAD rather than a remote-tracking ref for the reason freshen.go
	// documents: a tracking ref nothing refreshed is a confidently stale answer.
	if _, err := run("git", "-C", c.wtPath, "fetch", "origin", c.base); err != nil {
		return landResult{outcome: landSkipped, note: "not landed — could not fetch " + c.base}
	}
	if _, err := run("git", "-C", c.wtPath, "rebase", "FETCH_HEAD"); err != nil {
		conflicts := conflictedPaths(run, c.wtPath)
		_, _ = run("git", "-C", c.wtPath, "rebase", "--abort")
		note := "not landed — conflicts with " + c.base
		if len(conflicts) > 0 {
			note += " (" + strings.Join(conflicts, ", ") + ")"
		}
		return landResult{outcome: landConflict, conflicts: conflicts, note: note}
	}
	// HEAD:<base> pushes this worktree's commits into the base branch without checking anything out
	// in the main repo — the worker's worktree is the only tree we touch.
	if out, err := run("git", "-C", c.wtPath, "push", "origin", "HEAD:"+c.base); err != nil {
		return landResult{outcome: landPushError, note: "not landed — push rejected: " + firstLine(out, err)}
	}
	return landResult{outcome: landed, note: fmt.Sprintf("landed on %s", c.base)}
}

// conflictedPaths lists the files git stopped on during a rebase. Best-effort: an empty list makes
// the note less specific, never changes the decision.
func conflictedPaths(run cmdRunner, wtPath string) []string {
	out, err := run("git", "-C", wtPath, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil
	}
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		if p := strings.TrimSpace(line); p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

// crankLandQueue is the process-wide train. One crank owns one run, and every worker in that run
// queues here, which is what makes "rebase onto current base, then push" atomic between them.
var crankLandQueue = &landQueue{}

// tryLand is crank's call site for the train. Returns (landed, note): a false with a non-empty note
// means we tried and could not, and the caller should fall through to the merge policy so the work
// is still pushed and reviewable. A false with an EMPTY note means the train is off — say nothing.
//
// The verify gate is the gatekeeper. v.ran && v.ok is the same condition the quarantine branch above
// tests, expressed positively: a gate that did not run is NOT a pass, so a repo with no checks never
// auto-lands. That is deliberate — landing without you is a trade for having real checks, and a repo
// that hasn't defined any hasn't made that trade.
func tryLand(run cmdRunner, o crankOpts, repo, branch, wtPath string, v verifyResult) (bool, string) {
	if !o.land {
		return false, ""
	}
	if !(v.ran && v.ok) {
		return false, "not landed — no verify gate to trust (define .partyline/verify to auto-land)"
	}
	base := o.base
	if base == "" {
		base = gitwt.DefaultBaseName(repo)
	}
	res := crankLandQueue.land(run, landCandidate{
		branch:   branch,
		wtPath:   wtPath,
		base:     base,
		verified: true,
		hasWork:  branchAhead(wtPath) > 0,
	})
	return res.outcome == landed, res.note
}

// envOn reads a boolean opt-in from the environment. Only explicit affirmatives count: an operator
// who wrote PARTYLINE_CRANK_LAND=0 meant off, and treating "any value at all" as on — the way the
// fleet-width variable does, where any value is a width — would turn that into a yes.
func envOn(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
