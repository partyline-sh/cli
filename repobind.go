package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/gitwt"
)

// A repo binds itself to its Context Thread with a `.partyline.json` at the repo root
// (E3.5). It's meant to be CHECKED IN — the repo itself declares where its shared memory
// lives, so every teammate (and every parallel worktree agent) who launches a session in
// this repo auto-attaches to the same thread. Opt out per launch with --no-thread.
type repoBind struct {
	Thread string `json:"thread,omitempty"`
}

func repoBindPath(repo string) string { return filepath.Join(repo, ".partyline.json") }

// loadRepoBind returns the thread bound to dir's repository ("" when none / not a repo).
func loadRepoBind(dir string) string {
	repo, err := gitwt.RepoRoot(dir)
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(repoBindPath(repo))
	if err != nil {
		return ""
	}
	var rb repoBind
	if json.Unmarshal(b, &rb) != nil {
		return ""
	}
	return strings.TrimSpace(rb.Thread)
}

// threadBind is `ptln thread bind [<id> | --clear]`: bind the current repo to a thread
// (no args shows the current binding).
func threadBind(c *api.Client, args []string) {
	dir, _ := os.Getwd()
	repo, err := gitwt.RepoRoot(dir)
	if err != nil {
		fatal(fmt.Errorf("thread bind works inside a git repository: %w", err))
	}
	p := repoBindPath(repo)
	if len(args) == 0 {
		if id := loadRepoBind(dir); id != "" {
			title := id
			if th, _, e := c.GetThread(id); e == nil && th != nil {
				title = th.Title
			}
			fmt.Printf("bound to: %s (%s)\n  every `ptln new` in this repo auto-attaches (--no-thread skips)\n", title, id)
		} else {
			fmt.Println("no binding — `ptln thread bind <id>` writes .partyline.json (check it in to share with the team)")
		}
		return
	}
	rest, yes := takeYesFlag(args)
	if len(rest) == 0 {
		fatal(fmt.Errorf("usage: ptln thread bind [<id> | --clear] [--yes]"))
	}
	if rest[0] == "--clear" {
		// --clear deletes .partyline.json AND rewrites AGENTS.md / CLAUDE.md — three files the user
		// may well have hand-edited around, so it asks before touching any of them.
		if !confirmDestructive("clear the binding: delete .partyline.json and strip the AGENTS.md/CLAUDE.md breadcrumb", yes) {
			return
		}
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			fatal(err)
		}
		removeBreadcrumbs(repo)
		fmt.Println("✓ binding cleared")
		return
	}
	id := strings.TrimSpace(rest[0])
	th, _, err := c.GetThread(id)
	if err != nil || th == nil {
		fatal(fmt.Errorf("can't read thread %s: %v", id, err))
	}
	if err := writeRepoBind(repo, id); err != nil {
		fatal(err)
	}
	fmt.Printf("✓ %s bound to %q\n", filepath.Base(repo), th.Title)
	fmt.Println("  • .partyline.json written (check it in — every `ptln` session here auto-attaches)")
	fmt.Println("  • breadcrumb added to AGENTS.md/CLAUDE.md so even non-partyline agents find the context")
}

// writeRepoBind persists the repo→thread binding (shared by the CLI and the ctrl-\ c menu), then
// drops a breadcrumb into AGENTS.md/CLAUDE.md so agents that AREN'T wired to partyline's MCP still
// discover the shared context. The .partyline.json write is the source of truth (must succeed); the
// breadcrumb is best-effort (a failure never fails the bind).
func writeRepoBind(repo, thread string) error {
	b, _ := json.MarshalIndent(repoBind{Thread: thread}, "", "  ")
	if err := os.WriteFile(repoBindPath(repo), append(b, '\n'), 0o644); err != nil {
		return err
	}
	writeBreadcrumbs(repo, thread)
	return nil
}

// --- Agent breadcrumb (inspired by OpenWiki's AGENTS.md/CLAUDE.md pointer) --------------------
// A managed, comment-delimited block written into the repo's top-level agent-instruction file so
// ANY agent — Claude Code, Cursor, aider, an unwired session — sees "this repo has shared team
// context, here's how to read it" without needing partyline's MCP set up first. Written
// deterministically (no LLM, unlike OpenWiki): the content is fixed, so it's idempotent, testable,
// costs nothing, and can't mangle the file. Edits between the markers are overwritten on re-bind.

const breadcrumbBegin = "<!-- partyline:context BEGIN (managed by `ptln thread bind` — edits between these markers are overwritten) -->"
const breadcrumbEnd = "<!-- partyline:context END -->"

func breadcrumbBlock(thread string) string {
	return breadcrumbBegin + "\n" +
		"## Shared team context (partyline)\n\n" +
		"This repository is bound to a partyline **Context Thread** — the team's shared, durable memory of\n" +
		"decisions, constraints, and interface contracts that span people, machines, and AI sessions. Treat it\n" +
		"as authoritative background: **read it before making changes**, and record any new cross-cutting\n" +
		"decision, constraint, or contract you make.\n\n" +
		"- Read the whole thread:  `ptln thread recall " + thread + "`\n" +
		"- Record a fact:          `ptln thread remember " + thread + " decision \"…\"`  (kinds: decision | constraint | contract)\n\n" +
		"Sessions started with `ptln` in this repo auto-attach to this thread; MCP-wired agents also get\n" +
		"`recall` / `remember` / `read_context` tools automatically.\n" +
		breadcrumbEnd
}

// writeBreadcrumbs applies OpenWiki's sensible file rule: refresh the block in whichever of
// AGENTS.md / CLAUDE.md already exist; if NEITHER exists, create the vendor-neutral AGENTS.md.
func writeBreadcrumbs(repo, thread string) {
	agents, claude := filepath.Join(repo, "AGENTS.md"), filepath.Join(repo, "CLAUDE.md")
	wrote := false
	for _, p := range []string{agents, claude} {
		if _, err := os.Stat(p); err == nil {
			if e := upsertBreadcrumb(p, thread); e == nil {
				wrote = true
			}
		}
	}
	if !wrote { // neither existed — create AGENTS.md (engine-agnostic)
		_ = upsertBreadcrumb(agents, thread)
	}
}

func removeBreadcrumbs(repo string) {
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		removeBreadcrumb(filepath.Join(repo, name))
	}
}

// upsertBreadcrumb writes/refreshes the managed block in path (creating the file if absent),
// replacing an existing block in place and otherwise appending — surrounding content is preserved.
func upsertBreadcrumb(path, thread string) error {
	block := breadcrumbBlock(thread)
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return os.WriteFile(path, []byte(block+"\n"), 0o644)
	}
	if err != nil {
		return err
	}
	content := string(b)
	if i := strings.Index(content, breadcrumbBegin); i >= 0 {
		if j := strings.Index(content[i:], breadcrumbEnd); j >= 0 {
			end := i + j + len(breadcrumbEnd)
			return os.WriteFile(path, []byte(content[:i]+block+content[end:]), 0o644)
		}
	}
	sep := "\n\n"
	if strings.HasSuffix(content, "\n\n") {
		sep = ""
	} else if strings.HasSuffix(content, "\n") {
		sep = "\n"
	}
	return os.WriteFile(path, []byte(content+sep+block+"\n"), 0o644)
}

// removeBreadcrumb strips the managed block (and its surrounding blank lines) from path. If the file
// is left empty (we had created it solely for the breadcrumb), it's deleted. No-op if absent.
func removeBreadcrumb(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	content := string(b)
	i := strings.Index(content, breadcrumbBegin)
	if i < 0 {
		return
	}
	j := strings.Index(content[i:], breadcrumbEnd)
	if j < 0 {
		return
	}
	end := i + j + len(breadcrumbEnd)
	for end < len(content) && content[end] == '\n' { // eat trailing newlines
		end++
	}
	out := strings.TrimRight(content[:i], "\n")
	if rest := content[end:]; rest != "" {
		out += "\n\n" + rest
	}
	if strings.TrimSpace(out) == "" {
		_ = os.Remove(path)
		return
	}
	_ = os.WriteFile(path, []byte(strings.TrimRight(out, "\n")+"\n"), 0o644)
}

// writeRepoPin writes ONLY the .partyline.json pin — no agent-file breadcrumbs.
//
// The narrower sibling of writeRepoBind, for the AUTOMATIC link (#586). An explicit
// `ptln thread bind` is a person saying "set this repo up", and the breadcrumb block in CLAUDE.md /
// AGENTS.md is part of what they asked for. Auto-linking on a first `remember` is partyline making
// a decision on its own, and editing someone's agent-instruction file uninvited is a much larger
// liberty than dropping one small file that the tool response then tells them about.
//
// One file, mentioned out loud, is a helpful default. Two files, one of them theirs, silently, is
// the thing people rightly complain about.
func writeRepoPin(repo, thread string) error {
	b, _ := json.MarshalIndent(repoBind{Thread: thread}, "", "  ")
	return os.WriteFile(repoBindPath(repo), append(b, '\n'), 0o644)
}
