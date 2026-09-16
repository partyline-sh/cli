package gitwt

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"partyline.sh/partyline/internal/skillzip"
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

// A PACKAGE skill materializes its WHOLE tree (SKILL.md + scripts/assets), preserves the script's exec
// bit, stays git-excluded, and — on a bundle unpack failure — falls back to writing the body-only
// SKILL.md so the skill still injects.
func TestMaterializeSkillsBundle(t *testing.T) {
	_, wt := initRepoWorktree(t)

	// Build a real bundle for "packaged" via the shared bundler, from a temp source dir.
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte("---\nname: packaged\n---\nbundled body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "scripts", "run.sh"), []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "reference.md"), []byte("notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	zip, err := skillzip.Bundle(src)
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}

	skills := []Skill{
		{Name: "packaged", Body: "IGNORED when the bundle unpacks", Bundle: zip},
		{Name: "corrupt", Body: "fallback body wins", Bundle: []byte("not a zip")},
	}
	if err := MaterializeSkills(wt, skills); err != nil {
		t.Fatalf("MaterializeSkills: %v", err)
	}

	// Whole tree landed: SKILL.md, the script, and the reference file.
	base := filepath.Join(wt, ".agents", "skills", "packaged")
	if b, err := os.ReadFile(filepath.Join(base, "SKILL.md")); err != nil || string(b) != "---\nname: packaged\n---\nbundled body\n" {
		t.Errorf("packaged SKILL.md wrong: %q err=%v", string(b), err)
	}
	if _, err := os.Stat(filepath.Join(base, "reference.md")); err != nil {
		t.Errorf("bundle reference.md not materialized: %v", err)
	}
	sh, err := os.Stat(filepath.Join(base, "scripts", "run.sh"))
	if err != nil {
		t.Fatalf("bundle scripts/run.sh not materialized: %v", err)
	}
	if sh.Mode().Perm()&0o111 == 0 {
		t.Errorf("exec bit lost on the materialized script: %v", sh.Mode())
	}

	// A corrupt bundle falls back to the body-only SKILL.md (skill still injects, not dropped).
	if b, err := os.ReadFile(filepath.Join(wt, ".agents", "skills", "corrupt", "SKILL.md")); err != nil || string(b) != "fallback body wins" {
		t.Errorf("corrupt-bundle fallback wrong: %q err=%v", string(b), err)
	}

	// Everything git-excluded — worktree stays clean.
	out, err := exec.Command("git", "-C", wt, "status", "--porcelain").CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v\n%s", err, out)
	}
	if len(out) != 0 {
		t.Errorf("worktree not clean — materialized bundle not git-excluded:\n%s", out)
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
