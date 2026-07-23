package main

import (
	"os"
	"path/filepath"
	"strings"
)

// Phase B3 — project globals injection. A crank worker runs its engine in a per-task git WORKTREE (a
// sibling of the repo), and `git worktree add` only checks out TRACKED files — so a globals file
// written into the repo root does NOT reach the worker. We instead write the project's globals
// document (projects.document, delivered on the run event) directly INTO the worktree, where the
// worker reads it natively in its cwd. Wrapped in managed markers and upserted so it never clobbers a
// real file the worktree already carries (tracked, or copied in by .worktreeinclude/SeedInclude).
//
// TWO files, because partyline runs five engines and they don't read the same one: AGENTS.md is the
// vendor-neutral standard (an AAIF project) that codex, gemini, opencode, goose — and modern Claude —
// read; CLAUDE.md is claude's own convention, kept for back-compat. Writing only CLAUDE.md (the old
// behavior) meant four of five engines ran BLIND to the project's rules/stack/guardrails. Both get
// the identical managed block; an engine that reads both just sees the same guardrails twice
// (harmless). Aligns with AAIF's AGENTS.md standard.

const globalsBegin = "<!-- partyline:globals BEGIN (managed — the project document injected for this run) -->"
const globalsEnd = "<!-- partyline:globals END -->"

// globalsFiles are the per-engine memory filenames the block is injected into (see above). AGENTS.md
// leads: it's the neutral standard and the reason this reaches every engine, not just claude.
var globalsFiles = []string{"AGENTS.md", "CLAUDE.md"}

// writeWorktreeGlobals injects `globals` into each of globalsFiles under wtPath. No-op on empty
// globals or path. Best-effort: a read/write error is swallowed (missing project context degrades the
// run, never fails it — same posture as the run-log stream and SeedInclude).
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
	for _, name := range globalsFiles {
		injectGlobalsBlock(filepath.Join(wtPath, name), block)
	}
}

// injectGlobalsBlock upserts the managed block into ONE file: create if absent, replace an existing
// managed block in place (idempotent across a resumed task's re-run), else append after the file's
// real content (never clobbering it). Best-effort per file.
func injectGlobalsBlock(path, block string) {
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
	// Otherwise append after the existing (real) file content, preserving it.
	sep := "\n\n"
	if strings.HasSuffix(content, "\n\n") {
		sep = ""
	} else if strings.HasSuffix(content, "\n") {
		sep = "\n"
	}
	_ = os.WriteFile(path, []byte(content+sep+block+"\n"), 0o644)
}

// stripWorktreeGlobals removes the managed globals block from each of globalsFiles before a commit,
// deleting a file the injection created outright (nothing but the block left). Without this, the
// worker's `git add -A` swept the injected AGENTS.md + CLAUDE.md block into EVERY commit — ~200
// lines of "unrelated project-globals boilerplate" in every PR, which the independent reviewer
// (correctly) flagged on run after run and capped otherwise-clean work at B. The block is context
// FOR the worker, never part of the deliverable. Injection is idempotent, so a repair round just
// re-injects after the commit strips.
func stripWorktreeGlobals(wtPath string) {
	if wtPath == "" {
		return
	}
	for _, name := range globalsFiles {
		path := filepath.Join(wtPath, name)
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		content := string(b)
		i := strings.Index(content, globalsBegin)
		if i < 0 {
			continue
		}
		j := strings.Index(content[i:], globalsEnd)
		if j < 0 {
			continue
		}
		rest := content[:i] + content[i+j+len(globalsEnd):]
		if strings.TrimSpace(rest) == "" {
			_ = os.Remove(path) // the file WAS the block (injection created it) — remove entirely
			continue
		}
		_ = os.WriteFile(path, []byte(strings.TrimRight(rest, "\n")+"\n"), 0o644)
	}
}
