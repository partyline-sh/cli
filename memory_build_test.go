package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func tinyRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Makefile", "all:\n\techo hi\ntest:\n\techo t\n")
	write("go.mod", "module example.com/x\n\ngo 1.22\n")
	write("main.go", "package main\n\nfunc main() {}\n")
	write(".github/workflows/ci.yml", "on: push\n")
	for _, c := range [][]string{{"init"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "t"}} {
		_ = exec.Command("git", append([]string{"-C", dir}, c...)...).Run()
	}
	return dir
}

// The rule the whole feature rests on: every derived fact cites where it came from. A fact with
// no evidence is a guess, and a seed full of confident guesses poisons every later session.
func TestEveryDerivedFactCitesItsEvidence(t *testing.T) {
	for _, d := range deriveFacts(tinyRepo(t)) {
		if len(d.Sources) == 0 {
			t.Errorf("derived %q carries no Sources, so nothing can check it: %s", d.Topic, d.Body)
		}
	}
}

// Anything requiring intent is recorded as a question, never asserted.
func TestWhatCannotBeDerivedBecomesAQuestion(t *testing.T) {
	if len(buildQuestions) == 0 {
		t.Fatal("no questions at all — build would silently claim to know everything")
	}
	for _, q := range buildQuestions {
		if q.Kind != "question" {
			t.Errorf("%q is kind %q; anything not derivable must be a question", q.Topic, q.Kind)
		}
	}
}

// Build must be invisible to facts a human wrote. This is the property that makes rebuild safe to
// run on a repo people have been using for a year.
func TestBuildNeverTouchesAHumanFact(t *testing.T) {
	repo := tinyRepo(t)
	human := fact{
		ID: newFactID(time.Now()), Kind: "constraint", By: "a person", At: time.Now(),
		Body: "A human recorded this and build must never rewrite or remove it.",
	}
	if _, err := writeFact(repo, human); err != nil {
		t.Fatal(err)
	}
	actions, err := planBuild(repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range actions {
		if a.Prev != nil && a.Prev.ID == human.ID {
			t.Fatalf("build proposed to %s a human's fact", a.Verb)
		}
	}
	// And after a real apply it is still there, untouched.
	for _, a := range actions {
		if a.Verb != "add" {
			continue
		}
		if _, err := writeFact(repo, fact{Kind: a.Kind, At: time.Now(), Body: a.Body, Tags: a.Tags, Sources: a.Src}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := findFact(repo, human.ID)
	if err != nil {
		t.Fatalf("the human's fact disappeared: %v", err)
	}
	if got.Body != human.Body {
		t.Error("the human's fact was rewritten")
	}
}

// Running build twice must not produce a second copy of every fact. Append-only duplication is
// what made the previous store untrustworthy; doing it automatically would be worse.
func TestRebuildIsIdempotent(t *testing.T) {
	repo := tinyRepo(t)
	apply := func() {
		actions, err := planBuild(repo)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range actions {
			switch a.Verb {
			case "add":
				if _, err := writeFact(repo, fact{Kind: a.Kind, At: time.Now(), Body: a.Body, Tags: a.Tags, Sources: a.Src}); err != nil {
					t.Fatal(err)
				}
			case "update":
				if _, err := rewriteFact(repo, a.Prev.ID, a.Body, "t"); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	apply()
	first, _ := readFacts(repo, false)
	apply()
	second, _ := readFacts(repo, false)
	if len(first) != len(second) {
		t.Fatalf("a second build changed the fact count %d → %d", len(first), len(second))
	}
	actions, _ := planBuild(repo)
	if len(actions) != 0 {
		t.Errorf("a third plan still proposes %d change(s); build is not converging", len(actions))
	}
}

// A changed repo updates the existing fact in place rather than filing a contradiction beside it.
func TestChangedRepoUpdatesInPlace(t *testing.T) {
	repo := tinyRepo(t)
	actions, _ := planBuild(repo)
	for _, a := range actions {
		if a.Verb == "add" {
			if _, err := writeFact(repo, fact{Kind: a.Kind, At: time.Now(), Body: a.Body, Tags: a.Tags, Sources: a.Src}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "Makefile"), []byte("all:\n\techo hi\ntest:\n\techo t\nlint:\n\techo l\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	actions, _ = planBuild(repo)
	var found bool
	for _, a := range actions {
		if a.Topic == "commands" {
			found = true
			if a.Verb != "update" {
				t.Errorf("a changed Makefile produced %q, not an update", a.Verb)
			}
			if !strings.Contains(a.Body, "lint") {
				t.Error("the update does not reflect the new target")
			}
		}
	}
	if !found {
		t.Error("a changed Makefile produced no action at all")
	}
}

// A derived fact whose evidence vanished is PROPOSED for removal, never removed silently: the
// file may be gone for a minute or gone for good, and only a human can tell the difference.
func TestStaleFactsAreProposedNotDeleted(t *testing.T) {
	repo := tinyRepo(t)
	actions, _ := planBuild(repo)
	for _, a := range actions {
		if a.Verb == "add" {
			if _, err := writeFact(repo, fact{Kind: a.Kind, At: time.Now(), Body: a.Body, Tags: a.Tags, Sources: a.Src}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.RemoveAll(filepath.Join(repo, ".github")); err != nil {
		t.Fatal(err)
	}
	actions, _ = planBuild(repo)
	var sawRemove bool
	for _, a := range actions {
		if a.Verb == "remove" && a.Topic == "ci" {
			sawRemove = true
			if a.Prev == nil {
				t.Error("a removal with no record of what would be removed")
			}
		}
	}
	if !sawRemove {
		t.Error("deleting the CI config produced no removal proposal")
	}
	// The fact is still on disk: planning proposes, it does not delete.
	facts, _ := readFacts(repo, false)
	for _, f := range facts {
		if topicOf(f) == "ci" {
			return
		}
	}
	t.Error("the stale fact was deleted during planning")
}

// Derived facts have to survive the scope gate, or build would emit a pile of refusals.
func TestDerivedFactsPassTheScopeGate(t *testing.T) {
	for _, d := range append(deriveFacts(tinyRepo(t)), buildQuestions...) {
		if v := screenFact(d.Kind, d.Body); v.Refuse {
			t.Errorf("build would emit a fact its own gate refuses (%s): %s\n  %s", d.Topic, v.Reason, d.Body)
		}
	}
}
