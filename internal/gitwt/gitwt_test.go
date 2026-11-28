package gitwt

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// mkrepo builds a throwaway git repo with one commit and returns its root.
func mkrepo(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "init")
	return root
}

func TestSlugAndPath(t *testing.T) {
	if got := Slug("Fix payments! (v2)"); got != "Fix-payments-v2" {
		t.Fatalf("slug: %q", got)
	}
	// Fresh worktrees land inside the single container dir, not as loose repo siblings.
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	want := filepath.Join(parent, WorktreeContainer, "repo--feat-pay")
	if got := Path(repo, "feat/pay"); got != want {
		t.Fatalf("path: %q, want %q", got, want)
	}
	// LEGACY: a worktree that already exists as a direct sibling (pre-container) keeps
	// resolving to its real location, so resume/removal of in-flight work survives the upgrade.
	legacy := filepath.Join(parent, "repo--feat-pay")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Path(repo, "feat/pay"); got != legacy {
		t.Fatalf("legacy path: %q, want %q", got, legacy)
	}
	// And once a containered one exists too, the container wins.
	if err := os.MkdirAll(want, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Path(repo, "feat/pay"); got != want {
		t.Fatalf("container precedence: %q, want %q", got, want)
	}
}

func TestCreateGuardRemove(t *testing.T) {
	repo := mkrepo(t)

	wt, branch, err := Create(repo, "pay fix")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if branch != "pay-fix" {
		t.Fatalf("branch: %q", branch)
	}
	if _, err := os.Stat(filepath.Join(wt, "tracked.txt")); err != nil {
		t.Fatalf("worktree missing checkout: %v", err)
	}

	// The guard: the worktree is linked; the repo itself is NOT.
	if !IsLinkedWorktree(wt) {
		t.Fatal("worktree should be linked")
	}
	if IsLinkedWorktree(repo) {
		t.Fatal("repo root must NEVER register as a linked worktree")
	}

	// Same branch again → reuse, not error.
	wt2, _, err := Create(repo, "pay-fix")
	if err != nil || wt2 != wt {
		t.Fatalf("reuse: %v %q", err, wt2)
	}

	// List sees exactly the linked worktree. Compare resolved paths — git reports
	// /private/var/… where t.TempDir hands out /var/… on macOS.
	ls, err := List(repo)
	wtReal, _ := filepath.EvalSymlinks(wt)
	if err != nil || len(ls) != 1 || ls[0][1] != "pay-fix" {
		t.Fatalf("list: %v %v", ls, err)
	}
	if lsReal, _ := filepath.EvalSymlinks(ls[0][0]); lsReal != wtReal {
		t.Fatalf("list path: %q != %q", lsReal, wtReal)
	}

	// Removal refuses the repo, allows the worktree.
	if err := Remove(repo, repo); err == nil {
		t.Fatal("Remove(repo,repo) must refuse")
	}
	if err := Remove(repo, wt); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatal("worktree dir should be gone")
	}
	// And a random non-worktree dir is refused (linked gate).
	plain := t.TempDir()
	if err := Remove(repo, plain); err == nil {
		t.Fatal("Remove of a non-worktree must refuse")
	}
}

func TestSeedInclude(t *testing.T) {
	repo := mkrepo(t)
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".gitignore", ".env\n.mcp.json\nsecrets/\nnot-listed.key\n")
	write(".worktreeinclude", "# seed agent config\n.env\n.mcp.json\nsecrets/\ntracked.txt\n")
	write(".env", "KEY=v\n")
	write(".mcp.json", "{}\n")
	write("secrets/a.pem", "pem\n")
	write("not-listed.key", "nope\n") // ignored but NOT listed → must not copy

	wt, _, err := Create(repo, "seed-test")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := SeedInclude(repo, wt); err != nil {
		t.Fatalf("seed: %v", err)
	}
	for _, want := range []string{".env", ".mcp.json", "secrets/a.pem"} {
		if _, err := os.Stat(filepath.Join(wt, want)); err != nil {
			t.Fatalf("expected %s seeded: %v", want, err)
		}
	}
	if _, err := os.Stat(filepath.Join(wt, "not-listed.key")); !os.IsNotExist(err) {
		t.Fatal("ignored-but-unlisted file must not be copied")
	}
	// tracked.txt is listed but TRACKED → not ignored → the copy path must not touch it
	// (it's already there via checkout, from git, not from us).
	b, _ := os.ReadFile(filepath.Join(wt, "tracked.txt"))
	if strings.TrimSpace(string(b)) != "hi" {
		t.Fatal("tracked file corrupted")
	}
}

func TestMaterializeWIP(t *testing.T) {
	repo := mkrepo(t)
	// Uncommitted work in the source: a tracked-file edit + a brand-new file.
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "fresh.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wt, _, err := Create(repo, "fork")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := MaterializeWIP(repo, wt); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(wt, "tracked.txt")); strings.TrimSpace(string(b)) != "edited" {
		t.Fatalf("tracked edit didn't travel: %q", b)
	}
	if _, err := os.Stat(filepath.Join(wt, "fresh.go")); err != nil {
		t.Fatal("untracked file didn't travel")
	}
	// Mid-merge source must refuse.
	mh := filepath.Join(repo, ".git", "MERGE_HEAD")
	if err := os.WriteFile(mh, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := MaterializeWIP(repo, wt); err == nil {
		t.Fatal("mid-merge materialize must refuse")
	}
}
