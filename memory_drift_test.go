package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// driftRepo is a repo with a committed overview and a committed gotcha citing internal/importer.go.
func driftRepo(t *testing.T) (repo string, commit func(file, msg string)) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	repo = initRepo(t)
	git := func(a ...string) {
		if out, err := exec.Command("git", append([]string{"-C", repo}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", a, err, out)
		}
	}
	n := 0
	commit = func(file, msg string) {
		n++
		p := filepath.Join(repo, file)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(fmt.Sprintf("package x // %d\n", n)), 0o644); err != nil {
			t.Fatal(err)
		}
		git("add", "-A")
		git("commit", "-q", "-m", msg)
	}
	commit("internal/importer.go", "init")
	for _, f := range []fact{
		{Kind: "overview", By: "T", At: time.Now(), Body: "The importer reads CSV exports from the POS and writes them to Postgres."},
		{Kind: "gotcha", By: "T", At: time.Now(), Body: "internal/importer.go rejects CSV files with a byte-order mark."},
		{Kind: "decision", By: "T", At: time.Now(), Body: "Postgres, not SQLite: two services write the same tables."},
	} {
		if _, err := writeFact(repo, f); err != nil {
			t.Fatal(err)
		}
	}
	git("add", "-A")
	git("commit", "-q", "-m", "memory")
	return repo, commit
}

// Twenty code commits past the overview is the point to tell the user. Commits that only update
// memory are not the code moving on.
func TestDriftCountsCodeCommitsOnly(t *testing.T) {
	repo, commit := driftRepo(t)
	for i := 0; i < memoryDriftThreshold-1; i++ {
		commit(fmt.Sprintf("cmd/c%d.go", i), "code")
	}
	commit("memory/notes.txt", "memory only")
	commit("AGENTS.md", "map only")
	if _, n, ok := memoryDrift(repo); !ok || n != memoryDriftThreshold-1 {
		t.Fatalf("drift = %d, %v; want %d", n, ok, memoryDriftThreshold-1)
	}
	if memoryDriftNotice(repo) != "" {
		t.Error("notice before the threshold")
	}
	commit("cmd/last.go", "code")
	msg := memoryDriftNotice(repo)
	if !strings.Contains(msg, "20 code\ncommits ago") || !strings.Contains(msg, "--refresh") {
		t.Errorf("notice at the threshold:\n%s", msg)
	}
	if memoryDriftNotice(repo) != "" {
		t.Error("notice repeated with no new commits; a user who said no would be asked every session")
	}
	for i := 0; i < memoryDriftThreshold; i++ {
		commit(fmt.Sprintf("cmd/d%d.go", i), "code")
	}
	if memoryDriftNotice(repo) == "" {
		t.Error("no notice after another threshold of commits")
	}
}

// Rewriting the overview and committing it is what resets the count; until it is committed there
// is nothing to measure from, so no notice.
func TestRefreshedOverviewResetsDrift(t *testing.T) {
	repo, commit := driftRepo(t)
	for i := 0; i < 3; i++ {
		commit(fmt.Sprintf("cmd/c%d.go", i), "code")
	}
	ov, _ := currentOverview(repo)
	if _, err := rewriteFact(repo, ov.ID, ov.Body+" It runs nightly.", "T"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := memoryDrift(repo); ok {
		t.Error("an uncommitted overview was measured")
	}
	commit("cmd/c9.go", "code and the refreshed overview together")
	if _, n, ok := memoryDrift(repo); !ok || n != 0 {
		t.Errorf("drift after refresh = %d, %v; want 0", n, ok)
	}
}

// A fact naming a file is flagged when that file changes; one naming none never is. Verifying it
// clears the flag until the file changes again.
func TestChangedEvidenceIsFlaggedUntilVerified(t *testing.T) {
	repo, commit := driftRepo(t)
	facts, _ := readFacts(repo, false)
	if got := factsWithChangedEvidence(repo, facts, time.Minute); len(got) != 0 {
		t.Fatalf("flagged before anything changed: %+v", got)
	}
	commit("internal/importer.go", "change the importer")
	commit("cmd/other.go", "unrelated")
	got := factsWithChangedEvidence(repo, facts, time.Minute)
	if len(got) != 1 || got[0].Fact.Kind != "gotcha" || strings.Join(got[0].Paths, ",") != "internal/importer.go" {
		t.Fatalf("changed evidence = %+v", got)
	}
	f := got[0].Fact
	f.Verified = headCommit(repo)
	if _, err := writeFact(repo, f); err != nil {
		t.Fatal(err)
	}
	facts, _ = readFacts(repo, false)
	if got := factsWithChangedEvidence(repo, facts, time.Minute); len(got) != 0 {
		t.Errorf("flagged while the verify is uncommitted: %+v", got)
	}
	commit("cmd/after.go", "commit the verify")
	if got := factsWithChangedEvidence(repo, facts, time.Minute); len(got) != 0 {
		t.Errorf("still flagged after verify was committed: %+v", got)
	}
}

// A session records a fact, then commits it with the code it describes. That fact is current, and
// so is one about a file the session created. The first design stamped HEAD at write time and
// flagged the first on commit while never checking the second.
func TestFactCommittedWithItsCodeIsCurrent(t *testing.T) {
	repo, commit := driftRepo(t)
	for _, body := range []string{
		"internal/importer.go strips a byte-order mark before parsing since the POS export change.",
		"internal/bom.go holds the byte-order-mark handling shared by both importers.",
	} {
		if _, err := writeFact(repo, fact{Kind: "gotcha", By: "T", At: time.Now(), Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "internal", "bom.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commit("internal/importer.go", "strip BOMs, with the facts")
	facts, _ := readFacts(repo, false)
	for _, c := range factsWithChangedEvidence(repo, facts, time.Minute) {
		if strings.Contains(c.Fact.Body, "since the POS") || strings.Contains(c.Fact.Body, "bom.go") {
			t.Errorf("fact committed with its code was flagged: %s", c.Fact.Body)
		}
	}
	commit("internal/bom.go", "later change")
	var flagged bool
	for _, c := range factsWithChangedEvidence(repo, facts, time.Minute) {
		flagged = flagged || strings.Contains(c.Fact.Body, "bom.go holds")
	}
	if !flagged {
		t.Error("a later change to a file the fact names was not flagged")
	}
}

// AGENTS.md and memory/ change on every memory update; naming them is not evidence.
func TestMapFilesAreNotEvidence(t *testing.T) {
	repo, commit := driftRepo(t)
	if _, err := writeFact(repo, fact{Kind: "constraint", By: "T", At: time.Now(), Body: "AGENTS.md must stay under 200 lines or Codex truncates it."}); err != nil {
		t.Fatal(err)
	}
	commit("cmd/x.go", "fact")
	commit("AGENTS.md", "map regenerated")
	facts, _ := readFacts(repo, false)
	if got := factsWithChangedEvidence(repo, facts, time.Minute); len(got) != 0 {
		t.Errorf("flagged for a map change: %+v", got)
	}
}
