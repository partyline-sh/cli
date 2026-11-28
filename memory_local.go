package main

// REPO-LOCAL MEMORY: the facts live in the code repo, not in a side repo.
//
// Memory used to live in `~/.partyline/projects/<label>/memory`, a partyline-managed clone with
// its own git remote. That worked and nobody could tell: you had to join a project, add the repo,
// and have a memory remote before a single fact could be written, and the facts arrived in a
// repository the person reading the code had never cloned.
//
// So the default is now `<repo>/memory`. Checking out the code checks out what the team learned
// about it, and the facts are reviewed in the same pull request as the change that taught them.
// No project, no label, no second remote, no server.
//
// PARTYLINE NEVER COMMITS YOUR REPO. In the side-repo model `memorySync` committed and pushed,
// because that repo was ours. This one is yours: a fact is written and left in the working tree
// for you to commit with the change it came from. An agent that committed your repo on its own
// would be a worse problem than the one this solves.

import (
	"os"
	"path/filepath"

	"partyline.sh/partyline/internal/gitwt"
)

// memoryLocalDirName is the directory inside the repo. Plural-free and lowercase to read as part
// of the repo rather than as a partyline artifact.
const memoryLocalDirName = "memory"

// repoMemory resolves the repo-local memory store for a directory.
//
// It returns the REPO ROOT, not the memory directory, because the store functions (writeFact,
// readFacts) take a workspace dir and append `memory/` themselves — so the repo root is the
// workspace, and every existing store call works unchanged.
//
// ok=false means this is not a git repository. A repo with no `memory/` yet is still ok: the first
// fact creates it, the same way the first commit creates a branch.
func repoMemory(dir string) (root string, ok bool) {
	if dir == "" {
		dir, _ = os.Getwd()
	}
	root, err := gitwt.RepoRoot(dir)
	if err != nil || root == "" {
		return "", false
	}
	return root, true
}

// hasRepoMemory reports whether this repo already carries facts. Used to decide whether to brief a
// session at all, and by the launcher to show that a checkout came with context.
func hasRepoMemory(dir string) bool {
	root, ok := repoMemory(dir)
	if !ok {
		return false
	}
	entries, err := os.ReadDir(memoryDir(root))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".md" {
			return true
		}
	}
	return false
}
