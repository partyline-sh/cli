package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// readVisual: no file → gate off; file present → on, comments/blanks stripped, rest is the script.
func TestReadVisual(t *testing.T) {
	repo := t.TempDir()
	if on, _ := readVisual(repo); on {
		t.Fatal("no visual file → gate off")
	}
	if err := os.MkdirAll(filepath.Join(repo, ".partyline"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A file that is ONLY comments/blank has no script → still off (nothing to run).
	if err := os.WriteFile(filepath.Join(repo, visualFile), []byte("# just a comment\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if on, _ := readVisual(repo); on {
		t.Fatal("comment-only visual file → still off")
	}
	body := "# bring up the app and screenshot it\nnpm run build\nnode shot.js\n"
	if err := os.WriteFile(filepath.Join(repo, visualFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	on, script := readVisual(repo)
	if !on || script != "npm run build\nnode shot.js" {
		t.Fatalf("readVisual = (%v, %q), want (true, %q)", on, script, "npm run build\nnode shot.js")
	}
}

// touchesUI fires on unambiguously-visual sources and stays quiet for pure-logic/backend changes.
func TestTouchesUI(t *testing.T) {
	if !touchesUI([]string{"internal/foo.go", "web/src/app/board.tsx"}) {
		t.Error("a .tsx change must trigger the visual gate")
	}
	if !touchesUI([]string{"styles/main.CSS"}) {
		t.Error("extension match must be case-insensitive")
	}
	if touchesUI([]string{"internal/foo.go", "README.md", "verify.go"}) {
		t.Error("a non-UI change must not trigger the visual gate")
	}
	if touchesUI(nil) {
		t.Error("no files → no trigger")
	}
}

// collectShots returns PNG/JPG only, sorted; non-images are ignored; empty dir → nil.
func TestCollectShots(t *testing.T) {
	dir := t.TempDir()
	if got := collectShots(dir); got != nil {
		t.Fatalf("empty dir → nil, got %v", got)
	}
	for _, n := range []string{"b.png", "a.png", "shot.jpg", "notes.txt", "data.json"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := collectShots(dir)
	want := []string{filepath.Join(dir, "a.png"), filepath.Join(dir, "b.png"), filepath.Join(dir, "shot.jpg")}
	if len(got) != len(want) {
		t.Fatalf("collectShots = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("collectShots[%d] = %q, want %q (images only, sorted)", i, got[i], want[i])
		}
	}
}

// visualReviewerPrompt must carry the task, every screenshot path, and the exact VERDICT contract
// that parseReviewVerdict reads back.
func TestVisualReviewerPrompt(t *testing.T) {
	p := visualReviewerPrompt("make the board columns scroll", []string{"/tmp/x/one.png", "/tmp/x/two.png"}, "check dark mode")
	for _, want := range []string{"make the board columns scroll", "/tmp/x/one.png", "/tmp/x/two.png", "check dark mode", "VERDICT: PASS", "VERDICT: FAIL"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

// gitRepoWithUIChange builds a real repo whose HEAD commit touches a UI file, so changedFiles /
// runVisualReview exercise the actual `git diff HEAD^ HEAD` path.
func gitRepoWithUIChange(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	run := func(args ...string) {
		c := exec.Command("git", append([]string{"-C", repo}, args...)...)
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-qm", "seed")
	if err := os.WriteFile(filepath.Join(repo, "board.tsx"), []byte("export const Board = () => <div/>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-qm", "ui change")
	return repo
}

// changedFiles reads the HEAD commit's paths.
func TestChangedFiles(t *testing.T) {
	repo := gitRepoWithUIChange(t)
	got := changedFiles(repo)
	if len(got) != 1 || got[0] != "board.tsx" {
		t.Fatalf("changedFiles = %v, want [board.tsx]", got)
	}
}

// runVisualReview fail-closed paths that DON'T need the claude engine:
//   - gate off (no file) → ran:false
//   - enabled but diff touches no UI → ran:false (honest skip)
//   - enabled + UI change, render succeeds but drops no screenshot → quarantine (fail-closed)
//   - enabled + UI change, render script fails → quarantine (fail-closed)
func TestRunVisualReviewFailClosed(t *testing.T) {
	timeout := 10 * time.Second

	// Gate off.
	if vr := runVisualReview(t.TempDir(), t.TempDir(), "task", timeout, visualCfg{}); vr.ran {
		t.Fatalf("no visual file → gate off (ran:false), got %+v", vr)
	}

	writeVisual := func(repo, script string) {
		if err := os.MkdirAll(filepath.Join(repo, ".partyline"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, visualFile), []byte(script), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Enabled, but a non-UI-only diff → honest skip.
	noUI := t.TempDir()
	run := func(repo string, args ...string) {
		c := exec.Command("git", append([]string{"-C", repo}, args...)...)
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(noUI, "init", "-q")
	os.WriteFile(filepath.Join(noUI, "a.go"), []byte("package a\n"), 0o644)
	run(noUI, "add", "-A")
	run(noUI, "commit", "-qm", "seed")
	os.WriteFile(filepath.Join(noUI, "b.go"), []byte("package a\n\nvar X = 1\n"), 0o644)
	run(noUI, "add", "-A")
	run(noUI, "commit", "-qm", "logic")
	writeVisual(noUI, "echo should-not-render")
	if vr := runVisualReview(noUI, noUI, "task", timeout, visualCfg{}); vr.ran {
		t.Fatalf("no UI files changed → skip (ran:false), got %+v", vr)
	}

	// Enabled + UI change, but the render drops no screenshot → fail-closed quarantine.
	empty := gitRepoWithUIChange(t)
	writeVisual(empty, "true") // "renders" but writes nothing to $PARTYLINE_SHOTS_DIR
	vr := runVisualReview(empty, empty, "task", timeout, visualCfg{})
	if !vr.ran || vr.ok || !strings.Contains(vr.reasons, "no screenshot") {
		t.Fatalf("no screenshot → fail-closed quarantine, got %+v", vr)
	}

	// Enabled + UI change, render script fails → fail-closed quarantine (never reaches the reviewer).
	broken := gitRepoWithUIChange(t)
	writeVisual(broken, "echo boom-render >&2; exit 7")
	vr = runVisualReview(broken, broken, "task", timeout, visualCfg{})
	if !vr.ran || vr.ok || !strings.Contains(vr.reasons, "render script failed") {
		t.Fatalf("render failure → fail-closed quarantine, got %+v", vr)
	}
}

// The WEB-TOGGLE path (T2d): the gate is on for a project with NO repo `.partyline/visual` script.
// With no resolvable framework preset (no package.json / node_modules in the worktree), the gate must
// WARN and skip — never fail the run, never execute anything web-supplied.
func TestRunVisualReviewWebToggleNoRenderer(t *testing.T) {
	repo := gitRepoWithUIChange(t) // UI changed, so we'd render IF a renderer resolved
	vr := runVisualReview(repo, repo, "task", 10*time.Second, visualCfg{on: true, routes: []string{"/dashboard"}})
	if vr.ran || vr.ok {
		t.Fatalf("web toggle on, no renderer → skip without running (ran:false), got %+v", vr)
	}
	if !strings.Contains(vr.warn, "no renderer resolved") {
		t.Fatalf("web toggle on, no renderer → WARN, got warn=%q", vr.warn)
	}
}

// A web toggle that's OFF and no repo script → the gate is entirely off (ran:false, no warn), even
// when UI changed. The control-plane toggle is the only thing that turns it on absent a repo file.
func TestRunVisualReviewWebToggleOff(t *testing.T) {
	repo := gitRepoWithUIChange(t)
	vr := runVisualReview(repo, repo, "task", 10*time.Second, visualCfg{on: false})
	if vr.ran || vr.warn != "" {
		t.Fatalf("toggle off + no repo script → gate off silently, got %+v", vr)
	}
}
