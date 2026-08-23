package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// THE WORKLIST MUST BE SCOPED TO ONE RUN.
//
// It was named by THREAD id. Every run in a thread therefore wrote and read the same file, and the
// daemon runs two at once by default — so run B overwrote run A's worklist in the window before A's
// crank opened it, and A built B's task.
//
// Prod, 2026-08-15: run ef6b8616 ("Stop the queued-run banner…") executed "Review the changes this
// run produced against its task." — the placeholder text belonging to a REVIEW run in the same
// thread. It branched, ran and reported against that text, found nothing to do for it, and finished
// `no changes · no commit — nothing to keep`.
//
// That last part is why this needs a test rather than a fix and a shrug: it did not crash. It
// reported a plausible SUCCESS for work that was never attempted, and ten cards read as clean
// no-ops. A bug that fails loudly costs an afternoon; one that fabricates a green result costs
// whatever you built on top of believing it.

// Driven through resolveRun, NOT writeRunWorklist directly: resolveRun is where the filename is
// chosen, so a test that calls the writer with an id it picked itself proves only that two different
// strings make two different files. It would pass with the bug fully present — which it did, on the
// first version of this test.
func TestConcurrentRunsInOneThreadDoNotShareAWorklist(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	reg := daemonRegistry{Projects: []daemonProject{{Label: "proj", Path: dir}}}
	const thread = "fa365970-def0-4321-a8f1-630a723ef35c"

	fileArg := func(argv []string) string {
		for i, a := range argv {
			if a == "--file" && i+1 < len(argv) {
				return argv[i+1]
			}
		}
		return ""
	}
	resolve := func(runID, task string) string {
		t.Helper()
		argv, _, err := resolveRun(reg, runRef{ProjectLabel: "proj", ThreadID: thread, RunID: runID, Tasks: []string{task}})
		if err != nil {
			t.Fatal(err)
		}
		f := fileArg(argv)
		if f == "" {
			t.Fatal("resolveRun produced no --file worklist")
		}
		return f
	}

	// A is dispatched, then B — both in the same thread, which is the daemon's ordinary case.
	pathA := resolve("ef6b8616-0e95-4504-bcba-25400823522f", "Stop the queued-run banner claiming a stalled run is about to start")
	pathB := resolve("0096bce8-1111-2222-3333-444455556666", "Review the changes this run produced against its task.")

	if pathA == pathB {
		t.Fatalf("two concurrent runs in one thread share a worklist (%s) — B overwrites A's task", pathA)
	}

	// The decisive assertion: A's crank opens A's file AFTER B has written, and finds A's own task.
	got, err := os.ReadFile(pathA)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "Stop the queued-run banner") {
		t.Errorf("run A's worklist = %q, want its OWN task", string(got))
	}
	if strings.Contains(string(got), "Review the changes this run produced") {
		t.Error("run A's worklist contains run B's task — the exact prod contamination")
	}
	if b, _ := os.ReadFile(pathB); !strings.Contains(string(b), "Review the changes") {
		t.Errorf("run B's worklist = %q, want its own task", string(b))
	}
}

// The fallback path still works for a hand-driven run with no run id — nothing concurrent is
// involved there, and refusing outright would break it for no safety gain.
func TestWorklistFallsBackToTheThreadWithoutARunID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const thread = "fa365970-def0-4321-a8f1-630a723ef35c"

	path, err := writeRunWorklist(thread, []string{"a task"})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != thread+".txt" {
		t.Errorf("fallback filename = %q, want the thread id", filepath.Base(path))
	}
}

// resolveRun must REFUSE a malformed run id rather than let it become a filename. Same posture as
// every other id that reaches the filesystem or an argv on this path.
func TestResolveRunRejectsAMalformedRunID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	reg := daemonRegistry{Projects: []daemonProject{{Label: "proj", Path: dir}}}

	_, _, err := resolveRun(reg, runRef{
		ProjectLabel: "proj",
		ThreadID:     "fa365970-def0-4321-a8f1-630a723ef35c",
		RunID:        "../../etc/passwd",
		Tasks:        []string{"t"},
	})
	if err == nil {
		t.Fatal("a path-traversal run id was accepted as a worklist filename")
	}
	if !strings.Contains(err.Error(), "run id") {
		t.Errorf("error = %v, want it to name the run id", err)
	}
}
