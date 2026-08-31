package main

import (
	"strings"
	"testing"

	"partyline.sh/partyline/internal/gitwt"
)

// Real titles from this repo's own stranded branches. The old naming spent its four words on filler;
// these are what a person actually scans a branch list for.
func TestBranchSlugsKeepTheWordsThatIdentifyTheTask(t *testing.T) {
	for _, tc := range []struct{ task, want string }{
		{"Post a human's CLI party message under their login identity", "Post-human-s-CLI-party-message"},
		{"List every partyline MCP registration on this machine", "List-every-partyline-MCP-registration"},
		{"Gate the production deploy on a green staging smoke", "Gate-production-deploy-green-staging"},
		{"A party-mcp session whose party has ended withdraws its tools", "party-mcp-session-whose-party-has"},
	} {
		got := gitwt.FlatSlug(taskSlugWords(tc.task, slugWords))
		if got != tc.want {
			t.Errorf("task %q\n  got  %q\n  want %q", tc.task, got, tc.want)
		}
	}
}

// A nameless branch is worse than a bland one, so all-filler falls back to the original words rather
// than producing nothing.
func TestAllFillerFallsBackRatherThanVanishing(t *testing.T) {
	if got := taskSlugWords("To the point of it", slugWords); got == "" {
		t.Error("an all-filler title produced an empty slug")
	}
}

// The run-id fragment is load-bearing: unique PER run, stable WITHIN one, which is what resume,
// restart and chain stacking each depend on. A prettier slug must not have cost us that.
func TestTheRunFragmentSurvives(t *testing.T) {
	a := taskBranchName("9ec33667-aaaa", 0, "List every partyline MCP registration")
	b := taskBranchName("32626544-bbbb", 0, "List every partyline MCP registration")
	if a == b {
		t.Fatal("two runs with the same task produced the SAME branch — that is how four runs once piled onto one franken-PR")
	}
	if !strings.HasPrefix(a, "crank-9ec33667-01-") {
		t.Errorf("branch %q lost its run fragment or its index", a)
	}
	if a != taskBranchName("9ec33667-aaaa", 0, "List every partyline MCP registration") {
		t.Error("the same run+task produced two different names — resume reuses this")
	}
}

// A generated name must stay a single segment: a title with slashes would otherwise produce a ref
// that cannot coexist with its own prefix (git's directory/file conflict).
func TestAGeneratedNameIsOneSegment(t *testing.T) {
	got := taskBranchName("run1234", 2, "Move Epic/Feature/Task subtree to another project")
	if strings.Contains(strings.TrimPrefix(got, "crank-"), "/") {
		t.Errorf("branch %q contains a slash", got)
	}
}
