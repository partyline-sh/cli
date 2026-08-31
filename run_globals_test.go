package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeWorktreeGlobals injects the project globals into EVERY per-engine memory file (globalsFiles:
// AGENTS.md — the vendor-neutral AAIF standard read by codex/gemini/opencode/goose + modern claude —
// and CLAUDE.md for claude back-compat). It must, for each: (1) create when absent, (2) NEVER clobber
// a real file the worktree already carries (append instead), (3) be idempotent — a re-run replaces
// its own managed block in place rather than stacking copies, and (4) no-op on empty globals.
func TestWriteWorktreeGlobals(t *testing.T) {
	read := func(dir, name string) string {
		b, _ := os.ReadFile(filepath.Join(dir, name))
		return string(b)
	}

	t.Run("creates the block in EVERY globals file when absent", func(t *testing.T) {
		dir := t.TempDir()
		writeWorktreeGlobals(dir, "always run make lint")
		for _, name := range globalsFiles {
			got := read(dir, name)
			if !strings.Contains(got, "always run make lint") || !strings.Contains(got, globalsBegin) {
				t.Fatalf("%s: expected managed block with globals, got:\n%s", name, got)
			}
		}
		// The neutral standard must be one of them — that's the whole point of the fix.
		if !strings.Contains(strings.Join(globalsFiles, ","), "AGENTS.md") {
			t.Fatal("AGENTS.md (the vendor-neutral standard) must be a target file")
		}
	})

	t.Run("preserves an existing file (no clobber), per file", func(t *testing.T) {
		dir := t.TempDir()
		existing := "# Repo's own AGENTS.md\n\nHand-written guidance.\n"
		if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(existing), 0o644); err != nil {
			t.Fatal(err)
		}
		writeWorktreeGlobals(dir, "never touch prod")
		got := read(dir, "AGENTS.md")
		if !strings.Contains(got, "Hand-written guidance.") {
			t.Fatalf("clobbered the existing AGENTS.md:\n%s", got)
		}
		if !strings.Contains(got, "never touch prod") {
			t.Fatalf("did not append the globals block:\n%s", got)
		}
		if !strings.HasPrefix(got, "# Repo's own AGENTS.md") {
			t.Fatalf("existing content must stay first:\n%s", got)
		}
	})

	t.Run("idempotent — replaces its own block in place, per file", func(t *testing.T) {
		dir := t.TempDir()
		writeWorktreeGlobals(dir, "first version")
		writeWorktreeGlobals(dir, "second version")
		for _, name := range globalsFiles {
			got := read(dir, name)
			if strings.Count(got, globalsBegin) != 1 {
				t.Fatalf("%s: managed block stacked instead of replacing:\n%s", name, got)
			}
			if strings.Contains(got, "first version") || !strings.Contains(got, "second version") {
				t.Fatalf("%s: expected only the latest globals:\n%s", name, got)
			}
		}
	})

	t.Run("no-op on empty globals — no file created", func(t *testing.T) {
		dir := t.TempDir()
		writeWorktreeGlobals(dir, "   ")
		for _, name := range globalsFiles {
			if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
				t.Fatalf("empty globals must not create %s", name)
			}
		}
	})
}

// stripWorktreeGlobals is the commit-side inverse of writeWorktreeGlobals: the injected block must
// never reach a commit/PR. A file the injection CREATED disappears entirely; a file that had real
// content keeps exactly that content; the agent's own edits outside the block survive.
func TestStripWorktreeGlobals(t *testing.T) {
	dir := t.TempDir()
	// CLAUDE.md pre-exists with real content; AGENTS.md doesn't.
	orig := "# My project\n\nReal rules here.\n"
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	writeWorktreeGlobals(dir, "injected project doc")

	// Sanity: injection landed in both files.
	if b, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md")); !strings.Contains(string(b), "injected project doc") {
		t.Fatal("injection didn't create AGENTS.md")
	}

	stripWorktreeGlobals(dir)

	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatal("AGENTS.md was pure injection — strip must remove it entirely")
	}
	b, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); strings.Contains(got, "partyline:globals") || !strings.Contains(got, "Real rules here.") {
		t.Fatalf("CLAUDE.md must return to its real content, got: %q", got)
	}

	// Idempotent + safe on untouched trees.
	stripWorktreeGlobals(dir)
	// And an agent's own edit outside the block survives a later inject+strip cycle.
	edited := "# My project\n\nReal rules here.\nAgent-added rule.\n"
	_ = os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(edited), 0o644)
	writeWorktreeGlobals(dir, "injected project doc")
	stripWorktreeGlobals(dir)
	b, _ = os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if !strings.Contains(string(b), "Agent-added rule.") {
		t.Fatal("the agent's own edit outside the managed block must survive")
	}
}
