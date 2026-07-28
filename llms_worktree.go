// Worktree awareness for the switchboard.
//
// The switchboard groups sessions by project, and the project label is just
// filepath.Base(cwd) — so every git WORKTREE became its own TOP-LEVEL project,
// sibling to the repo it was cut from. On a machine where an agent creates a
// worktree per task that means 30 "projects" and the real ones drown. Here we
// resolve a worktree's PARENT repo deterministically and attribute the session
// to it; the switchboard then renders those sessions as a collapsed child group
// (see buildRows / headerRow in llms_tui.go).
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// The segment git puts in a worktree's gitdir pointer: <parent>/.git/worktrees/<name>.
// Forward slashes are what git writes in the pointer file on every platform.
const wtSegment = "/.git/worktrees/"

// worktreeParent resolves dir to the repo it is a worktree OF. Detection is exact —
// no name-pattern matching — and every failure below means "not a worktree", which
// leaves the caller on the old behaviour (dir is its own project):
//
//   - <dir>/.git must exist and be a regular FILE. Missing => not a repo at all;
//     a DIRECTORY => an ordinary repo, which is never a worktree.
//   - it must be readable, and small (the pointer is one short line — a big file
//     is something else wearing the name).
//   - its contents must start with "gitdir:" and name a non-empty path. A relative
//     path (git worktree --relative-paths) is resolved against dir.
//   - that path must contain "/.git/worktrees/<name>", with a non-empty parent
//     before the segment and a non-empty name after it. Anything else — a gitdir
//     pointing at a submodule's .git/modules/…, say — is not a worktree.
//
// gitwt.IsLinkedWorktree answers a similar question but forks `git rev-parse` and returns
// only a bool; this runs in the session scan over every project dir, so it has to be pure
// filesystem reads and it needs the PARENT path, not a yes/no.
func worktreeParent(dir string) (string, bool) {
	if dir == "" {
		return "", false
	}
	p := filepath.Join(dir, ".git")
	fi, err := os.Lstat(p)
	if err != nil || fi.IsDir() || !fi.Mode().IsRegular() || fi.Size() > 4096 {
		return "", false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	rest, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
	if !ok {
		return "", false
	}
	gitdir := strings.TrimSpace(rest)
	if gitdir == "" {
		return "", false
	}
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(dir, gitdir)
	}
	gitdir = filepath.ToSlash(filepath.Clean(gitdir))
	i := strings.Index(gitdir, wtSegment)
	if i <= 0 || strings.TrimSpace(gitdir[i+len(wtSegment):]) == "" {
		return "", false
	}
	return filepath.FromSlash(gitdir[:i]), true
}

// projKey is the switchboard's grouping key for a session: the PARENT repo when the
// session's cwd is (or was) a git worktree, else the cwd itself.
func (s aiSession) projKey() string {
	if s.ProjDir != "" {
		return s.ProjDir
	}
	return s.Cwd
}

// ---- worktree index -------------------------------------------------------
// You cannot read .git out of a directory that no longer exists, so a session whose
// worktree has been deleted is indistinguishable from a session in any other deleted
// dir — and those get the recover modal, which is meaningless for a worktree. So we
// remember dir -> parent for every worktree we ever resolve. That memory is what makes
// "this is a DEAD worktree session" a fact rather than a guess (see llms_prune.go).

func wtIndexPath() string { return filepath.Join(stateDir(), "worktree-index.json") }

func loadWtIndex() map[string]string {
	m := map[string]string{}
	if b, err := os.ReadFile(wtIndexPath()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func saveWtIndex(m map[string]string) {
	if b, err := json.Marshal(m); err == nil {
		_ = os.WriteFile(wtIndexPath(), b, 0o600)
	}
}

// attributeProjects fills ProjDir/WtName/DeadWt on every session.
//
// PERF CONSTRAINT: collectSessions runs over thousands of sessions and the switchboard
// warm-opens in ~18ms thanks to the scan cache (llms_index.go) — this must not undo
// that. So the filesystem work is done ONCE PER UNIQUE cwd per scan (a few dozen dirs,
// one Lstat plus at most one small read each), never once per session, and never per
// render. The switchboard reads only the fields set here.
func attributeProjects(sessions []aiSession) {
	idx, dirty := loadWtIndex(), false
	type attr struct {
		proj string
		wt   string
		dead bool
	}
	cache := make(map[string]attr, 64)
	live := make(map[string]bool, 64) // cwds seen this pass — the index is pruned to these
	for i := range sessions {
		cwd := sessions[i].Cwd
		if cwd == "" {
			continue
		}
		live[cwd] = true
		a, seen := cache[cwd]
		if !seen {
			a = attr{proj: cwd} // default: the dir is its own project (today's behaviour)
			if parent, ok := worktreeParent(cwd); ok {
				a = attr{proj: parent, wt: filepath.Base(cwd)}
				if idx[cwd] != parent {
					idx[cwd], dirty = parent, true
				}
			} else if _, err := os.Stat(cwd); err != nil {
				// Gone. Only the index can say it USED to be a worktree; if it can't,
				// fall through to today's behaviour (own project + recover modal).
				if parent, known := idx[cwd]; known {
					a = attr{proj: parent, wt: filepath.Base(cwd), dead: true}
				}
			}
			cache[cwd] = a
		}
		sessions[i].ProjDir, sessions[i].WtName, sessions[i].DeadWt = a.proj, a.wt, a.dead
	}
	// Keep the index bounded: drop dirs no session references any more.
	for d := range idx {
		if !live[d] {
			delete(idx, d)
			dirty = true
		}
	}
	if dirty {
		saveWtIndex(idx)
	}
}
