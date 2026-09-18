package main

import "testing"

// A project identifies a repo by its remote, because a path means a different repo on somebody
// else's machine. An ssh host alias is a per-machine spelling of the same repository, so it has
// to canonicalise — otherwise one teammate's "github-acr:acr-retail/x.git" and another's
// "git@github.com:acr-retail/x.git" are two different repos and facts about them never line up.
//
// This was found setting up a real project: three ACR repos, two written `git@github-acr:…` and
// one written `github-acr:…`. The two canonicalised and the third did not, because the pattern
// required a user@ prefix.
func TestAnSSHAliasCanonicalisesWithOrWithoutAUser(t *testing.T) {
	resolve := func(host string) string {
		if host == "github-acr" {
			return "github.com"
		}
		return ""
	}
	cases := map[string]string{
		"git@github-acr:acr-retail/acr-cloud-aggregator.git": "git@github.com:acr-retail/acr-cloud-aggregator.git",
		"github-acr:acr-retail/acr-integration-platform.git": "github.com:acr-retail/acr-integration-platform.git",
		"ssh://git@github-acr/acr-retail/x.git":              "ssh://git@github.com/acr-retail/x.git",
	}
	for raw, want := range cases {
		if got := canonicalRemote(raw, resolve); got != want {
			t.Errorf("canonicalRemote(%q) = %q, want %q", raw, got, want)
		}
	}
}

// Anything already naming a real host, or that is not an alias at all, must pass through
// untouched. Rewriting remotes that already work would be pure risk.
func TestOrdinaryRemotesArePassedThroughUnchanged(t *testing.T) {
	resolve := func(string) string { return "github.com" }
	for _, raw := range []string{
		"git@github.com:acr-retail/x.git",
		"https://github.com/acr-retail/x.git",
		"http://example.com/x.git",
		"ssh://git@github.com/acr-retail/x.git",
		"/Users/darcy/dev/x",
		"",
	} {
		if got := canonicalRemote(raw, resolve); got != raw {
			t.Errorf("canonicalRemote(%q) rewrote it to %q", raw, got)
		}
	}
}

// With the user@ now optional, a URL must not be mistaken for scp-style: "https://host/path"
// would otherwise parse as host "https".
func TestASchemeIsNotMistakenForAnSCPHost(t *testing.T) {
	resolve := func(host string) string {
		if host == "https" || host == "http" {
			t.Fatalf("a URL scheme %q was looked up as an ssh host", host)
		}
		return ""
	}
	for _, raw := range []string{"https://github.com/a/b.git", "http://example.com/a/b.git"} {
		if got := canonicalRemote(raw, resolve); got != raw {
			t.Errorf("canonicalRemote(%q) = %q", raw, got)
		}
	}
}

// Two spellings of one repository must be ONE identity. Without the optional user@ these
// normalised to "github.com:acr-retail/x" and "github.com/acr-retail/x" — a colon against a
// slash — so a repo written one way on your machine and the other way on a teammate's counted as
// two different repos, and shared facts about it silently failed to line up.
func TestTwoSpellingsOfOneRepoAreOneIdentity(t *testing.T) {
	want := normalizeRemote("git@github.com:acr-retail/acr-integration-platform.git")
	if want == "" {
		t.Fatal("the canonical spelling did not normalize at all")
	}
	for _, spelling := range []string{
		"github.com:acr-retail/acr-integration-platform.git",
		"https://github.com/acr-retail/acr-integration-platform.git",
		"ssh://git@github.com/acr-retail/acr-integration-platform.git",
		"git@github.com:acr-retail/acr-integration-platform",
	} {
		if got := normalizeRemote(spelling); got != want {
			t.Errorf("normalizeRemote(%q) = %q, want %q", spelling, got, want)
		}
	}
}

// A local path is not a shared identity — it means a different repo on someone else's machine —
// and must keep being refused.
func TestLocalPathsAreStillNotIdentities(t *testing.T) {
	for _, raw := range []string{"./local/path", "../sibling/repo", "/Users/darcy/dev/x", "justaword"} {
		if got := normalizeRemote(raw); got != "" {
			t.Errorf("normalizeRemote(%q) = %q, want it refused", raw, got)
		}
	}
}
