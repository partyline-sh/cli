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

// The anchored-context block gets its OWN markers. injectManagedBlock finds and replaces a block by
// its begin/end pair, so sharing markers between the two would make whichever wrote second REPLACE
// the first — silently, since both writes succeed.
const contextBegin = "<!-- partyline:context BEGIN (managed — what this team knows about the files this task names) -->"
const contextEnd = "<!-- partyline:context END -->"

// globalsFiles are the per-engine memory filenames the block is injected into (see above). AGENTS.md
// leads: it's the neutral standard and the reason this reaches every engine, not just claude.
var globalsFiles = []string{"AGENTS.md", "CLAUDE.md"}

// writeWorktreeGlobals injects `globals` into each of globalsFiles under wtPath. No-op on empty
// globals or path. Best-effort: a read/write error is swallowed (missing project context degrades the
// run, never fails it — same posture as the run-log stream and SeedInclude).
// writeWorktreeContext writes the ANCHORED context — what the team already knows about the files
// this task names — into the same worktree files as the globals block, under its own marker so the
// two never overwrite each other.
//
// It is a separate block, and separately markered, because the two answer different questions: the
// globals block is "how we work here" and is identical for every task, while this is "what bit
// someone last time they touched these files". A worker that sees them merged treats the specific
// warning as more house style.
func writeWorktreeContext(wtPath, anchored string) {
	anchored = strings.TrimSpace(anchored)
	if wtPath == "" || anchored == "" {
		return
	}
	block := contextBegin + "\n" + anchored + "\n" + contextEnd
	for _, name := range globalsFiles {
		injectManagedBlock(filepath.Join(wtPath, name), contextBegin, contextEnd, block)
	}
}

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
		injectManagedBlock(filepath.Join(wtPath, name), globalsBegin, globalsEnd, block)
	}
}

// injectManagedBlock upserts ONE managed block into ONE file: create if absent, replace an existing
// managed block in place (idempotent across a resumed task's re-run), else append after the file's
// real content (never clobbering it). Best-effort per file.
func injectManagedBlock(path, begin, end, block string) {
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
	if i := strings.Index(content, begin); i >= 0 {
		if j := strings.Index(content[i:], end); j >= 0 {
			stop := i + j + len(end)
			_ = os.WriteFile(path, []byte(content[:i]+block+content[stop:]), 0o644)
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
// managedBlocks is EVERY block partyline injects into a worker's memory files. Both the injector and
// the stripper read this one list, so a block can never be added on one side and forgotten on the
// other — which is exactly what happened when anchored context shipped: it was injected, never
// stripped, and rode into every commit. Worse than the original bug, because the context block
// varies PER TASK by design, so two siblings in one chain produced different blocks in the same
// tracked file and conflicted on content neither task wrote.
var managedBlocks = [][2]string{
	{globalsBegin, globalsEnd},
	{contextBegin, contextEnd},
}

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
		changed := false
		// Every block, not just the first: a file carries the globals AND the anchored context, and
		// stopping after one leaves the other in the commit.
		for _, m := range managedBlocks {
			for {
				i := strings.Index(content, m[0])
				if i < 0 {
					break
				}
				j := strings.Index(content[i:], m[1])
				if j < 0 {
					break
				}
				content = content[:i] + content[i+j+len(m[1]):]
				changed = true
			}
		}
		if !changed {
			continue
		}
		if strings.TrimSpace(content) == "" {
			_ = os.Remove(path) // the file WAS the block (injection created it) — remove entirely
			continue
		}
		_ = os.WriteFile(path, []byte(strings.TrimRight(content, "\n")+"\n"), 0o644)
	}
}
