package main

// `ptln project init | join | add-repo | repos` — the multi-repo half of a project.
//
// A project's memory repo is the shared artifact: `init` makes one, `join` clones somebody
// else's, `add-repo` declares that the repository you are standing in is part of it. After that
// every session started in any member repo resolves the same project and reads the same memory.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"partyline.sh/partyline/internal/gitwt"
)

// projectInit creates a project's memory repo locally. A remote is optional at this point: a
// project that has not been shared yet is a normal state, and `ptln memory sync` carries
// everything the moment one is added.
func projectInit(args []string) {
	label, remote := "", ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--remote" && i+1 < len(args) {
			remote, i = args[i+1], i+1
			continue
		}
		if label == "" {
			label = args[i]
		}
	}
	if label == "" {
		fatal(fmt.Errorf("usage: ptln project init <label> [--remote <git-url>]"))
	}
	dir := projectWorkspaceDir(label)
	if _, err := os.Stat(projectFilePath(dir)); err == nil {
		fatal(fmt.Errorf("project %q already exists here (%s)", label, dir))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fatal(err)
	}
	if _, err := gitMem(dir, "rev-parse", "--git-dir"); err != nil {
		if out, err := gitMem(dir, "init", "-q"); err != nil {
			fatal(fmt.Errorf("git init: %s", out))
		}
	}
	if remote != "" {
		if out, err := gitMem(dir, "remote", "add", "origin", remote); err != nil {
			fatal(fmt.Errorf("git remote add: %s", out))
		}
	}
	if err := saveProjectFile(dir, projectFile{Label: label}); err != nil {
		fatal(err)
	}
	_ = memoryCommit(dir, "project: "+label)
	fmt.Printf("✓ project %q created at %s\n", label, dir)
	if remote == "" {
		fmt.Println("  no remote yet — add one and run `ptln memory sync` to share it:")
		fmt.Printf("    git -C %s remote add origin <git-url>\n", dir)
	}
	fmt.Println("  now, inside each repo that belongs to it:  ptln project add-repo")
}

// projectJoin clones a project somebody else created. One clone and this machine knows the
// project's repos and everything the team has learned — including for repos not checked out here.
func projectJoin(args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: ptln project join <git-url> [<label>]"))
	}
	url := args[0]
	label := ""
	if len(args) > 1 {
		label = args[1]
	}
	if label == "" {
		label = strings.TrimSuffix(filepath.Base(strings.TrimSuffix(url, "/")), ".git")
	}
	dir := projectWorkspaceDir(label)
	if err := os.MkdirAll(projectsRoot(), 0o755); err != nil {
		fatal(err)
	}
	if out, err := gitMem(projectsRoot(), "clone", "--quiet", url, dir); err != nil {
		fatal(fmt.Errorf("could not clone the project: %s", firstLineOf(out)))
	}
	ws, err := loadProjectWorkspace(label)
	if err != nil {
		fatal(fmt.Errorf("cloned, but %s has no project.json — is this a partyline project repo?", url))
	}
	fmt.Printf("✓ joined %q (%d repo(s))\n", ws.Label, len(ws.File.Repos))
	for _, r := range ws.File.Repos {
		fmt.Printf("    %-20s %s\n", r.Name, r.Remote)
	}
	fmt.Println("  in each repo you have cloned:  ptln project add-repo")
}

// projectAddRepo declares the repository you are standing in a member of a project: recorded in
// the project's definition (shared) and in the repo's own .partyline.json (checked in), so a
// teammate who clones the repo inherits the link without doing anything.
func projectAddRepo(args []string) {
	label, name := "", ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--name" && i+1 < len(args) {
			name, i = args[i+1], i+1
			continue
		}
		if label == "" {
			label = args[i]
		}
	}
	cwd, _ := os.Getwd()
	repo, err := gitwt.RepoRoot(cwd)
	if err != nil {
		fatal(fmt.Errorf("not a git repository — a project member is a repo"))
	}
	remote := gitOriginURL(repo)
	if remote == "" {
		fatal(fmt.Errorf("this repo has no `origin` remote — a member is identified by its remote, because a path means a different repo on somebody else's machine"))
	}
	if label == "" {
		if ws, ok := projectForDir(cwd); ok {
			label = ws.Label
		}
	}
	if label == "" {
		fatal(fmt.Errorf("which project? `ptln project add-repo <label>` (list them: ptln project repos)"))
	}
	ws, err := loadProjectWorkspace(label)
	if err != nil {
		fatal(err)
	}
	if name == "" {
		name = filepath.Base(repo)
	}

	for _, r := range ws.File.Repos {
		if sameProjectRepo(r.Remote, remote) {
			writeRepoProject(repo, ws.Label)
			fmt.Printf("✓ %s is already part of %q — the repo now points at it\n", r.Name, ws.Label)
			return
		}
	}
	ws.File.Repos = append(ws.File.Repos, struct {
		Name   string `json:"name"`
		Remote string `json:"remote"`
		Note   string `json:"note,omitempty"`
	}{Name: name, Remote: remote})
	if err := saveProjectFile(ws.Dir, ws.File); err != nil {
		fatal(err)
	}
	writeRepoProject(repo, ws.Label)
	if err := memorySync(ws.Dir, "project: add repo "+name); err != nil {
		fmt.Printf("✓ added %s locally, but NOT shared yet: %v\n", name, err)
		return
	}
	fmt.Printf("✓ %s is part of %q — commit .partyline.json so teammates inherit the link\n", name, ws.Label)
}

// writeRepoProject records the project in the repo's checked-in bind file, preserving a thread
// pin that may already be there.
func writeRepoProject(repo, label string) {
	rb := repoBind{Project: label}
	if t := loadRepoBind(repo); t != "" {
		rb.Thread = t
	}
	if err := saveRepoBindFile(repo, rb); err != nil {
		fmt.Printf("  (could not write %s: %v)\n", repoBindPath(repo), err)
	}
}

func projectRepos(_ []string) {
	cwd, _ := os.Getwd()
	ws, ok := projectForDir(cwd)
	if !ok {
		entries, _ := os.ReadDir(projectsRoot())
		if len(entries) == 0 {
			fmt.Println("no projects on this machine — `ptln project init <label>` or `ptln project join <git-url>`")
			return
		}
		fmt.Println("projects on this machine:")
		for _, e := range entries {
			fmt.Println("   ", e.Name())
		}
		return
	}
	fmt.Printf("%s — %d repo(s)\n", ws.Label, len(ws.File.Repos))
	here := ws.repoNameFor(cwd)
	for _, r := range ws.File.Repos {
		mark := " "
		if r.Name == here {
			mark = "▸"
		}
		fmt.Printf("  %s %-20s %s\n", mark, r.Name, r.Remote)
	}
	fmt.Printf("\nmemory: %s\n", ws.Dir)
}
