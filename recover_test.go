package main

import (
	"path/filepath"
	"testing"
)

// stripURLCreds must remove embedded credentials from https remotes (so a token never
// lands in the sidecar) while leaving ssh/scp-style remotes untouched.
func TestStripURLCreds(t *testing.T) {
	cases := map[string]string{
		"https://user:tok@github.com/o/r.git":         "https://github.com/o/r.git",
		"https://x-access-token:ghp_A@github.com/o/r": "https://github.com/o/r",
		"https://github.com/o/r.git":                  "https://github.com/o/r.git", // no creds
		"git@github.com:o/r.git":                      "git@github.com:o/r.git",     // scp-style: no scheme authority
		"ssh://git@host/o/r":                          "ssh://git@host/o/r",         // ssh user is not a secret
		"":                                            "",
	}
	for in, want := range cases {
		if got := stripURLCreds(in); got != want {
			t.Errorf("stripURLCreds(%q) = %q, want %q", in, got, want)
		}
	}
}

// captureSessionRepos must look at a session's cwd at most once ever: after RepoChecked
// is set, it's skipped even if no repo was found, so later launches never re-fork git.
func TestCaptureOnce(t *testing.T) {
	meta := map[string]sessMeta{}
	all := []aiSession{{ID: "a", Cwd: ""}} // no dir → never touched
	if captureSessionRepos(all, meta) {
		t.Error("a session with no cwd should not be captured")
	}
	// A session already marked checked (no repo) must be skipped, not re-looked.
	meta["b"] = sessMeta{RepoChecked: true}
	all = []aiSession{{ID: "b", Cwd: t.TempDir()}} // real dir, but already checked
	if captureSessionRepos(all, meta) {
		t.Error("a RepoChecked session must be skipped (no re-fork)")
	}
}

// applyCwdOverrides relocates a session to its recorded CwdOverride only when that path still
// exists; a session with no override, or one whose override has itself vanished, keeps its
// original (gone) cwd so recovery is offered again instead of pointing at nothing.
func TestApplyCwdOverrides(t *testing.T) {
	moved := t.TempDir()                            // the new location (exists)
	gone := filepath.Join(t.TempDir(), "not-a-dir") // an override that no longer exists
	sessions := []aiSession{
		{ID: "moved", Cwd: "/old/path/gone"},
		{ID: "stale", Cwd: "/old/path/gone"},
		{ID: "plain", Cwd: "/still/here"},
	}
	meta := map[string]sessMeta{
		"moved": {CwdOverride: moved},
		"stale": {CwdOverride: gone},
	}
	applyCwdOverrides(sessions, meta)
	if sessions[0].Cwd != moved {
		t.Errorf("moved: Cwd = %q, want the override %q", sessions[0].Cwd, moved)
	}
	if sessions[1].Cwd != "/old/path/gone" {
		t.Errorf("stale override (dir gone) must be ignored, got Cwd = %q", sessions[1].Cwd)
	}
	if sessions[2].Cwd != "/still/here" {
		t.Errorf("no override must leave Cwd untouched, got %q", sessions[2].Cwd)
	}
}
