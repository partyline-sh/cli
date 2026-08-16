package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// P2 of provisioned workers (docs/plans/provisioned-workers.md). The security-load-bearing pieces are
// pure and tested here: repo-name validation (the wall before a server string becomes a path), the
// derived managed-dir path, the opt-in toggle, and the verify-tool preflight parse. The clone itself
// (network + a real GitHub token) is exercised in the end-to-end dogfood, not here.

func TestRepoFullNameRe(t *testing.T) {
	good := []string{"acme/web", "acme-org/repo.js", "a/b", "Org123/Repo_1"}
	bad := []string{"../etc", "acme", "a/b/c", "/abs", "a b/c", "acme/", "/repo", "a/..", "..//x", ""}
	for _, s := range good {
		if !repoFullNameRe.MatchString(s) {
			t.Errorf("repoFullNameRe rejected valid %q", s)
		}
	}
	for _, s := range bad {
		if repoFullNameRe.MatchString(s) {
			t.Errorf("repoFullNameRe ACCEPTED invalid %q", s)
		}
	}
}

func TestManagedRepoDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := managedRepoDir("acme/web")
	want := filepath.Join(home, ".partyline", "daemon", "repos", "acme", "web")
	if dir != want {
		t.Fatalf("managedRepoDir = %q, want %q", dir, want)
	}
	// Traversal / malformed names never yield a path — the caller refuses the run.
	for _, bad := range []string{"../../etc/passwd", "acme", "a/b/c", "/x", "..", "acme/../..", ""} {
		if got := managedRepoDir(bad); got != "" {
			t.Errorf("managedRepoDir(%q) = %q, want \"\" (rejected)", bad, got)
		}
	}
	// The derived path is always contained under the managed repos root — no escape.
	root := filepath.Join(home, ".partyline", "daemon", "repos")
	if !strings.HasPrefix(managedRepoDir("owner/name"), root+string(os.PathSeparator)) {
		t.Fatal("managed dir escaped the repos root")
	}
}

func TestProvisionEnabledToggle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if provisionEnabled() {
		t.Fatal("provision should be OFF by default")
	}
	if err := setProvisionEnabled(true); err != nil {
		t.Fatal(err)
	}
	if !provisionEnabled() {
		t.Fatal("provision should be ON after enable")
	}
	if err := setProvisionEnabled(false); err != nil {
		t.Fatal(err)
	}
	if provisionEnabled() {
		t.Fatal("provision should be OFF after disable")
	}
	// disable is idempotent (no error when already off).
	if err := setProvisionEnabled(false); err != nil {
		t.Fatalf("second disable errored: %v", err)
	}
}

func TestVerifyCommandTools(t *testing.T) {
	dir := t.TempDir()
	pd := filepath.Join(dir, ".partyline")
	if err := os.MkdirAll(pd, 0o755); err != nil {
		t.Fatal(err)
	}
	verify := "# a comment\nnpm run typecheck\n\n  go test ./...  \nNODE_ENV=test npm run build\n"
	if err := os.WriteFile(filepath.Join(pd, "verify"), []byte(verify), 0o644); err != nil {
		t.Fatal(err)
	}
	tools := verifyCommandTools(dir)
	want := []string{"npm", "go", "npm"} // comment + blank skipped; env prefix skipped to the real cmd
	if strings.Join(tools, ",") != strings.Join(want, ",") {
		t.Fatalf("verifyCommandTools = %v, want %v", tools, want)
	}
	// No file → no tools (best-effort, never errors).
	if got := verifyCommandTools(t.TempDir()); got != nil {
		t.Fatalf("verifyCommandTools with no file = %v, want nil", got)
	}
}

func TestEngineBinName(t *testing.T) {
	cases := map[string]string{"": "claude", "claude": "claude", "codex": "codex", "gemini": "gemini", "antigravity": "agy", "grok": ""}
	for in, want := range cases {
		if got := engineBinName(in); got != want {
			t.Errorf("engineBinName(%q) = %q, want %q", in, got, want)
		}
	}
}
