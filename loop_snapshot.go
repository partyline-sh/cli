package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// A loop reviews the work as it stood at one moment, not the working tree as it is while the
// review runs: the session, or the person, may keep editing. The snapshot is the working tree
// written as a git tree through a throwaway index (untracked files included, ignored files and
// memory/ left out), then checked out into a temporary directory that reviewers read.
//
// The repository's own index and working tree are never touched.

type loopSnapshot struct {
	Root string // the repository
	Base string // commit the work is compared against
	Tree string // tree id of the work
	Dir  string // checkout of Tree that reviewers read
	tmp  string
}

// snapshotExcludes are left out of the snapshot and its diff. memory/ changes during a session
// (the capture hook writes there) and is not the work under review.
var snapshotExcludes = []string{":(exclude)memory", ":(exclude).ptln"}

func (s *loopSnapshot) Cleanup() {
	if s != nil && s.tmp != "" {
		_ = os.RemoveAll(s.tmp)
	}
}

// takeSnapshot snapshots root's working tree and resolves base (a ref, or "" for the default).
func takeSnapshot(root, base string) (*loopSnapshot, error) {
	resolved, err := resolveLoopBase(root, base)
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "ptln-loop-*")
	if err != nil {
		return nil, err
	}
	s := &loopSnapshot{Root: root, Base: resolved, tmp: tmp}
	index := filepath.Join(tmp, "index")
	if s.Tree, err = writeWorkTree(root, index); err != nil {
		s.Cleanup()
		return nil, err
	}
	s.Dir = filepath.Join(tmp, filepath.Base(root))
	if err := os.Mkdir(s.Dir, 0o755); err != nil {
		s.Cleanup()
		return nil, err
	}
	if _, err := gitIndex(root, index, "--work-tree="+s.Dir, "checkout-index", "-a"); err != nil {
		s.Cleanup()
		return nil, err
	}
	return s, nil
}

// writeWorkTree writes root's working tree as a git tree through the throwaway index at index:
// untracked files included, ignored files and memory/ and .ptln/ left out.
func writeWorkTree(root, index string) (string, error) {
	steps := [][]string{
		{"read-tree", "HEAD"},
		// read-tree brought in the committed memory/ and .ptln/; the excludes on add below only stop
		// them being updated, so remove them from this index outright.
		{"rm", "-r", "-q", "--cached", "--ignore-unmatch", "--", "memory", ".ptln"},
		append([]string{"add", "-A", "--", "."}, snapshotExcludes...),
	}
	for _, a := range steps {
		if _, err := gitIndex(root, index, a...); err != nil {
			return "", err
		}
	}
	return gitIndex(root, index, "write-tree")
}

// loopGitEnv is the environment for partyline's own git commands in a checkout an agent wrote to:
// no fsmonitor program and no hooks from the repository's config run with the person's credentials.
// (A claude builder cannot write .git/config or .git/hooks: checked against claude 2.1.312.)
func loopGitEnv() []string {
	return append(os.Environ(), "GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=core.fsmonitor", "GIT_CONFIG_VALUE_0=false",
		"GIT_CONFIG_KEY_1=core.hooksPath", "GIT_CONFIG_VALUE_1=/dev/null")
}

// gitLoop runs git in dir with loopGitEnv and returns its trimmed output.
func gitLoop(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = loopGitEnv()
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// gitIndex runs git in root against the index file at index. stdout only: git add prints
// warnings (line endings, embedded repositories) on stderr, and a tree id must not have them
// prepended.
func gitIndex(root, index string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	cmd.Env = append(loopGitEnv(), "GIT_INDEX_FILE="+index)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// Untracked lists the files in the snapshot that git does not track, and does not ignore.
func (s *loopSnapshot) Untracked() ([]string, error) {
	out, err := exec.Command("git", append([]string{"-C", s.Root, "ls-files", "--others", "--exclude-standard", "-z", "--", "."}, snapshotExcludes...)...).Output()
	if err != nil {
		return nil, fmt.Errorf("listing untracked files: %v", err)
	}
	return strings.FieldsFunc(string(out), func(r rune) bool { return r == 0 }), nil
}

// loopPatchName is where the full patch sits inside the snapshot.
const loopPatchName = ".ptln-review.patch"

// WritePatch puts the full patch in the snapshot directory for reviewers to read.
func (s *loopSnapshot) WritePatch(diff string) error {
	return os.WriteFile(filepath.Join(s.Dir, loopPatchName), []byte(diff), 0o600)
}

// Diff is the work against its base, as a patch.
func (s *loopSnapshot) Diff() (string, error) {
	out, err := exec.Command("git", append([]string{"-C", s.Root, "diff", "--no-color", "--no-ext-diff", s.Base, s.Tree, "--", "."}, snapshotExcludes...)...).Output()
	if err != nil {
		return "", fmt.Errorf("git diff %s %s: %v", shortSHA(s.Base), shortSHA(s.Tree), err)
	}
	return string(out), nil
}

// resolveLoopBase turns base into a commit. With none given, it is where HEAD left the remote's
// default branch (or main/master), so a feature branch is reviewed as a whole. On the default
// branch, commits not yet pushed are included, because the base is where HEAD left origin/main.
func resolveLoopBase(root, base string) (string, error) {
	rev := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", root}, args...)...).Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	if rev("rev-parse", "--verify", "-q", "HEAD^{commit}") == "" {
		return "", fmt.Errorf("the repository has no commits; commit something to review against")
	}
	if base != "" {
		c := rev("rev-parse", "--verify", "-q", base+"^{commit}")
		if c == "" {
			return "", fmt.Errorf("%q is not a commit in this repository", base)
		}
		return c, nil
	}
	for _, ref := range []string{rev("rev-parse", "--abbrev-ref", "origin/HEAD"), "origin/main", "main", "origin/master", "master"} {
		if ref == "" || rev("rev-parse", "--verify", "-q", ref+"^{commit}") == "" {
			continue
		}
		if mb := rev("merge-base", "HEAD", ref); mb != "" {
			return mb, nil
		}
	}
	return rev("rev-parse", "HEAD"), nil
}
