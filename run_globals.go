package main

import (
	"os"
	"path/filepath"
	"strings"
)

// Phase B3 — project globals injection. A crank worker runs `claude` in a per-task git WORKTREE (a
// sibling of the repo), and `git worktree add` only checks out TRACKED files — so a CLAUDE.md written
// into the repo root does NOT reach the worker. We instead write the project's globals document
// (projects.document, delivered on the run event) directly INTO the worktree as CLAUDE.md, where the
// worker reads it natively in its cwd. Wrapped in managed markers and upserted so it never clobbers a
// real CLAUDE.md the worktree already carries (tracked, or copied in by .worktreeinclude/SeedInclude).

const globalsBegin = "<!-- partyline:globals BEGIN (managed — the project document injected for this run) -->"
const globalsEnd = "<!-- partyline:globals END -->"

// writeWorktreeGlobals injects `globals` into <wtPath>/CLAUDE.md. No-op on empty globals or path.
// Best-effort: a read/write error is swallowed (missing project context degrades the run, never fails
// it — same posture as the run-log stream and SeedInclude).
func writeWorktreeGlobals(wtPath, globals string) {
	globals = strings.TrimSpace(globals)
	if wtPath == "" || globals == "" {
		return
	}
	block := globalsBegin + "\n" +
		"# Project globals\n\n" +
		"This project's rules, stack, and guardrails — authoritative context for this task. Follow them.\n\n" +
		globals + "\n" +
		globalsEnd
	path := filepath.Join(wtPath, "CLAUDE.md")

	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		_ = os.WriteFile(path, []byte(block+"\n"), 0o644)
		return
	}
	if err != nil {
		return
	}
	content := string(b)
	// Replace an existing managed block in place (idempotent across a resumed task's re-run).
	if i := strings.Index(content, globalsBegin); i >= 0 {
		if j := strings.Index(content[i:], globalsEnd); j >= 0 {
			end := i + j + len(globalsEnd)
			_ = os.WriteFile(path, []byte(content[:i]+block+content[end:]), 0o644)
			return
		}
	}
	// Otherwise append after the existing (real) CLAUDE.md content, preserving it.
	sep := "\n\n"
	if strings.HasSuffix(content, "\n\n") {
		sep = ""
	} else if strings.HasSuffix(content, "\n") {
		sep = "\n"
	}
	_ = os.WriteFile(path, []byte(content+sep+block+"\n"), 0o644)
}
