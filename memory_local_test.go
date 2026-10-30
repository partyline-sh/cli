package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, a := range [][]string{
		{"init", "-q"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "T"},
	} {
		cmd := exec.Command("git", a...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", a, err, out)
		}
	}
	return dir
}

// Facts land in the repo being worked on, not in a partyline-managed side repo. This is the whole
// point: checking out the code checks out what the team learned about it.
func TestFactsLandInsideTheRepo(t *testing.T) {
	repo := initRepo(t)
	root, ok := repoMemory(repo)
	if !ok {
		t.Fatal("a git repo should resolve a repo-local memory store")
	}
	// os.MkdirTemp can hand back /var vs /private/var on macOS; compare resolved paths.
	gotR, _ := filepath.EvalSymlinks(root)
	wantR, _ := filepath.EvalSymlinks(repo)
	if gotR != wantR {
		t.Errorf("workspace should be the repo root: got %q want %q", gotR, wantR)
	}
	if hasRepoMemory(repo) {
		t.Error("a fresh repo should have no facts yet")
	}

	p, err := writeFact(root, fact{
		Kind: "constraint", By: "T", At: time.Now(),
		Body: "There is no SQL database on a store server; the POS stores data in ISAM files.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p, repo) && !strings.HasPrefix(gotR, filepath.Dir(filepath.Dir(p))) {
		t.Errorf("fact written outside the repo: %s", p)
	}
	if filepath.Base(filepath.Dir(p)) != "memory" {
		t.Errorf("fact should be under memory/: %s", p)
	}
	if !hasRepoMemory(repo) {
		t.Error("the repo should now report facts")
	}
}

// A non-repo directory has nowhere repo-local to put facts, and must say so rather than inventing
// a location.
func TestOutsideAGitRepoThereIsNoRepoLocalStore(t *testing.T) {
	if _, ok := repoMemory(t.TempDir()); ok {
		t.Error("a plain directory should not resolve a repo-local store")
	}
}

// PARTYLINE MUST NEVER COMMIT THE USER'S REPO. A fact is written and left in the working tree.
// Committing it would risk publishing whatever else the person had staged.
func TestSyncRefusesToTouchAUserRepo(t *testing.T) {
	repo := initRepo(t)
	if !isRepoLocalMemory(repo) {
		t.Fatal("a user repo must be classified as repo-local")
	}
	if _, err := writeFact(repo, fact{
		Kind: "gotcha", By: "T", At: time.Now(),
		Body: "Xero's /Reports/TrialBalance ignores the date parameter and returns activity to date.",
	}); err != nil {
		t.Fatal(err)
	}
	if err := memorySync(repo, "memory: should not happen"); err != nil {
		t.Fatalf("sync on a user repo should be a silent no-op, got %v", err)
	}
	// No commit may exist.
	cmd := exec.Command("git", "rev-list", "--all", "--count")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.TrimSpace(string(out)); n != "0" {
		t.Errorf("partyline committed the user's repo (%s commits)", n)
	}
	// And the fact must be sitting there untracked, which is what the CLI promises.
	cmd = exec.Command("git", "status", "--porcelain")
	cmd.Dir = repo
	st, _ := cmd.Output()
	if !strings.Contains(string(st), "memory/") {
		t.Errorf("the fact should be an uncommitted change:\n%s", st)
	}
}

// A partyline-managed clone is still ours to sync; the guard must not disable that.
func TestAPartylineMemoryCloneIsStillOurs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := filepath.Join(projectsRoot(), "acr")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if isRepoLocalMemory(dir) {
		t.Error("a clone under ~/.partyline/projects must not be treated as the user's repo")
	}
}

// Facts are mutable: a wrong claim is edited out, not annotated. This is the live defect from the
// server — #30 said "dead-letter queue", #141 said "Rejected list, not the dead-letter queue", and
// both were briefed at once.
func TestAFactCanBeRewrittenInPlace(t *testing.T) {
	repo := initRepo(t)
	if _, err := writeFact(repo, fact{
		ID: "f1", Kind: "constraint", By: "T", At: time.Now(),
		Body: "With schema enforcement on, POS events that fail validation go to the dead-letter queue.",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := rewriteFact(repo, "f1", "With schema enforcement on, events that fail validation are quarantined into the Rejected list, not the dead-letter queue.", "T")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "f1" {
		t.Errorf("the id must survive an edit, got %q", got.ID)
	}
	if got.Updated.IsZero() {
		t.Error("an edit should record updated")
	}
	facts, _ := readFacts(repo, true)
	if len(facts) != 1 {
		t.Fatalf("a rewrite must not create a second fact: got %d", len(facts))
	}
	if strings.Contains(facts[0].Body, "go to the dead-letter queue") {
		t.Error("the wrong claim survived the rewrite")
	}
	if !strings.Contains(facts[0].Body, "Rejected list") {
		t.Error("the correction is not in the fact")
	}
}

// A live session appends what it learned without restating what was already right.
func TestAFactCanBeAppendedTo(t *testing.T) {
	repo := initRepo(t)
	if _, err := writeFact(repo, fact{
		ID: "f2", Kind: "contract", By: "T", At: time.Now(),
		Body: "The 64-bit event stream uses one schema wrapped in CloudEvents 1.0 JSON.",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := appendToFact(repo, "f2", "Events are retained and replayable for 90 days, routed by event type.", "T"); err != nil {
		t.Fatal(err)
	}
	facts, _ := readFacts(repo, true)
	if len(facts) != 1 {
		t.Fatalf("append must not create a fact: got %d", len(facts))
	}
	for _, want := range []string{"CloudEvents", "90 days"} {
		if !strings.Contains(facts[0].Body, want) {
			t.Errorf("appended fact lost %q:\n%s", want, facts[0].Body)
		}
	}
}

// An id prefix is enough, so a human never types a full timestamped id.
func TestAnIDPrefixResolves(t *testing.T) {
	repo := initRepo(t)
	if _, err := writeFact(repo, fact{
		ID: "20260918-031936-f24b87", Kind: "decision", By: "T", At: time.Now(),
		Body: "Fleet manager and the integration platform are one project, so facts about either reach both teams.",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := findFact(repo, "20260918"); err != nil {
		t.Errorf("a unique prefix should resolve: %v", err)
	}
	if _, err := findFact(repo, "nope"); err == nil {
		t.Error("an unknown id should error")
	}
}

// The gate runs on every write path, because they all funnel through writeFact.
func TestTheGateCannotBeBypassedByTheStore(t *testing.T) {
	repo := initRepo(t)
	_, err := writeFact(repo, fact{
		Kind: "constraint", By: "T", At: time.Now(),
		Body: "The Mac mini's 228 GB disk keeps filling up, and the pending macOS update needs 25 GB free.",
	})
	if err == nil {
		t.Fatal("writeFact accepted a transient host fact")
	}
	if !strings.Contains(err.Error(), "three months") {
		t.Errorf("the refusal should state the test: %v", err)
	}
}

// Antigravity reads AGENTS.md; returning "" treated it as having no memory file at all.
func TestAntigravityHasAMemoryFile(t *testing.T) {
	if got := memoryFileName("antigravity"); got == "" {
		t.Error("antigravity reports no memory file, so facts never reach it")
	}
	for _, tool := range []string{"claude", "codex", "gemini", "antigravity"} {
		if memoryFileName(tool) == "" {
			t.Errorf("%s has no memory file", tool)
		}
	}
}
