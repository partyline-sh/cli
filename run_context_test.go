package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The globals block and the anchored-context block live in the SAME files. injectManagedBlock finds
// a block by its begin/end pair, so if the two shared markers whichever wrote second would silently
// REPLACE the first — both writes succeed, and the worker just quietly loses half its briefing.
// That was the first version of this code.
func TestGlobalsAndAnchoredContextCoexist(t *testing.T) {
	wt := t.TempDir()
	writeWorktreeGlobals(wt, "Always run npm run build, not just tsc.")
	writeWorktreeContext(wt, "## What this team already knows\n\n- [constraint #142] next build catches what tsc does not.")

	for _, name := range globalsFiles {
		b, err := os.ReadFile(filepath.Join(wt, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := string(b)
		if !strings.Contains(got, "Always run npm run build") {
			t.Errorf("%s: the globals block was lost when context was written", name)
		}
		if !strings.Contains(got, "constraint #142") {
			t.Errorf("%s: the anchored context is missing", name)
		}
	}
}

// Writing twice must be idempotent: a resumed task re-runs this path, and a briefing that doubles on
// every retry crowds out the task itself.
func TestContextBlockIsIdempotent(t *testing.T) {
	wt := t.TempDir()
	for i := 0; i < 3; i++ {
		writeWorktreeGlobals(wt, "rules v1")
		writeWorktreeContext(wt, "context v1")
	}
	b, _ := os.ReadFile(filepath.Join(wt, globalsFiles[0]))
	if n := strings.Count(string(b), contextBegin); n != 1 {
		t.Errorf("context block appears %d times after 3 writes, want 1", n)
	}
	if n := strings.Count(string(b), globalsBegin); n != 1 {
		t.Errorf("globals block appears %d times after 3 writes, want 1", n)
	}
}

// A re-run with DIFFERENT context must replace the old block, not append a second one — otherwise a
// resumed task reads stale guidance next to current guidance with nothing to say which is which.
func TestContextBlockUpdatesInPlace(t *testing.T) {
	wt := t.TempDir()
	writeWorktreeContext(wt, "first briefing")
	writeWorktreeContext(wt, "second briefing")
	b, _ := os.ReadFile(filepath.Join(wt, globalsFiles[0]))
	got := string(b)
	if strings.Contains(got, "first briefing") {
		t.Error("the superseded briefing is still in the file")
	}
	if !strings.Contains(got, "second briefing") {
		t.Error("the current briefing is missing")
	}
}

// The worker's own file must survive. A repo with a real CLAUDE.md must not have it replaced by ours.
func TestExistingFileContentIsPreserved(t *testing.T) {
	wt := t.TempDir()
	path := filepath.Join(wt, globalsFiles[0])
	if err := os.WriteFile(path, []byte("# The repo's own notes\n\nDo not delete me.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeWorktreeContext(wt, "injected context")
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "Do not delete me.") {
		t.Error("the repo's own file content was clobbered")
	}
}

// Empty input writes nothing at all — a run whose tasks name no files must not leave an empty
// managed block behind, which reads as "the team knows nothing about this code".
func TestEmptyContextWritesNothing(t *testing.T) {
	wt := t.TempDir()
	writeWorktreeContext(wt, "   \n  ")
	if _, err := os.Stat(filepath.Join(wt, globalsFiles[0])); !os.IsNotExist(err) {
		t.Error("an empty briefing created a file")
	}
}
