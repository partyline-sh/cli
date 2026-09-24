package main

import (
	"strings"
	"testing"
	"time"
)

func f(kind, repo, by, body string, ageH int, tags ...string) fact {
	return fact{ID: kind + by + body[:4], Kind: kind, Repo: repo, By: by,
		At: time.Now().Add(-time.Duration(ageH) * time.Hour), Body: body, Tags: tags}
}

// The brief is what makes a teammate's session know what yours learned, so what it puts FIRST
// decides whether anyone reads it: this repo's facts, then the project's, questions above
// settled things because only a question needs a human.
func TestRankFactsPutsThisRepoFirst(t *testing.T) {
	in := []fact{
		f("decision", "", "Darcy", "project-wide thing", 1),
		f("decision", "integration", "Matt", "other repo thing", 1),
		f("decision", "fleet-manager", "Darcy", "this repo thing", 2),
		f("question", "", "Matt", "who owns retries?", 3),
	}
	got := rankFacts(in, "fleet-manager")
	if !strings.Contains(got[0].Body, "this repo") {
		t.Errorf("this repo's fact is not first: %q", got[0].Body)
	}
	if !strings.Contains(got[1].Body, "who owns retries") {
		t.Errorf("an open question should outrank settled project facts, got %q", got[1].Body)
	}
	last := got[len(got)-1]
	if last.Repo != "integration" {
		t.Errorf("another repo's fact should sort last, got %+v", last)
	}
}

// A brief that dumps everything is noise and gets skimmed, which is worse than none because it
// looks like coverage.
func TestBriefIsCapped(t *testing.T) {
	var many []fact
	for i := 0; i < briefMax+8; i++ {
		many = append(many, f("decision", "fleet-manager", "Darcy", "thing number "+string(rune('a'+i)), i))
	}
	out := renderBrief("acr", "fleet-manager", many, briefMax)
	if n := strings.Count(out, "- ["); n != briefMax {
		t.Errorf("brief carried %d facts, cap is %d", n, briefMax)
	}
	if !strings.Contains(out, "more: `ptln memory ls`") {
		t.Error("a truncated brief must say what it left out")
	}
}

// Two people's agents holding opposite beliefs is the silent failure that sends humans back to
// pasting transcripts. A project-wide fact and a repo-scoped one DO collide — requiring equal
// scope hid exactly this pair in the first live test.
func TestConflictsAcrossScopes(t *testing.T) {
	facts := []fact{
		f("decision", "", "Darcy", "we use gRPC between fleet and integration", 2, "transport"),
		f("decision", "integration", "Matt", "we use REST, gRPC was dropped", 1, "transport"),
	}
	out := renderBrief("acr", "fleet-manager", facts, briefMax)
	if !strings.Contains(out, "these disagree") {
		t.Fatalf("a contradiction went unreported:\n%s", out)
	}
	if !strings.Contains(out, "Darcy") || !strings.Contains(out, "Matt") {
		t.Error("a conflict must name both authors so a human can settle it")
	}
}

// One person refining their own belief is not a disagreement, and neither are two facts about
// unrelated subjects.
func TestNoFalseConflicts(t *testing.T) {
	same := []fact{
		f("decision", "", "Darcy", "gRPC", 2, "transport"),
		f("decision", "", "Darcy", "gRPC with retries", 1, "transport"),
	}
	if strings.Contains(renderBrief("acr", "", same, briefMax), "disagree") {
		t.Error("one author refining their own decision is not a conflict")
	}
	unrelated := []fact{
		f("decision", "", "Darcy", "gRPC", 2, "transport"),
		f("decision", "", "Matt", "Postgres 16", 1, "database"),
	}
	if strings.Contains(renderBrief("acr", "", unrelated, briefMax), "disagree") {
		t.Error("facts about different subjects are not a conflict")
	}
}
