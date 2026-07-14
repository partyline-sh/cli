package gitwt

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestValidSkillName(t *testing.T) {
	cases := []struct {
		name string
		ok   bool
	}{
		{"pdf", true},
		{"seo-audit", true},
		{"a", true},
		{"a1b2-c3", true},
		{"0start", true},
		{"", false},
		{"-lead", false},          // must start alphanumeric
		{"UPPER", false},          // no uppercase
		{"has space", false},      // no whitespace
		{"has_underscore", false}, // underscore not allowed
		{"dot.name", false},       // no dots
		{"../etc/passwd", false},  // path traversal
		{"/abs/path", false},      // absolute path
		{"a/b", false},            // no slashes
		{"名前", false},             // no non-ascii
		{"a234567890123456789012345678901234567890", false}, // 40 chars > 39 cap
	}
	for _, c := range cases {
		if got := ValidSkillName(c.name); got != c.ok {
			t.Errorf("ValidSkillName(%q) = %v, want %v", c.name, got, c.ok)
		}
	}
}

// initRepoWorktree makes a real git repo + a linked worktree, so MaterializeSkills's git-exclude path
// (rev-parse --git-path info/exclude) exercises the actual per-worktree exclude file.
func initRepoWorktree(t *testing.T) (repo, wt string) {
	t.Helper()
	repo = t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-qm", "init")
	wt = filepath.Join(t.TempDir(), "wt")
	run("worktree", "add", "-q", wt)
	return repo, wt
}

func TestMaterializeSkills(t *testing.T) {
	_, wt := initRepoWorktree(t)
	skills := []Skill{
		{Name: "good-skill", Body: "---\nname: good-skill\n---\ndo the good thing\n"},
		{Name: "../evil", Body: "should never be written"},
		{Name: "UPPER", Body: "also rejected"},
		{Name: "another", Body: "second body"},
	}
	if err := MaterializeSkills(wt, skills); err != nil {
		t.Fatalf("MaterializeSkills: %v", err)
	}

	// Valid skills materialized under .agents/skills/<name>/SKILL.md.
	for _, name := range []string{"good-skill", "another"} {
		p := filepath.Join(wt, ".agents", "skills", name, "SKILL.md")
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s to exist: %v", p, err)
		}
	}
	body, err := os.ReadFile(filepath.Join(wt, ".agents", "skills", "good-skill", "SKILL.md"))
	if err != nil || string(body) != "---\nname: good-skill\n---\ndo the good thing\n" {
		t.Errorf("good-skill body wrong: %q err=%v", string(body), err)
	}

	// Claude sees it via .claude/skills/<name> (symlink → .agents copy).
	link := filepath.Join(wt, ".claude", "skills", "good-skill")
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("expected .claude/skills/good-skill: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		resolved, rerr := os.ReadFile(filepath.Join(link, "SKILL.md"))
		if rerr != nil || string(resolved) == "" {
			t.Errorf("symlink does not resolve to the skill body: %v", rerr)
		}
	}

	// Bad names skipped entirely — nothing escapes the skills tree.
	if _, err := os.Stat(filepath.Join(wt, ".agents", "skills", "UPPER")); !os.IsNotExist(err) {
		t.Errorf("invalid name 'UPPER' must NOT be materialized")
	}
	// Path traversal must not have written outside .agents/skills.
	if _, err := os.Stat(filepath.Join(filepath.Dir(wt), "evil")); !os.IsNotExist(err) {
		t.Errorf("path-traversal name '../evil' escaped the worktree")
	}

	// Injected dirs are git-excluded → `git status` in the worktree is clean (worker won't commit them).
	out, err := exec.Command("git", "-C", wt, "status", "--porcelain").CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v\n%s", err, out)
	}
	if len(out) != 0 {
		t.Errorf("worktree not clean — injected skills are not git-excluded:\n%s", out)
	}
}

func TestMaterializeSkillsNoop(t *testing.T) {
	dir := t.TempDir()
	if err := MaterializeSkills(dir, nil); err != nil {
		t.Fatalf("nil skills should be a no-op: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".agents")); !os.IsNotExist(err) {
		t.Errorf("no skills must create nothing")
	}
}
