package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeWorktreeGlobals must (1) create CLAUDE.md when absent, (2) NEVER clobber a real CLAUDE.md the
// worktree already carries (append instead), (3) be idempotent — a re-run replaces its own managed
// block in place rather than stacking copies, and (4) no-op on empty globals.
func TestWriteWorktreeGlobals(t *testing.T) {
	read := func(dir string) string {
		b, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
		return string(b)
	}

	t.Run("creates when absent", func(t *testing.T) {
		dir := t.TempDir()
		writeWorktreeGlobals(dir, "always run make lint")
		got := read(dir)
		if !strings.Contains(got, "always run make lint") || !strings.Contains(got, globalsBegin) {
			t.Fatalf("expected managed block with globals, got:\n%s", got)
		}
	})

	t.Run("preserves an existing CLAUDE.md (no clobber)", func(t *testing.T) {
		dir := t.TempDir()
		existing := "# Repo's own CLAUDE.md\n\nHand-written guidance.\n"
		if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(existing), 0o644); err != nil {
			t.Fatal(err)
		}
		writeWorktreeGlobals(dir, "never touch prod")
		got := read(dir)
		if !strings.Contains(got, "Hand-written guidance.") {
			t.Fatalf("clobbered the existing CLAUDE.md:\n%s", got)
		}
		if !strings.Contains(got, "never touch prod") {
			t.Fatalf("did not append the globals block:\n%s", got)
		}
		if !strings.HasPrefix(got, "# Repo's own CLAUDE.md") {
			t.Fatalf("existing content must stay first:\n%s", got)
		}
	})

	t.Run("idempotent — replaces its own block in place", func(t *testing.T) {
		dir := t.TempDir()
		writeWorktreeGlobals(dir, "first version")
		writeWorktreeGlobals(dir, "second version")
		got := read(dir)
		if strings.Count(got, globalsBegin) != 1 {
			t.Fatalf("managed block stacked instead of replacing:\n%s", got)
		}
		if strings.Contains(got, "first version") || !strings.Contains(got, "second version") {
			t.Fatalf("expected only the latest globals:\n%s", got)
		}
	})

	t.Run("no-op on empty globals", func(t *testing.T) {
		dir := t.TempDir()
		writeWorktreeGlobals(dir, "   ")
		if _, err := os.Stat(filepath.Join(dir, "CLAUDE.md")); !os.IsNotExist(err) {
			t.Fatalf("empty globals must not create CLAUDE.md")
		}
	})
}
