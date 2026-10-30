package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The bar is shared with the user's own work: a directory that is not in a project gets NOTHING.
// Filling somebody's status line with our absence is how a status line becomes noise.
func TestStatusLineSaysNothingOutsideAProject(t *testing.T) {
	if got := memoryStatusLine(t.TempDir()); got != "" {
		t.Errorf("status line outside a project = %q, want empty", got)
	}
}

// Inside a project it answers the questions the human cannot otherwise see: where am I, what does
// the team know, and is anything wrong.
func TestStatusLineShowsProjectAndTrouble(t *testing.T) {
	ws := t.TempDir()
	if err := saveProjectFile(ws, projectFile{Label: "acr"}); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".partyline.json"), []byte(`{"project":"acr"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Point the resolver at our fixture by making the workspace the project's real home.
	t.Setenv("HOME", filepath.Dir(ws))
	if err := os.MkdirAll(projectWorkspaceDir("acr"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := saveProjectFile(projectWorkspaceDir("acr"), projectFile{Label: "acr"}); err != nil {
		t.Fatal(err)
	}
	home := projectWorkspaceDir("acr")
	now := time.Now()
	if _, err := writeFact(home, fact{Kind: "decision", By: "Darcy", At: now, Body: "gRPC between fleet and integration", Tags: []string{"transport"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := writeFact(home, fact{Kind: "decision", By: "Matt", At: now, Body: "REST between fleet and integration", Tags: []string{"transport"}}); err != nil {
		t.Fatal(err)
	}

	got := memoryStatusLine(repoDirFor(t, repo))
	if !strings.Contains(got, "acr") {
		t.Errorf("status line does not name the project: %q", got)
	}
	if !strings.Contains(got, "2 learned") {
		t.Errorf("status line does not count what the team knows: %q", got)
	}
	if !strings.Contains(got, "disagreement") {
		t.Errorf("a contradiction must be visible without opening anything: %q", got)
	}
}

// repoDirFor makes dir a real repository: the resolver asks git for the root, and a hand-made
// .git directory is not a repository to git.
func repoDirFor(t *testing.T, dir string) string {
	t.Helper()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return dir
}
