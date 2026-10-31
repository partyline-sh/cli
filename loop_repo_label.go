package main

import (
	"path/filepath"
	"strings"
	"sync"
)

// repoNames caches repoName: a run's checkout keeps its repository, and the Loops popup redraws often.
var repoNames sync.Map

// repoName names the repository a checkout belongs to by its main checkout's folder, so every
// worktree of one repository has the same name. A checkout git cannot read (removed since) is named
// by its folder.
func repoName(dir string) string {
	if v, ok := repoNames.Load(dir); ok {
		return v.(string)
	}
	name := filepath.Base(dir)
	if out, err := gitMem(dir, "rev-parse", "--path-format=absolute", "--git-common-dir"); err == nil && strings.TrimSpace(out) != "" {
		common := strings.TrimSpace(out)
		n := strings.TrimSuffix(filepath.Base(common), ".git")
		if filepath.Base(common) == ".git" {
			n = filepath.Base(filepath.Dir(common))
		}
		if n != "" {
			name = n
		}
	}
	repoNames.Store(dir, name)
	return name
}

// repoLabel is repoName with the worktree's folder after it when that differs: "partyline (batchbar)".
func repoLabel(dir string) string {
	name := repoName(dir)
	if folder := filepath.Base(dir); folder != name {
		return name + " (" + folder + ")"
	}
	return name
}
