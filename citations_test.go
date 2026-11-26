package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The citation pre-flight: cited paths that exist pass silently; missing ones surface. Prose with
// slashes but no code extension must not false-positive.
func TestStaleCitations(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "web/src"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "web/src/app.tsx"), []byte("x"), 0o644)

	task := "Fix the toggle in web/src/app.tsx:42 and the store at web/src/store.ts — see docs/and/or notes. Not a path: either/or."
	stale := staleCitations(dir, task)
	if len(stale) != 1 || stale[0] != "web/src/store.ts" {
		t.Fatalf("want exactly the missing citation, got %v", stale)
	}
	if note := staleCitationNote(stale); note == "" || staleCitationNote(nil) != "" {
		t.Fatal("note renders for stale citations and only for stale citations")
	}
	// Traversal-shaped citations are never checked or echoed.
	if got := citedPaths("see ../../etc/passwd.sh"); len(got) != 0 {
		t.Fatalf("traversal must be dropped, got %v", got)
	}
}
