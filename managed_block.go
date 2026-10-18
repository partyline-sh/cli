package main

// Managed blocks: a region of a file that a tool owns and a human does not.
//
// Partyline writes into files people also hand-edit — AGENTS.md, CLAUDE.md, GEMINI.md. Every write
// is therefore an upsert bounded by a begin/end marker pair, never a truncating write, so prose
// around the block survives. Each distinct block needs its OWN marker pair: sharing one pair
// between two blocks makes whichever writes second replace the first.
//
// This lives in its own file rather than beside its first caller because the memory layer depends
// on it and outlives that caller.

import (
	"os"
	"strings"
)

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
