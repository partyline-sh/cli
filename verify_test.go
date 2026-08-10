package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gitDiff must show the WHOLE task branch (from its fork point), not just the last commit. An
// auto-repaired task adds a commit per repair round, so HEAD^..HEAD shows only the final small fix and
// the real change in earlier commits goes invisible — that's how a retried run got FAILed on a 3-line
// lint tail while its actual fix sat in an earlier commit. The reviewer must see BOTH commits.
func TestGitDiffWholeBranchAcrossRepairCommits(t *testing.T) {
	work := seedCloneWithOrigin(t) // on main, a.txt committed, origin/main + origin/HEAD present
	tgit(t, work, "checkout", "-b", "crank-01-thing")
	if err := os.WriteFile(filepath.Join(work, "feature.txt"), []byte("the real implementation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tgit(t, work, "add", "-A")
	tgit(t, work, "commit", "-m", "crank: real work")
	if err := os.WriteFile(filepath.Join(work, "lint.txt"), []byte("// eslint-disable\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tgit(t, work, "add", "-A")
	tgit(t, work, "commit", "-m", "crank: fix review findings")

	// Explicit base: the whole-branch diff includes BOTH commits' files.
	d := gitDiff(work, "main")
	if !strings.Contains(d, "feature.txt") {
		t.Errorf("whole-branch diff must include the commit-1 file feature.txt (the real work); got:\n%s", d)
	}
	if !strings.Contains(d, "lint.txt") {
		t.Errorf("whole-branch diff must include the commit-2 file lint.txt; got:\n%s", d)
	}
	// Empty base (no per-project base configured): still reaches the fork point via origin/HEAD.
	if d2 := gitDiff(work, ""); !strings.Contains(d2, "feature.txt") {
		t.Errorf("empty-base diff must fall back to origin/HEAD and still include feature.txt; got:\n%s", d2)
	}
}

// When no base ref resolves (a bare local repo, no origin), gitDiff falls back to the last commit —
// unchanged from before, and no crash.
func TestGitDiffFallbackWithoutBase(t *testing.T) {
	dir := t.TempDir()
	tgit(t, dir, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tgit(t, dir, "add", "-A")
	tgit(t, dir, "commit", "-m", "one")
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tgit(t, dir, "add", "-A")
	tgit(t, dir, "commit", "-m", "two")
	if d := gitDiff(dir, ""); !strings.Contains(d, "b.txt") {
		t.Errorf("fallback should show the last commit (b.txt); got:\n%s", d)
	}
}

// readChecks parses one command per non-empty, non-comment line from the BASE repo's verify file,
// and returns nil when there's no file (→ gate skipped, not a pass).
func TestReadChecks(t *testing.T) {
	repo := t.TempDir()
	if got := readChecks(repo); got != nil {
		t.Fatalf("no verify file → nil, got %v", got)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".partyline"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "# acceptance checks\ngo build ./...\n\n  go test ./...  \n# trailing comment\n"
	if err := os.WriteFile(filepath.Join(repo, verifyFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := readChecks(repo)
	want := []string{"go build ./...", "go test ./..."}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("readChecks = %v, want %v", got, want)
	}
}

// runChecks: no checks → skipped (ran=false); all pass → ok; the FIRST failure stops the run and
// its output is captured in the reason.
func TestRunChecks(t *testing.T) {
	base, wt := t.TempDir(), t.TempDir()
	timeout := 10 * time.Second

	if vr := runChecks(base, wt, nil, timeout); vr.ran || vr.ok {
		t.Fatalf("no checks → {ran:false}, got %+v", vr)
	}

	if vr := runChecks(base, wt, []string{"true", "true"}, timeout); !vr.ran || !vr.ok {
		t.Fatalf("all pass → {ran:true, ok:true}, got %+v", vr)
	}

	// A REGRESSION (fails in the worktree, passes on base) must fail with the command + output in
	// the reason; a check AFTER the failure must NOT run (we stop at the first regression).
	if err := os.WriteFile(filepath.Join(base, "ok.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(wt, "second-ran")
	checks := []string{
		"echo boom-output; test -f ok.txt",
		"touch " + marker,
	}
	vr := runChecks(base, wt, checks, timeout)
	if vr.ok || !vr.ran {
		t.Fatalf("a regressing check → {ran:true, ok:false}, got %+v", vr)
	}
	if !strings.Contains(vr.reasons, "boom-output") || !strings.Contains(vr.reasons, "ok.txt") {
		t.Fatalf("reason must carry the failing cmd + its output, got %q", vr.reasons)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a check after the first regression must not run")
	}
}

// Baseline-aware: a check that fails in the worktree AND on the base is PRE-EXISTING debt — the
// gate must WARN and keep going, never quarantine (the run-dbf167b5 lesson: 37 pre-existing lint
// errors FAILed a clean diff). Later checks still run, and a real regression still fails.
func TestRunChecksBaselineAware(t *testing.T) {
	base, wt := t.TempDir(), t.TempDir()
	timeout := 10 * time.Second

	// Fails in BOTH dirs → pre-existing → ok:true with a warn; the next check still runs.
	marker := filepath.Join(wt, "after-preexisting-ran")
	vr := runChecks(base, wt, []string{"exit 1", "touch " + marker}, timeout)
	if !vr.ran || !vr.ok {
		t.Fatalf("pre-existing failure must not quarantine, got %+v", vr)
	}
	if !strings.Contains(vr.warn, "pre-existing") {
		t.Fatalf("pre-existing failure must be surfaced as a warn, got %q", vr.warn)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("checks after a pre-existing failure must still run")
	}

	// Mixed: one pre-existing + one real regression → still FAILS on the regression.
	if err := os.WriteFile(filepath.Join(base, "ok.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	vr = runChecks(base, wt, []string{"exit 1", "test -f ok.txt"}, timeout)
	if vr.ok {
		t.Fatalf("a real regression must still fail even after a pre-existing warn, got %+v", vr)
	}
}

// readReview: no file → gate off; file present → on, contents are the rubric.
func TestReadReview(t *testing.T) {
	repo := t.TempDir()
	if on, _ := readReview(repo); on {
		t.Fatal("no review file → gate off")
	}
	if err := os.MkdirAll(filepath.Join(repo, ".partyline"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, reviewFile), []byte("  check error handling  \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	on, rubric := readReview(repo)
	if !on || rubric != "check error handling" {
		t.Fatalf("readReview = (%v, %q), want (true, %q)", on, rubric, "check error handling")
	}
}

// #2a: a task carrying an acceptance-criteria block turns the reviewer ON on its own, so a repo with
// no .partyline/review still gets its definition-of-done verified. A plain task without criteria and
// no review file must NOT trigger the reviewer (the fast path is preserved).
func TestTaskHasAcceptanceCriteria(t *testing.T) {
	withCriteria := "Add a rate limit\n\nAcceptance criteria (definition of done):\n- [executable check] returns 429 over 100 req/min"
	if !taskHasAcceptanceCriteria(withCriteria) {
		t.Error("a task with a definition-of-done block must be detected")
	}
	if taskHasAcceptanceCriteria("Add a rate limit to the login endpoint") {
		t.Error("a plain task without criteria must NOT trigger the reviewer")
	}
}

// #2b: when the task states acceptance criteria, the reviewer prompt must instruct per-criterion
// verification (each unmet criterion is a FAIL) — that's what makes criteria a gate, not decoration.
func TestReviewerPromptEnforcesCriteria(t *testing.T) {
	withCriteria := "Do X\n\nAcceptance criteria (definition of done):\n- [behavior review] the banner dismisses"
	p := reviewerPrompt(withCriteria, "diff", "", "")
	if !strings.Contains(p, "ACCEPTANCE CRITERIA") || !strings.Contains(strings.ToUpper(p), "AUTOMATIC FAIL") {
		t.Error("prompt must instruct per-criterion verification with an automatic FAIL on any unmet one")
	}
	// A plain task must NOT get the criteria clause (keeps the prompt focused).
	if strings.Contains(reviewerPrompt("Do X", "diff", "", ""), "ACCEPTANCE CRITERIA") {
		t.Error("no criteria block → no per-criterion clause")
	}
}

// parseReviewVerdict: PASS passes; FAIL fails with the reason; and — critically for a trust gate —
// a reply with NO verdict is FAIL-CLOSED (never a silent pass).
func TestParseReviewVerdict(t *testing.T) {
	if pass, _ := parseReviewVerdict("looks good.\nVERDICT: PASS"); !pass {
		t.Error("VERDICT: PASS must pass")
	}
	pass, reasons := parseReviewVerdict("missing null check on the input.\nVERDICT: FAIL — no null check")
	if pass {
		t.Error("VERDICT: FAIL must not pass")
	}
	if !strings.Contains(reasons, "null check") {
		t.Errorf("fail reasons must carry the analysis, got %q", reasons)
	}
	if pass, r := parseReviewVerdict("I think it's probably fine, no verdict line here"); pass || !strings.Contains(r, "fail-closed") {
		t.Errorf("no verdict → fail-closed, got (pass=%v, %q)", pass, r)
	}
}

// quarantinedCount (Trust · T3) counts worker-successes whose verification failed — the tasks that
// route a finished run to needs_approval. A worker failure, a passing verify, and a no-checks task
// must NOT count.
func TestQuarantinedCount(t *testing.T) {
	results := []crankResult{
		{ok: true, verify: verifyResult{ran: true, ok: false}},  // quarantined ✓
		{ok: true, verify: verifyResult{ran: true, ok: true}},   // verified pass
		{ok: true, verify: verifyResult{ran: false}},            // no gate
		{ok: false, verify: verifyResult{ran: true, ok: false}}, // worker failed (not a quarantine)
		{ok: true, verify: verifyResult{ran: true, ok: false}},  // quarantined ✓
	}
	if got := quarantinedCount(results); got != 2 {
		t.Errorf("quarantinedCount = %d, want 2", got)
	}
}

// The reviewer must be handed the harness-verified baseline facts and the pre-existing carve-out —
// without them it fails clean work for repo debt (run dbf167b5: criterion "lint passes" was red on
// base; the reviewer FAILed an A-grade diff).
func TestReviewerPromptBaselineFacts(t *testing.T) {
	withCriteria := "Do X\n\nAcceptance criteria (definition of done):\n- [executable check] npm run lint passes"
	p := reviewerPrompt(withCriteria, "diff", "", "check fails on the BASE too — pre-existing: npm run lint")
	for _, want := range []string{"HARNESS-VERIFIED BASELINE FACTS", "pre-existing", "PRE-EXISTING on the base branch"} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt missing %q", want)
		}
	}
	// No baseline notes → no facts block (keeps the prompt focused), but the carve-out clause
	// still rides the criteria instruction.
	bare := reviewerPrompt(withCriteria, "diff", "", "")
	if strings.Contains(bare, "HARNESS-VERIFIED BASELINE FACTS") {
		t.Fatal("no notes → no facts block")
	}
	if !strings.Contains(bare, "PRE-EXISTING on the base branch") {
		t.Fatal("criteria clause must always carry the pre-existing carve-out")
	}
}
