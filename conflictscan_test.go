package main

import (
	"errors"
	"strings"
	"testing"
)

// A scripted cmdRunner: responses keyed by the joined command line's PREFIX, in order of declaration.
func scriptedRunner(t *testing.T, script []struct {
	prefix string
	out    string
	err    error
}) cmdRunner {
	return func(name string, args ...string) (string, error) {
		line := name + " " + strings.Join(args, " ")
		for _, s := range script {
			if strings.HasPrefix(line, s.prefix) {
				return s.out, s.err
			}
		}
		t.Fatalf("unscripted command: %s", line)
		return "", nil
	}
}

type step = struct {
	prefix string
	out    string
	err    error
}

func TestScanPRConflictsRealConflict(t *testing.T) {
	run := scriptedRunner(t, []step{
		{prefix: "gh pr list", out: `[{"number":488,"headRefName":"crank-abc-01-drag"},{"number":490,"headRefName":"feature/human-work"}]`},
		{prefix: "git fetch origin main"},
		{prefix: "git diff --name-only origin/main...crank-me", out: "web/src/lib/api/work.ts\nweb/app.tsx\n"},
		{prefix: "git fetch origin refs/heads/crank-abc-01-drag"},
		{prefix: "git fetch origin refs/heads/feature/human-work"},
		// candidate 488 overlaps on work.ts and merge-tree reports a REAL conflict there
		{prefix: "git diff --name-only origin/main...FETCH_HEAD", out: "web/src/lib/api/work.ts\nother.ts\n"},
		{prefix: "git merge-tree", out: "deadbeef0123\nweb/src/lib/api/work.ts\n", err: errors.New("exit status 1")},
	})
	got, checked := scanPRConflicts(run, "crank-me", "main")
	if !checked {
		t.Fatal("scan should have run")
	}
	// BOTH candidates overlap (the shared diff script) and conflict; the crank one is resolvable,
	// the human one is not.
	if len(got) != 2 {
		t.Fatalf("want 2 conflicts, got %+v", got)
	}
	if got[0].PR != 488 || !got[0].Resolvable || got[0].Files[0] != "web/src/lib/api/work.ts" {
		t.Fatalf("crank conflict wrong: %+v", got[0])
	}
	if got[1].PR != 490 || got[1].Resolvable {
		t.Fatalf("human PR must be info-only (resolvable=false): %+v", got[1])
	}
}

func TestScanPRConflictsCleanMergeIsNotAConflict(t *testing.T) {
	run := scriptedRunner(t, []step{
		{prefix: "gh pr list", out: `[{"number":7,"headRefName":"crank-x-01-y"}]`},
		{prefix: "git fetch origin main"},
		{prefix: "git diff --name-only origin/main...crank-me", out: "a.ts\n"},
		{prefix: "git fetch origin refs/heads/crank-x-01-y"},
		{prefix: "git diff --name-only origin/main...FETCH_HEAD", out: "a.ts\n"},
		{prefix: "git merge-tree", out: "deadbeef\n"}, // exit 0 — same file, different regions
	})
	got, checked := scanPRConflicts(run, "crank-me", "main")
	if !checked || len(got) != 0 {
		t.Fatalf("clean merge must not be flagged: checked=%v got=%+v", checked, got)
	}
}

func TestScanPRConflictsNoOverlapSkipsMergeTree(t *testing.T) {
	run := scriptedRunner(t, []step{
		{prefix: "gh pr list", out: `[{"number":7,"headRefName":"other-branch"}]`},
		{prefix: "git fetch origin main"},
		{prefix: "git diff --name-only origin/main...crank-me", out: "a.ts\n"},
		{prefix: "git fetch origin refs/heads/other-branch"},
		{prefix: "git diff --name-only origin/main...FETCH_HEAD", out: "b.ts\n"},
		// NOTE: no merge-tree script — reaching it would t.Fatal (unscripted)
	})
	got, checked := scanPRConflicts(run, "crank-me", "main")
	if !checked || len(got) != 0 {
		t.Fatalf("no overlap must skip merge-tree and report clean: %v %+v", checked, got)
	}
}

func TestScanPRConflictsSkipsSelfByBranch(t *testing.T) {
	run := scriptedRunner(t, []step{
		{prefix: "gh pr list", out: `[{"number":1,"headRefName":"crank-me"}]`},
		{prefix: "git fetch origin main"},
		{prefix: "git diff --name-only origin/main...crank-me", out: "a.ts\n"},
	})
	got, checked := scanPRConflicts(run, "crank-me", "main")
	if !checked || len(got) != 0 {
		t.Fatalf("own PR must be excluded: %v %+v", checked, got)
	}
}

func TestScanPRConflictsToolingFailuresReportNothing(t *testing.T) {
	// gh unavailable → checked=false (the control plane keeps prior knowledge).
	run := scriptedRunner(t, []step{{prefix: "gh pr list", err: errors.New("gh: not found")}})
	if _, checked := scanPRConflicts(run, "crank-me", "main"); checked {
		t.Fatal("gh failure must not claim a scan ran")
	}
	// git too old for --write-tree → checked=false, whole scan unusable.
	run = scriptedRunner(t, []step{
		{prefix: "gh pr list", out: `[{"number":7,"headRefName":"x"}]`},
		{prefix: "git fetch origin main"},
		{prefix: "git diff --name-only origin/main...crank-me", out: "a.ts\n"},
		{prefix: "git fetch origin refs/heads/x"},
		{prefix: "git diff --name-only origin/main...FETCH_HEAD", out: "a.ts\n"},
		{prefix: "git merge-tree", out: "usage: git merge-tree <base-commit> <branch1> <branch2>", err: errors.New("exit status 129")},
	})
	if _, checked := scanPRConflicts(run, "crank-me", "main"); checked {
		t.Fatal("old-git merge-tree must invalidate the scan, not report clean")
	}
}
