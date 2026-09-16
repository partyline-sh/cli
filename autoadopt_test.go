package main

import (
	"os"
	"path/filepath"
	"testing"

	"partyline.sh/partyline/internal/api"
)

// matchAdoptions is the decision core of auto-adopt: which canonical projects THIS machine should
// register, given its registry and its local clones. Everything here is the rule stated in the
// function's doc, pinned.

func TestMatchAdoptionsAdoptsByRemoteIdentity(t *testing.T) {
	projects := []api.CanonicalProject{
		{Label: "partyline", RepoURL: "git@github.com:partyline-sh/partyline.git"},
	}
	repos := []localRepoRemote{
		// Different spelling of the SAME repo — the whole reason matching goes through sameRemote.
		{Path: "/home/x/dev/partyline", Remote: "https://github.com/partyline-sh/partyline"},
	}
	got := matchAdoptions(projects, map[string]bool{}, repos)
	if len(got) != 1 {
		t.Fatalf("expected 1 adoption, got %d", len(got))
	}
	if got[0].Label != "partyline" || got[0].Path != "/home/x/dev/partyline" || got[0].Preset != "spec" {
		t.Fatalf("wrong adoption: %+v", got[0])
	}
}

func TestMatchAdoptionsNeverRepointsAnExistingLabel(t *testing.T) {
	projects := []api.CanonicalProject{
		{Label: "partyline", RepoURL: "git@github.com:partyline-sh/partyline.git"},
	}
	repos := []localRepoRemote{{Path: "/somewhere/else", Remote: "git@github.com:partyline-sh/partyline.git"}}
	if got := matchAdoptions(projects, map[string]bool{"partyline": true}, repos); len(got) != 0 {
		t.Fatalf("an already-registered label must never be repointed, got %+v", got)
	}
}

func TestMatchAdoptionsSkipsWhatItCannotIdentify(t *testing.T) {
	projects := []api.CanonicalProject{
		{Label: "no-repo"}, // nothing to match on
		{Label: "elsewhere", RepoURL: "git@github.com:o/r.git"},              // repo this machine doesn't have
		{Label: string(make([]byte, 60)), RepoURL: "git@github.com:o/x.git"}, // label fails labelRe
	}
	repos := []localRepoRemote{{Path: "/home/x/dev/thing", Remote: "git@github.com:o/x.git"}}
	if got := matchAdoptions(projects, map[string]bool{}, repos); len(got) != 0 {
		t.Fatalf("expected no adoptions, got %+v", got)
	}
}

func TestMatchAdoptionsFirstMatchingCloneWins(t *testing.T) {
	projects := []api.CanonicalProject{{Label: "p", RepoURL: "git@github.com:o/p.git"}}
	repos := []localRepoRemote{
		{Path: "/a/p", Remote: "git@github.com:o/p.git"},
		{Path: "/b/p", Remote: "https://github.com/o/p"},
	}
	got := matchAdoptions(projects, map[string]bool{}, repos)
	if len(got) != 1 || got[0].Path != "/a/p" {
		t.Fatalf("expected exactly one adoption at the first clone, got %+v", got)
	}
}

// The cache and the opt-out marker live under daemonDir(), which follows HOME in tests.
func TestCanonicalCacheRoundTripAndAutoAdoptToggle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if _, ok := readCanonicalCache(); ok {
		t.Fatal("no cache written yet — read must report absence, not an empty cache")
	}
	writeCanonicalCache(canonicalCache{FetchedAt: "2026-09-16T00:00:00Z", Projects: []canonicalProject{
		{ID: "1", Label: "partyline", Visibility: "team", AdoptedHere: true, HasRepo: true},
	}})
	c, ok := readCanonicalCache()
	if !ok || len(c.Projects) != 1 || c.Projects[0].Label != "partyline" || !c.Projects[0].AdoptedHere {
		t.Fatalf("cache did not round-trip: %+v ok=%v", c, ok)
	}

	if !autoAdoptEnabled() {
		t.Fatal("auto-adopt must default ON — the default-off convenience is a convenience nobody has")
	}
	if err := setAutoAdoptEnabled(false); err != nil {
		t.Fatal(err)
	}
	if autoAdoptEnabled() {
		t.Fatal("off marker written but still enabled")
	}
	if err := setAutoAdoptEnabled(true); err != nil {
		t.Fatal(err)
	}
	if !autoAdoptEnabled() {
		t.Fatal("marker removed but still disabled")
	}
	// Turning it on twice must not error on the already-absent marker.
	if err := setAutoAdoptEnabled(true); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(daemonDir(), "canonical_projects.json"))
}
