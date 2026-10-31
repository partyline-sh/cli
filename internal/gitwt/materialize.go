package gitwt

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MaterializeWIP replays the source checkout's uncommitted work into a destination
// worktree — what makes "fork this agent's in-progress session into a parallel worktree"
// possible, not just branching from the last commit. Staged + unstaged changes travel as
// one binary diff (HEAD..worktree) piped to git apply; untracked (non-ignored) files are
// copied. Refuses while the source is mid-rebase/merge/cherry-pick/revert/bisect — a
// half-finished operation would replay as garbage.
func MaterializeWIP(src, dst string) error {
	for _, marker := range []string{"rebase-merge", "rebase-apply", "MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "BISECT_LOG"} {
		p, err := git(src, "rev-parse", "--git-path", marker)
		if err != nil {
			continue
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(src, p)
		}
		if _, err := os.Stat(p); err == nil {
			return fmt.Errorf("source is mid-%s — finish or abort it before forking with state", strings.ToLower(strings.TrimSuffix(marker, "_HEAD")))
		}
	}
	if _, err := git(src, "rev-parse", "--verify", "HEAD"); err != nil {
		return fmt.Errorf("source has no commits — nothing to diff against")
	}

	// Staged + unstaged vs HEAD, binary-safe, applied atomically in the destination.
	diff, err := gitCmd(src, "diff", "--binary", "HEAD").Output()
	if err != nil {
		return fmt.Errorf("git diff: %w", err)
	}
	if len(bytes.TrimSpace(diff)) > 0 {
		apply := gitCmd(dst, "apply", "--whitespace=nowarn")
		apply.Stdin = bytes.NewReader(diff)
		if out, err := apply.CombinedOutput(); err != nil {
			return fmt.Errorf("git apply: %v\n%s", err, out)
		}
	}

	// Untracked, non-ignored files ride along too (new files the agent just wrote).
	out, err := gitCmd(src, "ls-files", "--others", "--exclude-standard", "-z").Output()
	if err != nil {
		return nil // best-effort — the diff already landed
	}
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel == "" {
			continue
		}
		_ = copyFile(filepath.Join(src, rel), filepath.Join(dst, rel))
	}
	return nil
}
