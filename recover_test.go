package main

import (
	"os"
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

// claudeProjectDir must match Claude Code's real encoding: every non-alphanumeric char → '-',
// consecutive separators NOT collapsed. Cases verified against real ~/.claude/projects names.
func TestClaudeProjectDir(t *testing.T) {
	cases := map[string]string{
		"/Users/darcy/dev/hoops-dashboard":       "-Users-darcy-dev-hoops-dashboard",
		"/Users/darcy/dev/hoops/hoops-dashboard": "-Users-darcy-dev-hoops-hoops-dashboard",
		"/Users/darcy/.claude-mem/observer":      "-Users-darcy--claude-mem-observer", // '/.' → '--' (not collapsed)
	}
	for in, want := range cases {
		if got := claudeProjectDir(in); got != want {
			t.Errorf("claudeProjectDir(%q) = %q, want %q", in, got, want)
		}
	}
}

// migrateSessionStore copies a claude transcript into the NEW cwd's project dir (so `claude
// --resume` finds it there), leaves the original as a backup, and refuses tools it doesn't know.
func TestMigrateSessionStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Seed a claude store file under the OLD cwd's project dir.
	oldProj := filepath.Join(home, ".claude", "projects", claudeProjectDir("/old/cwd"))
	if err := os.MkdirAll(oldProj, 0o755); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(oldProj, "sess-123.jsonl")
	if err := os.WriteFile(store, []byte(`{"cwd":"/old/cwd"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := aiSession{Tool: "claude", ID: "sess-123", storePath: store}

	newCwd := filepath.Join(home, "moved")
	if err := migrateSessionStore(s, newCwd); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	dest := filepath.Join(home, ".claude", "projects", claudeProjectDir(newCwd), "sess-123.jsonl")
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("transcript not migrated to the new project dir (%s): %v", dest, err)
	}
	if _, err := os.Stat(store); err != nil {
		t.Error("original store must remain as a backup (copy, not move)")
	}

	// A tool we haven't taught this to must error, not pretend to relocate.
	if err := migrateSessionStore(aiSession{Tool: "codex", ID: "x", storePath: store}, newCwd); err == nil {
		t.Error("an unsupported tool must return an error")
	}
}
