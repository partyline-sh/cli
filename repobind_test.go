package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The breadcrumb must be idempotent (re-binding the same thread yields byte-identical content — no
// git churn) and update in place when the thread changes (exactly one managed block, ever).
func TestBreadcrumbUpsertIdempotentAndReplaces(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(p, []byte("# My Repo\n\nExisting agent notes.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := upsertBreadcrumb(p, "thread-abc"); err != nil {
		t.Fatal(err)
	}
	after1, _ := os.ReadFile(p)
	if !strings.Contains(string(after1), "Existing agent notes.") {
		t.Fatal("surrounding content was not preserved")
	}
	if !strings.Contains(string(after1), "ptln thread recall thread-abc") {
		t.Fatal("breadcrumb missing the recall command with the thread id")
	}

	// Re-running with the same id must be a no-op (byte-identical).
	if err := upsertBreadcrumb(p, "thread-abc"); err != nil {
		t.Fatal(err)
	}
	after2, _ := os.ReadFile(p)
	if string(after1) != string(after2) {
		t.Fatal("re-binding the same thread changed the file — not idempotent")
	}

	// Changing the thread updates in place; exactly ONE managed block remains.
	if err := upsertBreadcrumb(p, "thread-xyz"); err != nil {
		t.Fatal(err)
	}
	final, _ := os.ReadFile(p)
	if strings.Count(string(final), breadcrumbBegin) != 1 {
		t.Fatalf("expected exactly one managed block, got %d", strings.Count(string(final), breadcrumbBegin))
	}
	if strings.Contains(string(final), "thread-abc") || !strings.Contains(string(final), "thread-xyz") {
		t.Fatal("block did not update to the new thread id")
	}
}

// Removing the block restores surrounding content; a file created solely for the breadcrumb is deleted.
func TestBreadcrumbRemove(t *testing.T) {
	dir := t.TempDir()

	// File with other content → block removed, content kept, file stays.
	withContent := filepath.Join(dir, "CLAUDE.md")
	_ = os.WriteFile(withContent, []byte("# Keep me\n"), 0o644)
	_ = upsertBreadcrumb(withContent, "t1")
	removeBreadcrumb(withContent)
	got, err := os.ReadFile(withContent)
	if err != nil {
		t.Fatal("file with surrounding content should survive removal")
	}
	if strings.Contains(string(got), breadcrumbBegin) {
		t.Fatal("managed block was not removed")
	}
	if !strings.Contains(string(got), "# Keep me") {
		t.Fatal("surrounding content was lost")
	}

	// File that is ONLY the breadcrumb (we created it) → deleted on removal.
	only := filepath.Join(dir, "AGENTS.md")
	_ = upsertBreadcrumb(only, "t2") // creates the file with just the block
	removeBreadcrumb(only)
	if _, err := os.Stat(only); !os.IsNotExist(err) {
		t.Fatal("a breadcrumb-only file should be deleted on removal")
	}
}
