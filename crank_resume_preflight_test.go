package main

import (
	"os"
	"strings"
	"testing"
)

// A RESUMED TASK MUST NOT BE PREFLIGHTED.
//
// "Red before green" rests on the worktree being untouched. On `crank --resume` it deliberately is
// not: the worktree still holds the previous attempt's uncommitted work. A task interrupted
// mid-flight — a rate limit, a provider 529, a timeout — has usually written its tests already, so
// its acceptance check passes on resume. The preflight then read "the work is already here" as
// "this can never prove itself" and refused to let it finish.
//
// Observed end to end: a run died on `API Error: 529 Overloaded` after 15 minutes with eight
// modified files and seven of its acceptance tests already written. Retry re-dispatched it, the
// preflight ran against that worktree, saw green, and blocked it. The task furthest along was the
// one that could not be resumed — the exact inverse of what resume is for.
func TestAResumedTaskSkipsThePreflight(t *testing.T) {
	src, err := os.ReadFile("crank.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)

	i := strings.Index(body, "if len(o.acceptance) > 0")
	if i < 0 {
		t.Fatal("the preflight guard is gone from crank.go")
	}
	line := body[i:]
	if j := strings.IndexByte(line, '\n'); j > 0 {
		line = line[:j]
	}
	if !strings.Contains(line, `resume == ""`) {
		t.Fatalf("the preflight no longer excludes a resume — an interrupted task that had written its tests becomes unresumable.\nguard is: %s", line)
	}
}

// The reasoning has to survive, because the guard looks like a redundant condition to anyone who
// has not watched a resumable run get refused.
func TestTheResumeExclusionKeepsItsReason(t *testing.T) {
	src, _ := os.ReadFile("crank.go")
	body := string(src)
	for _, phrase := range []string{"NOT ON A RESUME", "uncommitted work", "UNRESUMABLE"} {
		if !strings.Contains(body, phrase) {
			t.Errorf("the resume exclusion no longer explains %q — the next reader will delete it as redundant", phrase)
		}
	}
}
