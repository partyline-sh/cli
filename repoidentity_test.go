package main

import "testing"

// The SAME cases as web/src/lib/api/repo-identity.test.ts, deliberately. Two implementations of one
// question drift the moment their tests differ, and a drift here is invisible: a project that IS
// this repo gets refused as somebody else's, quoting the repo's own remote back at the user.

func TestNormalizeRemoteFoldsEverySpellingOfOneRepo(t *testing.T) {
	spellings := []string{
		"git@github.com:partyline-sh/partyline.git",
		"https://github.com/partyline-sh/partyline.git",
		"https://github.com/partyline-sh/partyline",
		"ssh://git@github.com/partyline-sh/partyline.git",
		"git://github.com/partyline-sh/partyline.git",
		"https://github.com/partyline-sh/partyline/",
		"HTTPS://GitHub.com/Partyline-SH/Partyline.git",
	}
	const want = "github.com/partyline-sh/partyline"
	for _, s := range spellings {
		if got := normalizeRemote(s); got != want {
			t.Errorf("normalizeRemote(%q) = %q, want %q", s, got, want)
		}
	}
	for _, a := range spellings {
		for _, b := range spellings {
			if !sameRemote(a, b) {
				t.Errorf("sameRemote(%q, %q) = false, want true", a, b)
			}
		}
	}
}

func TestSameRemoteKeepsDifferentReposDifferent(t *testing.T) {
	cases := [][2]string{
		{"git@github.com:acme/api.git", "git@github.com:acme/web.git"},
		// The dangerous one: a fork, or two teams with a repo called `infra`. Matching these would
		// point an agent at another team's context.
		{"git@github.com:acme/infra.git", "git@github.com:globex/infra.git"},
		{"git@github.com:acme/api.git", "git@gitlab.com:acme/api.git"},
	}
	for _, c := range cases {
		if sameRemote(c[0], c[1]) {
			t.Errorf("sameRemote(%q, %q) = true, want false", c[0], c[1])
		}
	}
}

// An empty normalization must be a NON-match, not a wildcard: if "" matched "", a repo would link to
// whichever project happened to have a blank repo_url.
func TestNormalizeRemoteRejectsUnusableInput(t *testing.T) {
	for _, junk := range []string{"", "   ", "not-a-remote", "./local/path", "../sibling/repo"} {
		if got := normalizeRemote(junk); got != "" {
			t.Errorf("normalizeRemote(%q) = %q, want empty", junk, got)
		}
	}
	if sameRemote("", "") || sameRemote("nonsense", "nonsense") {
		t.Error("two unusable remotes reported as the same repo")
	}
}

// A port is transport, not identity: the same repo reached on :22 and :2222 is one repo.
func TestSameRemoteIgnoresAnExplicitPort(t *testing.T) {
	if !sameRemote("ssh://git@github.com:2222/acme/api.git", "git@github.com:acme/api.git") {
		t.Error("an explicit ssh port made one repo look like two")
	}
}
