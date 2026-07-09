// Package gitwt creates and removes git worktrees for isolated agent sessions (E3).
//
// Mechanics follow the battle-tested shape of agent-deck's internal/git (MIT): branch
// resolution (existing local → reuse; on origin → track; new → rooted at a FRESH
// origin/<default> so parallel agents don't fork from a stale local HEAD), and — the
// part that actually matters — teardown that interrogates git before deleting anything,
// so a bug can never rm -rf the user's real repository.
package gitwt

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// gitCmd builds a git command against a repo/dir (prompts disabled, so nothing hangs
// waiting for credentials).
func gitCmd(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return cmd
}

// git runs a git command and returns trimmed combined output.
func git(dir string, args ...string) (string, error) {
	out, err := gitCmd(dir, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// RepoRoot resolves the repository work-tree root containing dir ("" → not a repo).
func RepoRoot(dir string) (string, error) {
	out, err := git(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("not a git repository: %s", dir)
	}
	return out, nil
}

var slugRe = regexp.MustCompile(`[^a-zA-Z0-9._/-]+`)

// Slug sanitizes a name into a git-legal branch component: illegal runs collapse to '-',
// no leading/trailing separators, no ".." or ".lock".
func Slug(name string) string {
	s := slugRe.ReplaceAllString(strings.TrimSpace(name), "-")
	s = strings.Trim(s, "-/.")
	s = strings.ReplaceAll(s, "..", "-")
	s = strings.TrimSuffix(s, ".lock")
	return s
}

// Path returns where a worktree for (repo, branch) lives: a SIBLING of the repo —
// `<parent>/<repoName>--<slug>` with '/' in the branch flattened. Sibling placement
// (not a subdirectory) keeps the worktree out of the parent repo's untracked files,
// so nothing needs gitignoring and `git status` in the main tree stays clean.
func Path(repo, branch string) string {
	flat := strings.ReplaceAll(Slug(branch), "/", "-")
	return filepath.Join(filepath.Dir(repo), filepath.Base(repo)+"--"+flat)
}

// defaultBranchRef returns "origin/<default>" after a best-effort refresh, or "" when
// there's no usable remote. New branches root here — a FRESH remote default — instead of
// the caller's possibly-stale local HEAD.
func defaultBranchRef(repo string) string {
	head, err := git(repo, "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	if err != nil || !strings.HasPrefix(head, "origin/") {
		return ""
	}
	name := strings.TrimPrefix(head, "origin/")
	// Refresh it (best-effort — offline just uses what we have).
	_, _ = git(repo, "fetch", "origin", name)
	return head
}

// Create makes a worktree for `name` next to the repo and returns (path, branch).
//   - existing local branch  → reuse it
//   - branch exists on origin → local tracking branch
//   - otherwise              → new branch off a fresh origin/<default> (fallback: HEAD)
//
// Creating over an existing directory fails (git refuses) — no silent reuse.
func Create(repo, name string) (string, string, error) { return create(repo, name, "") }

// CreateFrom is Create with an explicit base commit-ish for a NEW branch — fork semantics:
// the worktree starts exactly where `base` is (e.g. the forking session's HEAD), not at
// origin/<default>. Existing local/origin branches still win over the base.
func CreateFrom(repo, name, base string) (string, string, error) { return create(repo, name, base) }

func create(repo, name, base string) (string, string, error) {
	branch := Slug(name)
	if branch == "" {
		return "", "", fmt.Errorf("worktree needs a usable branch name (got %q)", name)
	}
	path := Path(repo, branch)
	if _, err := os.Stat(path); err == nil {
		// The directory already exists. If it's this branch's linked worktree, reuse it —
		// "launch another agent in the same worktree" is a legitimate ask; anything else is a
		// conflict the user must resolve.
		if IsLinkedWorktree(path) {
			if cur, _ := git(path, "branch", "--show-current"); cur == branch {
				return path, branch, nil
			}
		}
		return "", "", fmt.Errorf("%s already exists and isn't this branch's worktree", path)
	}

	var err error
	switch {
	case hasLocalBranch(repo, branch):
		_, err = git(repo, "worktree", "add", path, branch)
	case hasRemoteBranch(repo, branch):
		_, err = git(repo, "worktree", "add", "--track", "-b", branch, path, "origin/"+branch)
	case base != "":
		_, err = git(repo, "worktree", "add", "-b", branch, path, base)
	default:
		if def := defaultBranchRef(repo); def != "" {
			_, err = git(repo, "worktree", "add", "-b", branch, path, def)
		} else {
			_, err = git(repo, "worktree", "add", "-b", branch, path)
		}
	}
	if err != nil {
		return "", "", fmt.Errorf("git worktree add: %w", err)
	}
	return path, branch, nil
}

func hasLocalBranch(repo, branch string) bool {
	_, err := git(repo, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

func hasRemoteBranch(repo, branch string) bool {
	_, err := git(repo, "show-ref", "--verify", "--quiet", "refs/remotes/origin/"+branch)
	return err == nil
}

// IsLinkedWorktree reports whether path is a LINKED worktree — never the main working
// tree. It asks git, not path conventions: a linked worktree's git dir lives at
// `<repo>/.git/worktrees/<id>` (parent basename "worktrees"), the main tree's at
// `<repo>/.git`. This is the load-bearing guard for every destructive operation.
func IsLinkedWorktree(path string) bool {
	if out, err := git(path, "rev-parse", "--absolute-git-dir"); err == nil && out != "" {
		return filepath.Base(filepath.Dir(out)) == "worktrees"
	}
	// Orphaned worktree (its admin dir was pruned): .git is a FILE "gitdir: …/worktrees/<id>".
	b, err := os.ReadFile(filepath.Join(path, ".git"))
	if err != nil {
		return false
	}
	return strings.Contains(string(b), "/worktrees/")
}

// Remove tears a worktree down. Hard gates, all of which must hold:
// the path is a linked worktree (per git, above), and it is not the repo root itself
// (compared via EvalSymlinks so /var vs /private/var can't slip through on macOS).
// After `git worktree remove --force` a stubborn tree (node_modules etc.) falls back to
// os.RemoveAll — allowed ONLY because the linked-worktree gate already passed.
func Remove(repo, path string) error {
	rp, _ := filepath.EvalSymlinks(repo)
	pp, _ := filepath.EvalSymlinks(path)
	if rp == "" || pp == "" || rp == pp {
		return fmt.Errorf("refusing to remove %s: it is the repository itself", path)
	}
	if !IsLinkedWorktree(path) {
		return fmt.Errorf("refusing to remove %s: not a linked git worktree", path)
	}
	if _, err := git(repo, "worktree", "remove", "--force", path); err == nil {
		return nil
	}
	// Force-remove failed (untracked bulk, locked files). The gates above make this safe.
	if err := os.RemoveAll(pp); err != nil {
		return err
	}
	_, _ = git(repo, "worktree", "prune")
	return nil
}

// DeleteBranch force-deletes a LOCAL branch (best-effort). crank --restart calls this AFTER Remove
// so a "start over" run doesn't reuse the prior attempt's commits — with the branch gone, the next
// Create roots a fresh branch off origin/<default>. A missing branch, or one still checked out in a
// worktree (Remove must run first), just returns an error the caller ignores. Never touches remotes.
func DeleteBranch(repo, branch string) error {
	_, err := git(repo, "branch", "-D", branch)
	return err
}

// List returns the repo's linked worktrees as (path, branch) pairs — the main working
// tree is excluded (it's not removable, so callers shouldn't see it as one).
func List(repo string) ([][2]string, error) {
	out, err := git(repo, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var res [][2]string
	var path, branch string
	flush := func() {
		if path != "" && IsLinkedWorktree(path) {
			res = append(res, [2]string{path, branch})
		}
		path, branch = "", ""
	}
	for _, ln := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(ln, "worktree "):
			flush()
			path = strings.TrimPrefix(ln, "worktree ")
		case strings.HasPrefix(ln, "branch refs/heads/"):
			branch = strings.TrimPrefix(ln, "branch refs/heads/")
		}
	}
	flush()
	return res, nil
}
