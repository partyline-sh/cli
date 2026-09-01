package main

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

// fakeGit scripts git replies by substring so the train's DECISIONS can be asserted without a repo.
// What matters here is which commands run, in what order, and — above all — which ones do NOT.
type fakeGit struct {
	mu      sync.Mutex
	calls   []string
	replies map[string]string
	errors  map[string]bool
}

func (f *fakeGit) run(name string, args ...string) (string, error) {
	line := name + " " + strings.Join(args, " ")
	f.mu.Lock()
	f.calls = append(f.calls, line)
	f.mu.Unlock()
	for k, fail := range f.errors {
		if strings.Contains(line, k) && fail {
			return f.replies[k], errors.New("exit 1")
		}
	}
	for k, v := range f.replies {
		if strings.Contains(line, k) {
			return v, nil
		}
	}
	return "", nil
}

func (f *fakeGit) ran(sub string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

func okCandidate() landCandidate {
	return landCandidate{branch: "crank-01-thing", wtPath: "/wt", base: "main", verified: true, hasWork: true}
}

func TestLandRebasesThenPushes(t *testing.T) {
	f := &fakeGit{}
	got := (&landQueue{}).land(f.run, okCandidate())
	if got.outcome != landed {
		t.Fatalf("outcome = %q, want %q (%s)", got.outcome, landed, got.note)
	}
	if !f.ran("fetch origin main") || !f.ran("rebase FETCH_HEAD") {
		t.Error("must replay onto the freshly fetched base before pushing")
	}
	if !f.ran("push origin HEAD:main") {
		t.Error("must push the worktree HEAD into the base branch")
	}
}

// The rule the whole design rests on: the train is not a way around the verify gate.
func TestLandRefusesUnverifiedWork(t *testing.T) {
	f := &fakeGit{}
	c := okCandidate()
	c.verified = false
	got := (&landQueue{}).land(f.run, c)
	if got.outcome != landSkipped {
		t.Fatalf("outcome = %q, want %q", got.outcome, landSkipped)
	}
	if f.ran("push") {
		t.Fatal("UNVERIFIED WORK MUST NEVER REACH THE BASE BRANCH")
	}
	if f.ran("rebase") || f.ran("fetch") {
		t.Error("an unverified branch should be rejected before touching git at all")
	}
}

func TestLandRefusesEmptyAndUnusableInputs(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*landCandidate)
	}{
		{"nothing to land", func(c *landCandidate) { c.hasWork = false }},
		{"no base", func(c *landCandidate) { c.base = "" }},
		{"base name that could be read as a flag", func(c *landCandidate) { c.base = "--upload-pack=evil" }},
		{"base name with shell metacharacters", func(c *landCandidate) { c.base = "main;rm -rf /" }},
	}
	for _, tc := range cases {
		f := &fakeGit{}
		c := okCandidate()
		tc.mut(&c)
		got := (&landQueue{}).land(f.run, c)
		if got.outcome != landSkipped {
			t.Errorf("%s: outcome = %q, want %q", tc.name, got.outcome, landSkipped)
		}
		if f.ran("push") {
			t.Errorf("%s: must not push", tc.name)
		}
	}
}

// One unmergeable branch must not stop the others, and must leave itself intact for a human.
func TestLandConflictAbortsAndDoesNotPush(t *testing.T) {
	f := &fakeGit{
		replies: map[string]string{"diff --name-only": "web/src/lib/api/runs.ts\n"},
		errors:  map[string]bool{"rebase FETCH_HEAD": true},
	}
	got := (&landQueue{}).land(f.run, okCandidate())
	if got.outcome != landConflict {
		t.Fatalf("outcome = %q, want %q", got.outcome, landConflict)
	}
	if !f.ran("rebase --abort") {
		t.Fatal("a conflicted rebase must be aborted so the branch survives for review")
	}
	if f.ran("push") {
		t.Fatal("must not push a branch that failed to replay")
	}
	if len(got.conflicts) != 1 || got.conflicts[0] != "web/src/lib/api/runs.ts" {
		t.Errorf("conflicts = %v, want the conflicted path recorded as evidence", got.conflicts)
	}
}

func TestLandReportsAPushRejection(t *testing.T) {
	f := &fakeGit{errors: map[string]bool{"push": true}}
	got := (&landQueue{}).land(f.run, okCandidate())
	if got.outcome != landPushError {
		t.Fatalf("outcome = %q, want %q", got.outcome, landPushError)
	}
}

// The reason the queue exists: N workers finish at once, and their landings must not interleave.
// Each landing has to see the base as the previous one left it, which means one at a time.
func TestLandingsAreSerialised(t *testing.T) {
	var mu sync.Mutex
	inFlight, maxInFlight := 0, 0
	runner := func(name string, args ...string) (string, error) {
		line := strings.Join(args, " ")
		if strings.Contains(line, "rebase FETCH_HEAD") {
			mu.Lock()
			inFlight++
			if inFlight > maxInFlight {
				maxInFlight = inFlight
			}
			mu.Unlock()
			// Give any concurrent landing every chance to overlap with this one.
			for i := 0; i < 2000; i++ {
				_ = i
			}
			mu.Lock()
			inFlight--
			mu.Unlock()
		}
		return "", nil
	}
	q := &landQueue{}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); q.land(runner, okCandidate()) }()
	}
	wg.Wait()
	if maxInFlight != 1 {
		t.Fatalf("%d landings overlapped — the train must land exactly one branch at a time", maxInFlight)
	}
}

// tryLand is the gate in front of the gate. These assert the two ways the train stays off.
func TestTryLandIsOffUnlessAsked(t *testing.T) {
	f := &fakeGit{}
	ok, note := tryLand(f.run, crankOpts{land: false, base: "main"}, "/repo", "b", "/wt", verifyResult{ran: true, ok: true})
	if ok {
		t.Fatal("the train must not run without --land")
	}
	if note != "" {
		t.Errorf("silent when off, got note %q", note)
	}
	if len(f.calls) != 0 {
		t.Errorf("must not touch git when off, ran: %v", f.calls)
	}
}

func TestTryLandRequiresAPassingGate(t *testing.T) {
	for _, v := range []verifyResult{
		{ran: false, ok: false}, // no checks defined — NOT a pass
		{ran: false, ok: true},  // "ok" without having run means nothing
		{ran: true, ok: false},  // ran and rejected
	} {
		f := &fakeGit{}
		ok, note := tryLand(f.run, crankOpts{land: true, base: "main"}, "/repo", "b", "/wt", v)
		if ok {
			t.Fatalf("verify %+v must not land", v)
		}
		if note == "" {
			t.Errorf("verify %+v: should say why it didn't land", v)
		}
		if f.ran("push") {
			t.Fatalf("verify %+v: PUSHED WITHOUT A PASSING GATE", v)
		}
	}
}

// "=0 means off" is the whole point: the fleet-width variable treats any value as a value, and
// copying that here would read a deliberate "0" as a yes.
func TestEnvOn(t *testing.T) {
	for _, c := range []struct {
		val  string
		want bool
	}{{"1", true}, {"true", true}, {"TRUE", true}, {"yes", true}, {"on", true}, {" 1 ", true},
		{"", false}, {"0", false}, {"false", false}, {"no", false}, {"off", false}, {"maybe", false}} {
		t.Setenv("PARTYLINE_TEST_LAND", c.val)
		if got := envOn("PARTYLINE_TEST_LAND"); got != c.want {
			t.Errorf("envOn(%q) = %v, want %v", c.val, got, c.want)
		}
	}
}
