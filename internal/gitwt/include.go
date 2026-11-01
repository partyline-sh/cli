package gitwt

import (
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// SeedInclude copies gitignored files named by the repo's .worktreeinclude into a fresh
// worktree. `git worktree add` checks out only TRACKED files — the .env / .mcp.json /
// local config an agent needs are usually gitignored, so a new worktree starts broken
// without them. A file is copied only when it is BOTH matched by a .worktreeinclude
// pattern AND actually ignored by git (`git check-ignore`) — tracked files never copy
// (the checkout already has them), and a stray pattern can't exfiltrate tracked source.
// Mirrors the .worktreeinclude convention Claude Code Desktop documents.
//
// Best-effort by design: no .worktreeinclude means nothing to do; individual copy
// failures are skipped (the worktree is still valid).
func SeedInclude(repo, worktree string) error {
	lines, err := os.ReadFile(filepath.Join(repo, ".worktreeinclude"))
	if err != nil {
		return nil // no manifest — nothing to seed
	}
	var patterns []string
	for _, ln := range strings.Split(string(lines), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		patterns = append(patterns, strings.TrimPrefix(ln, "./"))
	}
	if len(patterns) == 0 {
		return nil
	}

	// Candidates: walk the repo, skip .git and any nested worktree/submodule (a dir with
	// its own .git), match rel paths against the patterns.
	var candidates []string
	_ = filepath.WalkDir(repo, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, rerr := filepath.Rel(repo, p)
		if rerr != nil || rel == "." {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			if _, e := os.Stat(filepath.Join(p, ".git")); e == nil {
				return filepath.SkipDir // nested worktree / submodule
			}
			return nil
		}
		if matchesInclude(rel, patterns) {
			candidates = append(candidates, rel)
		}
		return nil
	})
	if len(candidates) == 0 {
		return nil
	}

	// Intersect with what git actually ignores. check-ignore exits 1 for "none ignored" —
	// that's an empty result, not an error.
	for _, rel := range gitIgnored(repo, candidates) {
		src, dst := filepath.Join(repo, rel), filepath.Join(worktree, rel)
		if _, err := os.Stat(dst); err == nil {
			continue // never clobber something already in the worktree
		}
		_ = copyFile(src, dst)
	}
	return nil
}

// matchesInclude implements the small pattern language a .worktreeinclude needs:
// an exact rel path, a directory prefix ("secrets/" or "secrets" matches everything
// under it), or a glob applied to both the full rel path and the basename (".env*",
// "config/*.local.json").
func matchesInclude(rel string, patterns []string) bool {
	rel = filepath.ToSlash(rel)
	for _, pat := range patterns {
		pat = strings.TrimSuffix(filepath.ToSlash(pat), "/")
		if pat == "" {
			continue
		}
		if rel == pat || strings.HasPrefix(rel, pat+"/") {
			return true
		}
		if ok, _ := path.Match(pat, rel); ok {
			return true
		}
		if ok, _ := path.Match(pat, path.Base(rel)); ok {
			return true
		}
	}
	return false
}

// gitIgnored filters rel paths down to the ones git ignores, via one
// `git check-ignore --stdin -z` round-trip.
func gitIgnored(repo string, rels []string) []string {
	cmd := gitCmd(repo, "check-ignore", "-z", "--stdin")
	cmd.Stdin = strings.NewReader(strings.Join(rels, "\x00"))
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return nil // exit 1 with no output = nothing ignored
	}
	var res []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			res = append(res, p)
		}
	}
	return res
}

func copyFile(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil || !info.Mode().IsRegular() {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, cerr := io.Copy(out, in)
	if err := out.Close(); cerr == nil {
		cerr = err
	}
	return cerr
}
