package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"partyline.sh/partyline/internal/api"
)

// parseGradedReview must fail CLOSED — an unparseable, empty, or invalid-grade reply yields an error so
// the review run fails loudly (retryable) rather than recording a misleading grade (mirrors
// parseReviewVerdict in verify_test.go).
func TestParseGradedReview(t *testing.T) {
	cases := []struct {
		name      string
		reply     string
		wantErr   bool
		wantGrade string
		wantIssue int
	}{
		{
			name:      "fenced json",
			reply:     "here's my review:\n```json\n{\"grade\":\"B\",\"summary\":\"solid\",\"issues\":[{\"severity\":\"low\",\"text\":\"nit\"}]}\n```\nthanks",
			wantGrade: "B",
			wantIssue: 1,
		},
		{
			name:      "bare json, no fence",
			reply:     "{\"grade\":\"a\",\"summary\":\"great\",\"issues\":[]}",
			wantGrade: "A", // normalized to upper
		},
		{
			name:      "grade with surrounding prose and objects",
			reply:     "The task { was } to do X. My verdict:\n{\"grade\":\"F\",\"summary\":\"broken\",\"issues\":[{\"severity\":\"high\",\"text\":\"crashes\"},{\"severity\":\"med\",\"text\":\"no tests\"}]}",
			wantGrade: "F",
			wantIssue: 2,
		},
		{name: "no json at all", reply: "I think it looks pretty good overall, B+ maybe", wantErr: true},
		{name: "empty", reply: "", wantErr: true},
		{name: "invalid grade E", reply: "{\"grade\":\"E\",\"summary\":\"x\"}", wantErr: true},
		{name: "invalid grade word", reply: "{\"grade\":\"good\",\"summary\":\"x\"}", wantErr: true},
		{name: "malformed json", reply: "```json\n{\"grade\":\"A\", oops}\n```", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			grade, _, issues, err := parseGradedReview(c.reply)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got grade=%q", grade)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if grade != c.wantGrade {
				t.Errorf("grade = %q, want %q", grade, c.wantGrade)
			}
			if len(issues) != c.wantIssue {
				t.Errorf("issues = %d, want %d", len(issues), c.wantIssue)
			}
		})
	}
}

// The grader prompt must carry the three integrity layers: the acceptance-criteria checklist, the
// truthful working-tree description, and the anti-fabrication rule — the guard against a reviewer
// CLAIMING verification it never performed (which once graded a correct run F on an invented
// build-breaking-import finding).
func TestReviewGradePromptIntegrityLayers(t *testing.T) {
	crit := []api.ReviewCriterion{{Text: "the toggle persists across reloads", Verify: "grep for localStorage"}}
	tree := "YOUR WORKING TREE: a read-only checkout of branch `crank-x` at its tip"
	canon := []string{"[constraint] all timestamps are UTC", "[contract] the API returns 200 on success"}
	p := reviewGradePrompt("=== TASK 1 ===\ndiff", crit, canon, tree)
	for _, want := range []string{
		"ACCEPTANCE CRITERIA",
		"the toggle persists across reloads",
		"verify: grep for localStorage",
		"crank-x",
		"VERIFICATION HONESTY",
		"unverified",
		"PROJECT CANON",          // canon injected
		"all timestamps are UTC", // a canon line present
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt missing %q", want)
		}
	}
	// Canon is framed as check-criteria, NOT the author's context to adopt (independence guard).
	if !strings.Contains(p, "NOT the author's reasoning") {
		t.Fatal("canon must be framed as check-criteria, preserving reviewer independence")
	}
	// No criteria / no canon / no tree note → those sections are absent, honesty rule stays.
	p = reviewGradePrompt("d", nil, nil, "")
	if strings.Contains(p, "ACCEPTANCE CRITERIA") || strings.Contains(p, "PROJECT CANON") || !strings.Contains(p, "VERIFICATION HONESTY") {
		t.Fatal("sections must be conditional; the honesty rule is not")
	}
}

// reviewCheckout must hand back a detached worktree at the branch tip and clean up completely —
// and never touch the base repo's own checkout.
func TestReviewCheckout(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	if err := os.WriteFile(repo+"/a.txt", []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "crank-t-01-x")
	if err := os.WriteFile(repo+"/a.txt", []byte("branch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-q", "-m", "work")
	git("checkout", "-q", "main") // base repo sits on main — the grader must still see the BRANCH

	wt, cleanup := reviewCheckout(repo, "crank-t-01-x")
	if wt == "" {
		t.Fatal("checkout failed")
	}
	b, err := os.ReadFile(wt + "/a.txt")
	if err != nil || string(b) != "branch\n" {
		t.Fatalf("worktree must show the BRANCH content, got %q err %v", b, err)
	}
	cleanup()
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatal("cleanup must remove the worktree dir")
	}
	// Flag-shaped and missing branches fail open (no dir, no cleanup).
	if wt, cl := reviewCheckout(repo, "-evil"); wt != "" || cl != nil {
		t.Fatal("flag-shaped branch must be refused")
	}
	if wt, cl := reviewCheckout(repo, "no-such-branch"); wt != "" || cl != nil {
		t.Fatal("missing branch must fail open")
	}
}
