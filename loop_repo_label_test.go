package main

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// A worktree is labelled by its repository first; the main checkout and a non-repository by their folder.
func TestRepoLabel(t *testing.T) {
	main := filepath.Join(t.TempDir(), "proj")
	wt := filepath.Join(t.TempDir(), "feature")
	for _, c := range [][]string{{"init", "-q", main}, {"-C", main, "commit", "-q", "--allow-empty", "-m", "x"}, {"-C", main, "worktree", "add", "-q", wt}} {
		if out, err := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, c...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", c, err, out)
		}
	}
	plain := t.TempDir()
	for dir, want := range map[string]string{main: "proj", wt: "proj (feature)", plain: filepath.Base(plain)} {
		if got := repoLabel(dir); got != want {
			t.Errorf("repoLabel(%s) = %q, want %q", dir, got, want)
		}
	}
}
