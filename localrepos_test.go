package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The v1 of this feature was removed because ~220 throwaway crank worktrees evicted the real
// repositories from the list it existed to offer. These tests exist to make that specific failure
// impossible to reintroduce silently: the worktree exclusion is asserted against a real directory
// tree, not against a mock.

// mkRepo makes dir a real repository (.git is a DIRECTORY).
func mkRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// mkWorktree makes dir a LINKED WORKTREE (.git is a FILE holding a gitdir: pointer) — exactly what
// `git worktree add` produces, and exactly what flooded the old candidate list.
func mkWorktree(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /somewhere/.git/worktrees/x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func names(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, filepath.Base(p))
	}
	return out
}

func has(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestScanExcludesWorktreesAndNoise(t *testing.T) {
	home := t.TempDir()
	managed := filepath.Join(home, ".partyline", "daemon", "repos")

	mkRepo(t, filepath.Join(home, "dev", "partyline"))   // a real repo
	mkRepo(t, filepath.Join(home, "dev", "assetmgmt"))   // another
	mkWorktree(t, filepath.Join(home, "dev", "crank-1")) // THE regression: a linked worktree
	mkWorktree(t, filepath.Join(home, "dev", "crank-2"))
	mkRepo(t, filepath.Join(managed, "acme", "web"))                    // a managed clone — partyline's own
	mkRepo(t, filepath.Join(home, ".cache", "vendored"))                // hidden dir
	mkRepo(t, filepath.Join(home, "dev", "app", "node_modules", "dep")) // vendored dep

	got := names(scanLocalRepoPaths(home, managed))

	for _, want := range []string{"partyline", "assetmgmt"} {
		if !has(got, want) {
			t.Errorf("expected real repo %q in %v", want, got)
		}
	}
	for _, bad := range []string{"crank-1", "crank-2", "web", "vendored", "dep"} {
		if has(got, bad) {
			t.Errorf("%q must not be offered, got %v", bad, got)
		}
	}
	if len(got) != 2 {
		t.Errorf("expected exactly the 2 real repos, got %v", got)
	}
}

// A repository's own subdirectories are its files, not more projects. Without this the picker
// would list every package directory of a monorepo.
func TestScanDoesNotDescendIntoARepo(t *testing.T) {
	home := t.TempDir()
	mkRepo(t, filepath.Join(home, "mono"))
	mkRepo(t, filepath.Join(home, "mono", "packages", "inner"))

	got := names(scanLocalRepoPaths(home, ""))
	if len(got) != 1 || got[0] != "mono" {
		t.Errorf("expected only the outer repo, got %v", got)
	}
}

func TestClassifyDir(t *testing.T) {
	cases := []struct {
		name          string
		exists, isDir bool
		want          localRepoKind
	}{
		{"no .git at all", false, false, repoNone},
		{"real repository (.git dir)", true, true, repoInclude},
		{"linked worktree (.git file)", true, false, repoWorktree},
	}
	for _, c := range cases {
		if got := classifyDir(c.exists, c.isDir); got != c.want {
			t.Errorf("%s: classifyDir(%v,%v) = %v, want %v", c.name, c.exists, c.isDir, got, c.want)
		}
	}
}

// The handle is the whole reference-not-command guarantee: it must be stable (a handle picked in
// the browser still resolves seconds later) and path-specific (two repos never collide).
func TestLocalRepoHandleStableAndDistinct(t *testing.T) {
	a := localRepoHandle("/Users/x/dev/app")
	if a != localRepoHandle("/Users/x/dev/app") {
		t.Error("handle must be stable for the same path")
	}
	if a == localRepoHandle("/Users/x/work/app") {
		t.Error("same basename in a different parent must not collide")
	}
	if a == "" {
		t.Error("handle must not be empty")
	}
}

// An unknown handle must resolve to nothing — that refusal is what stops the server naming a
// directory this machine never offered. Roots are injected (a temp tree, never the real home):
// the original form of this test walked the CI runner's actual home directory and timed out the
// v0.38.0 release build after 10 minutes.
func TestResolveHandle(t *testing.T) {
	home := t.TempDir()
	mkRepo(t, filepath.Join(home, "dev", "app"))
	want := filepath.Join(home, "dev", "app")

	if got := resolveLocalRepoHandleIn(home, "", localRepoHandle(want)); got != want {
		t.Errorf("known handle resolved to %q, want %q", got, want)
	}
	if got := resolveLocalRepoHandleIn(home, "", "not-a-real-handle"); got != "" {
		t.Errorf("unknown handle resolved to %q, want empty", got)
	}
	if got := resolveLocalRepoHandleIn(home, "", ""); got != "" {
		t.Errorf("empty handle resolved to %q, want empty", got)
	}
	if got := resolveLocalRepoHandleIn("", "", localRepoHandle(want)); got != "" {
		t.Errorf("empty home resolved to %q, want empty", got)
	}
}

func TestDisplayParentIsHomeRelative(t *testing.T) {
	if got := displayParent("/Users/x/dev/app", "/Users/x"); got != "~/dev" {
		t.Errorf("displayParent = %q, want ~/dev", got)
	}
	if got := displayParent("/Users/x/app", "/Users/x"); got != "~" {
		t.Errorf("displayParent at home root = %q, want ~", got)
	}
	// Outside home: fall back to the bare basename rather than leaking a full path.
	if got := displayParent("/opt/stuff/app", "/Users/x"); got != "stuff" {
		t.Errorf("displayParent outside home = %q, want stuff", got)
	}
}

func TestSkipDir(t *testing.T) {
	for _, s := range []string{".git", ".cache", "node_modules", "Library"} {
		if !skipDir(s) {
			t.Errorf("skipDir(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"dev", "src", "work"} {
		if skipDir(s) {
			t.Errorf("skipDir(%q) = true, want false", s)
		}
	}
}
