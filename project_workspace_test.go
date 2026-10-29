package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The flaw this whole change exists to fix: a project is a SET of repos. Two different repos in
// one project must resolve to that project and be told apart — the first version compared
// unparseable remotes as equal and declared a brand-new integration repo to be the fleet manager.
func TestProjectSpansManyRepos(t *testing.T) {
	pf := projectFile{Label: "acr"}
	pf.Repos = append(pf.Repos,
		struct {
			Name   string `json:"name"`
			Remote string `json:"remote"`
			Note   string `json:"note,omitempty"`
		}{Name: "fleet-manager", Remote: "git@github.com:acr/fleet.git"},
		struct {
			Name   string `json:"name"`
			Remote string `json:"remote"`
			Note   string `json:"note,omitempty"`
		}{Name: "integration", Remote: "git@github.com:acr/integration.git"},
	)
	dir := t.TempDir()
	if err := saveProjectFile(dir, pf); err != nil {
		t.Fatal(err)
	}
	ws := &projectWorkspace{Label: "acr", Dir: dir, File: pf}
	if len(ws.File.Repos) != 2 {
		t.Fatalf("a project must hold many repos, got %d", len(ws.File.Repos))
	}
}

// Identity comparison for MEMBERSHIP: canonical when both remotes parse, literal otherwise, and
// never "both unparseable therefore equal".
func TestSameProjectRepo(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"git@github.com:acr/fleet.git", "https://github.com/acr/fleet", true},
		{"git@github.com:acr/fleet.git", "git@github.com:acr/integration.git", false},
		{"/tmp/repo-fleet.git", "/tmp/repo-fleet.git", true},
		{"/tmp/repo-fleet.git", "/tmp/repo-integration.git", false}, // the bug the fixture caught
		{"", "", false},
		{"", "git@github.com:acr/fleet.git", false},
	}
	for _, c := range cases {
		if got := sameProjectRepo(c.a, c.b); got != c.want {
			t.Errorf("sameProjectRepo(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// A repo declares its project in a checked-in file, so a teammate who clones it inherits the
// link with no setup — and a thread pin already in that file survives.
func TestRepoBindKeepsBothFields(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".partyline.json"), []byte(`{"thread":"th_1","project":"acr"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeRepoBind(repo, "th_2"); err != nil {
		t.Fatal(err)
	}
	rb, err := readRepoBindFile(repo)
	if err != nil {
		t.Fatal(err)
	}
	if rb.Thread != "th_2" {
		t.Errorf("thread = %q, want th_2", rb.Thread)
	}
	if rb.Project != "acr" {
		t.Errorf("re-binding a thread detached the repo from its project (project = %q)", rb.Project)
	}
}
