package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"partyline.sh/partyline/internal/api"
)

// Local repositories this machine can offer as a project (the "Local directory" half of Repository
// access). A user whose repo is not on GitHub — or who simply has not connected it — still has a
// real git repo on a machine partyline already runs on, and until now the only way to bind it was
// `ptln daemon add-project` typed on that machine. The web read as GitHub-or-nothing.
//
// THIS IS A DELIBERATE SECOND ATTEMPT. A previous version of web-assignable projects was removed
// (fa4b8569) for three reasons; this design answers each rather than ignoring them:
//
//  1. "Never used." That picker lived on project settings, where you go after a project already
//     exists. This one lives in Repository access, next to GitHub, where someone is deciding how
//     partyline reaches their code in the first place.
//
//  2. "Provisioning obsoletes it." Provisioned workers (provision.go) clone from a remote, so they
//     do obsolete the GitHub-backed case. They cannot serve a repo with no reachable remote, which
//     is exactly the gap this fills.
//
//  3. "Actively bad UI — candidates came from session cwds, so ~220 throwaway build dirs evicted
//     the real repositories." THAT was the actual defect, and it is the one this file exists to
//     avoid. Candidates are not scraped from session history: the machine scans for real git
//     repositories under the user's home directory, and the two things that flooded the old list —
//     LINKED WORKTREES (every crank task makes one) and MANAGED CLONES — are excluded by
//     construction, not by ranking.
//
// SECURITY POSTURE (reference-not-command). An absolute path NEVER leaves this machine and the
// server can never send one back. Each repo is advertised as an opaque handle (a hash of the local
// path) plus display-only metadata; binding sends that handle back, and the daemon re-derives its
// own list to map handle → path. The server can therefore only ever name a directory this machine
// already offered — it cannot name an arbitrary one.

const (
	// How deep under a root to look. Three levels reaches ~/dev/org/repo without walking a whole
	// disk; deeper mostly finds vendored checkouts and build output.
	localRepoMaxDepth = 3
	// A hard ceiling so a pathological home directory cannot produce an unbounded heartbeat.
	localRepoMax = 200
)

// skipDirNames are directories that never contain a repo worth offering, and that are expensive or
// noisy to walk. Everything beginning with "." is skipped separately.
var skipDirNames = map[string]bool{
	"node_modules": true, "vendor": true, "Library": true, "Applications": true,
	"target": true, "dist": true, "build": true, "venv": true, "__pycache__": true,
	"Pictures": true, "Movies": true, "Music": true,
}

// localRepoKind classifies a directory entry during the scan. Split out from the walk so the
// FILTERING RULE — the thing that broke last time — is a pure function with its own tests, rather
// than a condition buried in a callback nobody can exercise.
type localRepoKind int

const (
	repoNone     localRepoKind = iota // not a repo; keep descending
	repoInclude                       // a real repository; offer it and stop descending
	repoWorktree                      // a linked worktree; skip it entirely and stop descending
)

// classifyDir decides what a directory is, given the name and mode of its `.git` entry (if any).
// gitExists=false means no .git at all. gitIsDir distinguishes a real repository (.git is a
// DIRECTORY) from a linked worktree (.git is a FILE containing a gitdir: pointer) — the exact
// distinction that keeps every crank build dir out of this list.
func classifyDir(gitExists, gitIsDir bool) localRepoKind {
	if !gitExists {
		return repoNone
	}
	if gitIsDir {
		return repoInclude
	}
	return repoWorktree
}

// skipDir reports whether the walk should refuse to descend into a directory by name. Hidden
// directories are skipped wholesale: ~/.cache, ~/.local and friends hold thousands of vendored
// checkouts that are nobody's project.
func skipDir(name string) bool {
	if name == "" {
		return false
	}
	if strings.HasPrefix(name, ".") {
		return true
	}
	return skipDirNames[name]
}

// localRepoHandle is the opaque id the web sees for a directory. It is a hash, so the path itself
// never leaves the machine, and it is stable across scans, so a handle picked in the browser still
// resolves a moment later.
func localRepoHandle(abs string) string {
	sum := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(sum[:])[:16]
}

// displayParent renders a repo's parent directory for the picker, relative to home ("~/dev"). Two
// repos can share a basename, so the parent is what makes the list decidable by a human. It is
// display metadata only — binding never uses it, and the absolute path is not reconstructible from
// it by the server.
func displayParent(abs, home string) string {
	parent := filepath.Dir(abs)
	if home != "" && strings.HasPrefix(parent, home) {
		rel := strings.TrimPrefix(parent, home)
		if rel == "" {
			return "~"
		}
		return "~" + rel
	}
	return filepath.Base(parent)
}

// localRepoScanBudget caps the WALK, not just the result count. The v0.38.0 release build proved
// why: this walk against the mac-mini runner's home took >10 MINUTES (network-backed dirs, giant
// work trees — stat storms the skip rules can't predict), and a machine like that exists among
// users too. This feeds a heartbeat; a partial list delivered now beats a complete one that never
// arrives. The deadline check costs one time.Since per directory.
const localRepoScanBudget = 5 * time.Second

// scanLocalRepoPaths walks a root for real git repositories and returns their absolute paths.
// Best-effort by design: an unreadable subtree is skipped, never fatal — this feeds a heartbeat,
// and a picker missing one entry beats a daemon that fails to report. Takes its roots as
// arguments so the walk itself is testable against a temp tree.
func scanLocalRepoPaths(home, managed string) []string {
	var out []string
	deadline := time.Now().Add(localRepoScanBudget)
	_ = filepath.WalkDir(home, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil //nolint:nilerr // unreadable subtree: skip, never fail the scan
		}
		if len(out) >= localRepoMax || time.Now().After(deadline) {
			return filepath.SkipAll
		}
		if path != home {
			if skipDir(d.Name()) || (managed != "" && strings.HasPrefix(path, managed)) {
				return filepath.SkipDir
			}
			if depthUnder(home, path) > localRepoMaxDepth {
				return filepath.SkipDir
			}
		}
		gi, statErr := os.Stat(filepath.Join(path, ".git"))
		switch classifyDir(statErr == nil, statErr == nil && gi.IsDir()) {
		case repoInclude:
			out = append(out, path)
			return filepath.SkipDir // a repo's subdirectories are its files, not more projects
		case repoWorktree:
			return filepath.SkipDir // a linked worktree — the thing that flooded the old list
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// localRepoHome is the pair of roots the real scan uses: the user's home, and the managed-clone
// directory to exclude. Managed clones are partyline's own working copies, not the user's
// projects — offering them would let someone bind a directory this machine made for itself.
func localRepoHome() (home, managed string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", ""
	}
	return home, filepath.Join(daemonDir(), provisionReposSub)
}

// The heartbeat fires every ~60s and a home-directory walk is far too expensive to repeat that
// often for a list that changes when someone clones a repo — minutes-stale is fine, and a fresh
// clone shows up on the next refresh. The bind path does NOT use this cache: resolving a handle
// always re-scans, so a stale cache can never widen what a handle resolves to.
const localRepoTTL = 10 * time.Minute

var localRepoCache struct {
	mu   sync.Mutex
	at   time.Time
	list []api.LocalRepo
}

// cachedLocalRepos is the heartbeat's view — scanLocalRepos behind a TTL.
func cachedLocalRepos() []api.LocalRepo {
	localRepoCache.mu.Lock()
	defer localRepoCache.mu.Unlock()
	if !localRepoCache.at.IsZero() && time.Since(localRepoCache.at) < localRepoTTL {
		return localRepoCache.list
	}
	localRepoCache.list = scanLocalRepos()
	localRepoCache.at = time.Now()
	return localRepoCache.list
}

// invalidateLocalRepoCache forces the next heartbeat to re-scan — called after a bind, so a
// just-registered repo is reflected without waiting out the TTL.
func invalidateLocalRepoCache() {
	localRepoCache.mu.Lock()
	localRepoCache.at = time.Time{}
	localRepoCache.mu.Unlock()
}

// scanLocalRepos is what the heartbeat advertises: handle + display metadata, never a path.
func scanLocalRepos() []api.LocalRepo {
	home, managed := localRepoHome()
	if home == "" {
		return nil
	}
	paths := scanLocalRepoPaths(home, managed)
	out := make([]api.LocalRepo, 0, len(paths))
	for _, p := range paths {
		out = append(out, api.LocalRepo{
			Handle: localRepoHandle(p),
			Name:   filepath.Base(p),
			Parent: displayParent(p, home),
		})
	}
	return out
}

// depthUnder counts path segments of path below root.
func depthUnder(root, path string) int {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." {
		return 0
	}
	return len(strings.Split(rel, string(filepath.Separator)))
}

// resolveLocalRepoHandle maps a handle the server sent back to an absolute path on THIS machine.
// This is the whole reference-not-command guarantee in one function: the server's string is
// COMPARED against what this machine advertises, never used as a path. An unknown or stale handle
// resolves to "" and the caller refuses the bind, so the server can only ever name a directory the
// machine itself offered.
func resolveLocalRepoHandle(handle string) string {
	home, managed := localRepoHome()
	return resolveLocalRepoHandleIn(home, managed, handle)
}

// resolveLocalRepoHandleIn is the resolution rule with its roots injected — the testable form.
// The v0.38.0 release died on a test calling the wrapper above: it walked the RUNNER's real home
// for 10 minutes. A test that does real filesystem I/O against whatever machine it runs on isn't
// testing the rule, it's testing the machine.
func resolveLocalRepoHandleIn(home, managed, handle string) string {
	handle = strings.TrimSpace(handle)
	if handle == "" || home == "" {
		return ""
	}
	for _, p := range scanLocalRepoPaths(home, managed) {
		if localRepoHandle(p) == handle {
			return p
		}
	}
	return ""
}

// daemonRepos is the human view of exactly what this machine advertises — `ptln daemon repos`.
// It exists so the web picker is never the only way to see this: if a repo you expected is
// missing, you can find out here rather than guessing at a list rendered in a browser.
func daemonRepos() {
	repos := scanLocalRepos()
	if len(repos) == 0 {
		fmt.Println("no local git repositories found under your home directory")
		fmt.Println("(looked ≤3 levels deep, skipping hidden dirs, node_modules, worktrees and managed clones)")
		return
	}
	fmt.Printf("%d local repositor%s this machine offers to the web:\n\n", len(repos), plural(len(repos), "y", "ies"))
	for _, r := range repos {
		fmt.Printf("  %-28s %s\n", r.Name, r.Parent)
	}
	fmt.Println("\nPick one in Settings → Integrations → Repository access → Local directory,")
	fmt.Println("or bind it here: ptln daemon add-project <label> <dir>")
}

// plural picks a suffix — small enough to inline, but it appears in two counts above.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// bindLocalRepo registers a repository the web picked as a project in THIS machine's registry —
// the daemon half of Repository access → Local directory. It is deliberately the same end state as
// `ptln daemon add-project`: one entry in the local registry, mirrored back on the next sync.
//
// The handle is resolved against a FRESH scan (never the heartbeat cache), so a handle only works
// if it still names a repository this machine currently offers. Anything else — unknown handle,
// deleted directory, invalid label — is refused, and the refusal is the safe outcome.
func bindLocalRepo(ev api.BindRepoEvent) error {
	label := strings.TrimSpace(ev.Label)
	if !labelRe.MatchString(label) {
		return fmt.Errorf("invalid label %q", label)
	}
	abs := resolveLocalRepoHandle(ev.Handle)
	if abs == "" {
		return fmt.Errorf("unknown repository — this machine does not offer that directory")
	}
	preset := strings.TrimSpace(ev.Preset)
	if preset != "chat" {
		preset = "spec"
	}
	engine := strings.ToLower(strings.TrimSpace(ev.Engine))
	if engine != "" && !validEngine(engine) {
		engine = ""
	}
	reg := loadDaemonRegistry()
	for i := range reg.Projects {
		if reg.Projects[i].Label == label { // same semantics as add-project: update in place
			reg.Projects[i].Path, reg.Projects[i].Preset, reg.Projects[i].Engine = abs, preset, engine
			if err := saveDaemonRegistry(reg); err != nil {
				return err
			}
			invalidateLocalRepoCache()
			syncIfEnrolled()
			return nil
		}
	}
	reg.Projects = append(reg.Projects, daemonProject{Label: label, Path: abs, Preset: preset, Engine: engine})
	if err := saveDaemonRegistry(reg); err != nil {
		return err
	}
	invalidateLocalRepoCache()
	syncIfEnrolled()
	return nil
}
