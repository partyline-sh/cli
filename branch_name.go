package main

import "strings"

// branch_name.go — a branch name a person can scan.
//
// Ours were `crank-<runid8>-NN-<first four words of the task>`. The run-id fragment is load-bearing
// and stays: it makes a branch unique PER run while stable WITHIN one, which is exactly what
// resume-in-place, restart and chain stacking depend on. What was weak is the slug — four words off
// the front of a sentence, stop-words and all, so `Post a human's CLI` became `Post-a-human-s-CLI`
// and told you almost nothing.
//
// This repo currently carries 225 stranded branches. Triaging them is harder than it needs to be for
// exactly this reason, and the fix costs nothing at write time.
//
// NOT A MODEL CALL. dmux names branches with AI, and for a branch that is a poor trade: it adds
// latency and tokens to every task, and the task already carries a human-written title. Dropping the
// filler and keeping more of the meaningful words gets most of the value deterministically — no
// network on the path that creates a worktree.

// slugStop are words that carry no identity in a branch name. Deliberately short: an aggressive stop
// list mangles real titles ("Gate the production deploy" must not become "Gate-production-deploy"
// losing nothing, but "How to fix the thing" should not spend three of its words on "how to the").
var slugStop = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true, "but": true,
	"to": true, "of": true, "in": true, "on": true, "at": true, "by": true,
	"for": true, "with": true, "from": true, "into": true, "is": true, "are": true,
	"it": true, "its": true, "this": true, "that": true, "then": true, "so": true,
}

// slugWords is how many meaningful words a branch name keeps. Five rather than four, because
// dropping the filler frees room and the extra word is usually the one that identifies the task.
const slugWords = 5

// taskSlugWords picks the words a branch name is built from: filler dropped, order preserved, capped.
//
// Every word being filler is possible ("To the point"), and the answer then is the ORIGINAL words —
// a nameless branch is worse than a bland one.
func taskSlugWords(s string, n int) string {
	fields := strings.Fields(s)
	kept := make([]string, 0, n)
	for _, f := range fields {
		w := strings.ToLower(strings.Trim(f, ".,:;!?\"'()[]"))
		if slugStop[w] {
			continue
		}
		kept = append(kept, f)
		if len(kept) == n {
			break
		}
	}
	if len(kept) == 0 {
		return firstWords(s, n)
	}
	return strings.Join(kept, " ")
}
