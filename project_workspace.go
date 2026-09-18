package main

// A PROJECT IS A SET OF REPOS, NOT A REPO.
//
// The old model keyed a project on one git remote: one `repo_url`, one `.partyline.json`, one
// checkout. Real work does not look like that. ACR is a dozen repositories — a fleet manager
// here, an integration codebase there, several services — worked on by people who do not all
// have the same ones cloned. Under the old model each repo was its own island with its own
// memory, so two people on one project could not share what they learned without pasting it to
// each other by hand.
//
// So: a project is a NAME, a set of member repos, and one MEMORY HOME that every member of the
// project has. The memory home is an ordinary git repository — the same transport the team
// already uses for code, which means no server, no account, offline-capable, reviewable, and
// available to a teammate who has none of the code repos cloned.
//
// Each member repo keeps a checked-in `.partyline.json` naming the project. That is what lets a
// session started anywhere in the project resolve the whole project from the working directory.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"partyline.sh/partyline/internal/gitwt"
)

// projectFile is `project.json` at the root of the memory repo: the project's definition, shared
// by everyone who clones it.
type projectFile struct {
	Label string `json:"label"`
	Repos []struct {
		Name   string `json:"name"`           // short handle ("fleet-manager")
		Remote string `json:"remote"`         // canonical identity (normalized git remote)
		Note   string `json:"note,omitempty"` // what this repo is, one line
	} `json:"repos"`
}

// projectWorkspace is a project as this machine sees it: the definition plus where its memory
// clone lives locally.
type projectWorkspace struct {
	Label string
	Dir   string // local clone of the memory repo
	File  projectFile
}

// projectsRoot is where memory clones live. One directory per project label.
func projectsRoot() string { return filepath.Join(stateDir(), "projects") }

func projectWorkspaceDir(label string) string { return filepath.Join(projectsRoot(), label) }

// projectFilePath is the definition inside a memory clone.
func projectFilePath(dir string) string { return filepath.Join(dir, "project.json") }

// loadProjectWorkspace reads a project by label from this machine's clones.
func loadProjectWorkspace(label string) (*projectWorkspace, error) {
	dir := projectWorkspaceDir(label)
	b, err := os.ReadFile(projectFilePath(dir))
	if err != nil {
		return nil, fmt.Errorf("no project %q on this machine — `ptln project join <git-url>` to get it", label)
	}
	var pf projectFile
	if err := json.Unmarshal(b, &pf); err != nil {
		return nil, fmt.Errorf("%s is not readable: %w", projectFilePath(dir), err)
	}
	if pf.Label == "" {
		pf.Label = label
	}
	return &projectWorkspace{Label: pf.Label, Dir: dir, File: pf}, nil
}

// saveProjectFile writes the definition back. Callers commit it; see memoryCommit.
func saveProjectFile(dir string, pf projectFile) error {
	b, err := json.MarshalIndent(pf, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(projectFilePath(dir), append(b, '\n'), 0o644)
}

// projectForDir answers "which project is this working directory part of", which is the question
// every session start asks. The repo's checked-in .partyline.json names it; no network, no server.
func projectForDir(dir string) (*projectWorkspace, bool) {
	repo, err := gitwt.RepoRoot(dir)
	if err != nil {
		return nil, false
	}
	b, err := os.ReadFile(repoBindPath(repo))
	if err != nil {
		return nil, false
	}
	var rb repoBind
	if json.Unmarshal(b, &rb) != nil || strings.TrimSpace(rb.Project) == "" {
		return nil, false
	}
	ws, err := loadProjectWorkspace(rb.Project)
	if err != nil {
		return nil, false
	}
	return ws, true
}

// repoNameFor returns the project's short handle for this directory's repository, matching on
// the canonical remote so a clone under any path (or an ssh host alias) still resolves.
func (w *projectWorkspace) repoNameFor(dir string) string {
	repo, err := gitwt.RepoRoot(dir)
	if err != nil {
		return ""
	}
	want := gitOriginURL(repo)
	for _, r := range w.File.Repos {
		if sameProjectRepo(r.Remote, want) {
			return r.Name
		}
	}
	return ""
}

// sameProjectRepo decides whether two remotes name the same MEMBER of a project.
//
// Distinct from repoidentity.go's sameRemote, which answers cross-machine identity and rightly
// refuses to match two remotes it cannot canonicalise: over there a bare path is meaningless
// because it names different code on different machines. Here the comparison is between entries
// in ONE project file and the remote of a checkout that claims membership, so falling back to
// the literal string is both meaningful and necessary — a team whose git server has no dot in
// its hostname, or a local path, would otherwise be unable to join a repo at all.
//
// It must never do what the first version did and treat two unparseable remotes as equal: that
// matched every such repo to every other, and in a two-repo fixture it declared a brand-new
// integration repo to be the fleet manager.
func sameProjectRepo(a, b string) bool {
	if na, nb := normalizeRemote(a), normalizeRemote(b); na != "" && nb != "" {
		return na == nb
	}
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	return a != "" && a == b
}
