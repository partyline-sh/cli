package main

// A repo can belong to a project WITHOUT carrying a file.
//
// The designed path is a committed `.partyline.json`: the repo declares its project, and anyone
// who clones it joins automatically. That is right for a teammate and wrong as the ONLY way,
// because it means adding a repo to a project writes into that repo's working tree — which is
// not always yours to do, and is a poor trade for a machine that already knows the answer.
//
// The project file already records every member by canonical remote. So a directory can be
// matched to its project by asking git what the repo's origin is. No file, no commit, nothing in
// the working tree.
//
// WHY THIS IS CACHED. projectForDir runs on the status bar's redraw path. Resolving a remote
// shells out to `git remote get-url` and then, for an ssh host alias, to `ssh -G` — tens of
// milliseconds, several times a second, forever. The cache makes the second answer free. It is
// only ever a cache: delete it and the next call rebuilds it from the remote.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

func repoProjectCachePath() string { return filepath.Join(stateDir(), "repo-projects.json") }

var repoProjectMu sync.Mutex

// cachedProjectFor returns the project label remembered for this repo root, if any.
func cachedProjectFor(repoRoot string) string {
	b, err := os.ReadFile(repoProjectCachePath())
	if err != nil {
		return ""
	}
	var m map[string]string
	if json.Unmarshal(b, &m) != nil {
		return ""
	}
	return m[repoRoot]
}

// rememberProjectFor records the resolution. Best-effort: a failed write costs a repeated lookup,
// never a wrong answer.
func rememberProjectFor(repoRoot, label string) {
	repoProjectMu.Lock()
	defer repoProjectMu.Unlock()
	m := map[string]string{}
	if b, err := os.ReadFile(repoProjectCachePath()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	if m[repoRoot] == label {
		return
	}
	m[repoRoot] = label
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	tmp := repoProjectCachePath() + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, repoProjectCachePath())
	}
}

// forgetProjectFor drops a cached resolution, so a repo removed from a project stops resolving to
// it without waiting for anything to expire.
func forgetProjectFor(repoRoot string) {
	repoProjectMu.Lock()
	defer repoProjectMu.Unlock()
	m := map[string]string{}
	if b, err := os.ReadFile(repoProjectCachePath()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	if _, ok := m[repoRoot]; !ok {
		return
	}
	delete(m, repoRoot)
	if b, err := json.MarshalIndent(m, "", "  "); err == nil {
		_ = os.WriteFile(repoProjectCachePath(), b, 0o600)
	}
}

// projectByRemote finds the project that lists this repo's origin as a member. Returns "" when
// the repo has no origin, or no project claims it.
func projectByRemote(repoRoot string) string {
	remote := gitOriginURL(repoRoot)
	if remote == "" {
		return ""
	}
	for _, ws := range allWorkspaces() {
		for _, r := range ws.File.Repos {
			if sameProjectRepo(r.Remote, remote) {
				return ws.Label
			}
		}
	}
	return ""
}
