package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func tmpWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := saveProjectFile(dir, projectFile{Label: "acr"}); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A fact is a file, and what was written must come back — including the front matter a human or
// a teammate's agent will read in a pull request.
func TestWriteThenReadFact(t *testing.T) {
	dir := tmpWorkspace(t)
	at := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	path, err := writeFact(dir, fact{Kind: "decision", Repo: "fleet-manager", By: "Darcy", At: at,
		Tags: []string{"transport"}, Body: "gRPC, not REST — the links are flaky"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, ".md") {
		t.Errorf("facts are markdown files, got %s", path)
	}
	got, err := readFacts(dir, false)
	if err != nil || len(got) != 1 {
		t.Fatalf("readFacts = %d facts, %v", len(got), err)
	}
	f := got[0]
	if f.Kind != "decision" || f.Repo != "fleet-manager" || f.By != "Darcy" || !f.At.Equal(at) {
		t.Errorf("front matter lost: %+v", f)
	}
	if len(f.Tags) != 1 || f.Tags[0] != "transport" || !strings.Contains(f.Body, "flaky") {
		t.Errorf("tags or body lost: %+v", f)
	}
}

// Only the closed set of kinds, and never an empty body: without both, "memory" becomes a diary
// and the brief becomes noise.
func TestWriteFactRefusesJunk(t *testing.T) {
	dir := tmpWorkspace(t)
	if _, err := writeFact(dir, fact{Kind: "musing", Body: "hmm", At: time.Now()}); err == nil {
		t.Error("an unknown kind was accepted")
	}
	if _, err := writeFact(dir, fact{Kind: "decision", Body: "   ", At: time.Now()}); err == nil {
		t.Error("an empty fact was accepted")
	}
}

// Supersession is what keeps memory trustworthy: the replaced belief stops being briefed but
// stays on disk, so the history is auditable.
func TestSupersededFactsLeaveTheBrief(t *testing.T) {
	dir := tmpWorkspace(t)
	old, _ := writeFact(dir, fact{Kind: "decision", By: "Darcy", At: time.Now().Add(-time.Hour), Body: "we use gRPC"})
	oldID := strings.TrimSuffix(filepath.Base(old), ".md")
	if _, err := writeFact(dir, fact{Kind: "decision", By: "Matt", At: time.Now(), Supersedes: oldID, Body: "we use REST now"}); err != nil {
		t.Fatal(err)
	}
	current, _ := readFacts(dir, false)
	if len(current) != 1 || !strings.Contains(current[0].Body, "REST") {
		t.Fatalf("the superseded fact is still current: %+v", current)
	}
	all, _ := readFacts(dir, true)
	if len(all) != 2 {
		t.Errorf("history lost the old fact: %d", len(all))
	}
}

// Two agents writing at the same moment must never collide on a file — that is the entire reason
// a fact is one file instead of a line in a list.
func TestFactIDsDoNotCollide(t *testing.T) {
	now := time.Now()
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		id := newFactID(now) // same instant, as two machines would
		if seen[id] {
			t.Fatalf("two facts minted the same id at one instant: %s", id)
		}
		seen[id] = true
	}
}

// An unreadable file must not take down the brief: partial memory beats none.
func TestUnreadableFactIsSkipped(t *testing.T) {
	dir := tmpWorkspace(t)
	if _, err := writeFact(dir, fact{Kind: "gotcha", By: "Darcy", At: time.Now(), Body: "real"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir(dir), "junk.md"), []byte("not a fact"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readFacts(dir, false)
	if err != nil || len(got) != 1 {
		t.Fatalf("got %d facts, %v — junk should be skipped, not fatal", len(got), err)
	}
}

// A project with nothing learned yet is a normal state, not an error.
func TestEmptyMemoryIsNotAnError(t *testing.T) {
	if got, err := readFacts(tmpWorkspace(t), false); err != nil || len(got) != 0 {
		t.Errorf("readFacts on a fresh project = %d, %v", len(got), err)
	}
}
