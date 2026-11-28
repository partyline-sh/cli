package main

import (
	"os"
	"path/filepath"
	"testing"
)

// worktreeParent is the load-bearing bit of the nesting: get it wrong and either real
// projects vanish under a wrong parent, or every worktree stays a top-level sibling. Each
// case below is a shape that actually occurs on disk.
func TestWorktreeParent(t *testing.T) {
	// A real linked worktree: .git is a FILE containing the gitdir pointer.
	t.Run("linked worktree", func(t *testing.T) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, ".git"), "gitdir: /Users/x/dev/partyline/.git/worktrees/crank-01\n")
		got, ok := worktreeParent(dir)
		if !ok || got != "/Users/x/dev/partyline" {
			t.Fatalf("got (%q,%v), want (/Users/x/dev/partyline,true)", got, ok)
		}
	})

	// A relative pointer (git worktree --relative-paths) resolves against the worktree dir.
	t.Run("relative gitdir", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, "wt", "task-1")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(dir, ".git"), "gitdir: ../../repo/.git/worktrees/task-1")
		got, ok := worktreeParent(dir)
		if want := filepath.Join(root, "repo"); !ok || got != want {
			t.Fatalf("got (%q,%v), want (%q,true)", got, ok, want)
		}
	})

	// Everything below must report "not a worktree" — the caller then keeps today's
	// behaviour (the dir is its own project).
	t.Run("not a worktree", func(t *testing.T) {
		gitDirRepo := t.TempDir() // .git is a DIRECTORY → an ordinary repo
		if err := os.Mkdir(filepath.Join(gitDirRepo, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		noGit := t.TempDir() // no .git at all

		malformed := t.TempDir() // .git file that isn't a gitdir pointer
		write(t, filepath.Join(malformed, ".git"), "ref: refs/heads/main\n")

		empty := t.TempDir() // "gitdir:" with no path
		write(t, filepath.Join(empty, ".git"), "gitdir:   \n")

		noSeg := t.TempDir() // a gitdir with no /.git/worktrees/ segment (e.g. a submodule)
		write(t, filepath.Join(noSeg, ".git"), "gitdir: /Users/x/dev/repo/.git/modules/sub\n")

		noName := t.TempDir() // segment present but no worktree name after it
		write(t, filepath.Join(noName, ".git"), "gitdir: /Users/x/dev/repo/.git/worktrees/\n")

		noParent := t.TempDir() // nothing before the segment
		write(t, filepath.Join(noParent, ".git"), "gitdir: /.git/worktrees/w\n")

		for name, dir := range map[string]string{
			".git is a directory":  gitDirRepo,
			"no .git":              noGit,
			"missing dir":          filepath.Join(t.TempDir(), "gone"),
			"malformed contents":   malformed,
			"empty gitdir":         empty,
			"no worktrees segment": noSeg,
			"no worktree name":     noName,
			"no parent prefix":     noParent,
			"empty path":           "",
		} {
			if got, ok := worktreeParent(dir); ok {
				t.Errorf("%s: got (%q,true), want not-a-worktree", name, got)
			}
		}
	})
}

// attributeProjects is what the switchboard reads: worktree sessions carry the PARENT as
// their grouping key, a deleted worktree is remembered (via the index) and flagged dead,
// and an ordinary dir is untouched.
func TestAttributeProjects(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // the worktree index lives in stateDir()
	repo := t.TempDir()
	wt := filepath.Join(t.TempDir(), "partyline--crank-01")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(wt, ".git"), "gitdir: "+filepath.Join(repo, ".git", "worktrees", "crank-01")+"\n")
	plain := t.TempDir()

	ss := []aiSession{{ID: "a", Cwd: wt}, {ID: "b", Cwd: plain}, {ID: "c"}}
	attributeProjects(ss)
	if ss[0].ProjDir != repo || ss[0].WtName != filepath.Base(wt) || ss[0].DeadWt {
		t.Fatalf("worktree session: %+v (want proj=%s wt=%s dead=false)", ss[0], repo, filepath.Base(wt))
	}
	if ss[0].projKey() != repo {
		t.Fatalf("projKey = %q, want the parent %q", ss[0].projKey(), repo)
	}
	if ss[1].ProjDir != plain || ss[1].WtName != "" || ss[1].DeadWt {
		t.Fatalf("plain session: %+v (want proj=%s, not a worktree)", ss[1], plain)
	}
	if ss[2].projKey() != "" { // no cwd recorded → no project
		t.Fatalf("cwd-less session projKey = %q, want empty", ss[2].projKey())
	}

	// Delete the worktree: the index remembers it, so the session is still grouped under
	// the parent but flagged dead (hidden by default, prunable).
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	ss2 := []aiSession{{ID: "a", Cwd: wt}}
	attributeProjects(ss2)
	if ss2[0].ProjDir != repo || !ss2[0].DeadWt {
		t.Fatalf("dead worktree session: %+v (want proj=%s dead=true)", ss2[0], repo)
	}

	// A gone dir we never saw as a worktree stays its own project — the recover modal's
	// case, which we must not steal.
	ss3 := []aiSession{{ID: "z", Cwd: filepath.Join(t.TempDir(), "never-seen")}}
	attributeProjects(ss3)
	if ss3[0].DeadWt || ss3[0].ProjDir != ss3[0].Cwd {
		t.Fatalf("unknown gone dir: %+v (want its own project, dead=false)", ss3[0])
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
