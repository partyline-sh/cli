package main

// Where session stores actually live — and why one home is not enough.
//
// THE BUG THIS FIXES. collectSessions() called os.UserHomeDir() once and handed that single home to
// every adapter, so every store glob was rooted at one place. A session started under a different
// $HOME — a migration, a service account, `sudo -u`, a second login on the same box — is not
// hard to find, it is STRUCTURALLY INVISIBLE. Reported from a real machine: `ptln` could not see an
// ACR session because that session had been started with a different home during a migration.
//
// It is worth being precise about whose bug it is: Claude writes to $HOME/.claude for whatever
// $HOME it had, which is correct. partyline was the one assuming there is only ever one.
//
// THE PART THAT IS NOT A GLOB CHANGE. A session found under another home cannot be resumed by
// running `claude --resume <id>` in the current environment — the tool resolves its own store from
// its own $HOME and will not find it. So a root is not a path, it is a path PLUS the environment a
// session under it must be resumed with. Widening the scan without that would list sessions that
// fail to resume, which is worse than not listing them at all.
//
// DISCOVERY, NOT CONFIGURATION. A config key the user has to already know about is, for this class
// of problem, the same as no feature: nobody reads the docs for a tool that appears to be working
// until it visibly is not. So the roots are OFFERED at the moment the user is already looking for a
// session that is not there (see llms_empty.go), and only then persisted.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// sessionRoot is one place session stores live, and the home to resume them under.
type sessionRoot struct {
	// Home is the $HOME whose dot-directories hold the stores. Adapters glob under it, and a
	// resume from this root runs with HOME set to it.
	Home string `json:"home"`
	// Primary marks the process's own home. Its sessions resume with the ambient environment, so
	// they are never marked in the UI and never carry an env override.
	Primary bool `json:"-"`
}

const rootsFile = "session-roots.json"

// loadSessionRoots returns the process's own home first, then any adopted roots.
//
// The primary is always first and always present: an adopted-roots file that somehow lists nothing
// (or is corrupt) must degrade to today's behaviour, never to "no sessions at all".
func loadSessionRoots() []sessionRoot {
	home, _ := os.UserHomeDir()
	roots := []sessionRoot{{Home: home, Primary: true}}

	b, err := os.ReadFile(filepath.Join(daemonDir(), rootsFile))
	if err != nil {
		return roots
	}
	var extra []sessionRoot
	if json.Unmarshal(b, &extra) != nil {
		return roots
	}
	seen := map[string]bool{home: true}
	for _, r := range extra {
		r.Home = strings.TrimSpace(r.Home)
		if r.Home == "" || seen[r.Home] {
			continue
		}
		// A root that has gone away is skipped rather than removed: an unplugged external disk or
		// an unmounted home should not silently erase the user's decision to look there.
		if fi, serr := os.Stat(r.Home); serr != nil || !fi.IsDir() {
			continue
		}
		seen[r.Home] = true
		roots = append(roots, sessionRoot{Home: r.Home})
	}
	return roots
}

// addSessionRoot persists a root. Idempotent, and it refuses the primary home — adopting the home
// you are already scanning would double every session in the list.
func addSessionRoot(home string) error {
	home = strings.TrimSpace(home)
	if home == "" {
		return nil
	}
	if abs, err := filepath.Abs(expandTilde(home)); err == nil {
		home = abs
	}
	if self, _ := os.UserHomeDir(); home == self {
		return nil
	}
	var extra []sessionRoot
	path := filepath.Join(daemonDir(), rootsFile)
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &extra)
	}
	for _, r := range extra {
		if r.Home == home {
			return nil
		}
	}
	extra = append(extra, sessionRoot{Home: home})
	if err := os.MkdirAll(daemonDir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(extra, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// storeDirs are the dot-directories that indicate an engine has kept sessions under a home. Used
// only to decide whether a candidate is worth OFFERING — the adapters do the real reading.
var storeDirs = []string{
	".claude/projects",
	".codex/sessions",
	".gemini/tmp",
	".antigravity",
}

// hasSessionStore reports whether a home looks like it holds sessions, and is READABLE by this
// process. Readability is the whole test: it means the operating system has already decided this
// process may see that directory, so there is no separate privacy judgement for partyline to make
// or to get wrong. A permission error is a silent skip, never a prompt.
func hasSessionStore(home string) bool {
	for _, d := range storeDirs {
		p := filepath.Join(home, d)
		fi, err := os.Stat(p)
		if err != nil || !fi.IsDir() {
			continue
		}
		// Stat alone is not enough — a directory can be listed in a parent it cannot be opened
		// through. Open it to confirm we could actually read what is inside.
		f, oerr := os.Open(p)
		if oerr != nil {
			continue
		}
		names, rerr := f.Readdirnames(1)
		_ = f.Close()
		if rerr == nil && len(names) > 0 {
			return true
		}
	}
	return false
}

// candidateRoots finds other homes on this machine that hold readable session stores.
//
// Bounded on purpose: it looks at the SIBLINGS of the current home (the migration and
// second-account cases, which is what this is for) and at nothing else. No filesystem walk, no
// /etc/passwd parse, no guessing at mount points — a handful of stats. A discovery step that is
// expensive is one that gets moved out of the hot path and then never runs.
func candidateRoots() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	adopted := map[string]bool{}
	for _, r := range loadSessionRoots() {
		adopted[r.Home] = true
	}

	entries, err := os.ReadDir(filepath.Dir(home))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		p := filepath.Join(filepath.Dir(home), e.Name())
		if adopted[p] || !e.IsDir() {
			continue
		}
		if hasSessionStore(p) {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// resumeEnv returns the environment a session from this root must be resumed with. The primary
// root resumes with the ambient environment untouched; an adopted one needs HOME pointed at it, or
// the engine looks for the session in the wrong place and reports that it does not exist.
func resumeEnv(root string) []string {
	if root == "" {
		return os.Environ()
	}
	self, _ := os.UserHomeDir()
	if root == self {
		return os.Environ()
	}
	out := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "HOME=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "HOME="+root)
}
