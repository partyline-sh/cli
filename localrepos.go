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
	return scanLocalRepoPathsUntil(home, managed, time.Now().Add(localRepoScanBudget))
}

// scanLocalRepoPathsUntil is the deadline-injected form, so a MULTI-root scan (home + scan roots)
// shares ONE budget instead of stacking a full budget per root — the heartbeat must never wait
// roots×budget on a machine with several pathological mounts.
func scanLocalRepoPathsUntil(home, managed string, deadline time.Time) []string {
	var out []string
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

// localRepoScanRootsMax bounds how many extra roots an owner can register — a sanity cap, since
// every root shares one scan budget and one heartbeat.
const localRepoScanRootsMax = 8

// localRepoRoots is the FULL root list the scan advertises from: $HOME plus the owner's registered
// scan roots (`ptln daemon scan-root add`), for machines whose repos live on a data mount rather
// than under home. Registry entries that no longer exist are silently skipped — a detached mount
// must not error every heartbeat. Roots nested under another root are dropped (the outer walk
// would double-offer them).
func localRepoRoots() (roots []string, managed string) {
	home, managed := localRepoHome()
	if home != "" {
		roots = append(roots, home)
	}
	for _, r := range loadDaemonRegistry().ScanRoots {
		r = filepath.Clean(strings.TrimSpace(r))
		if !filepath.IsAbs(r) {
			continue
		}
		if fi, err := os.Stat(r); err != nil || !fi.IsDir() {
			continue
		}
		nested := false
		for _, have := range roots {
			if r == have || strings.HasPrefix(r, have+string(filepath.Separator)) {
				nested = true
				break
			}
		}
		if !nested && len(roots) < 1+localRepoScanRootsMax {
			roots = append(roots, r)
		}
	}
	return roots, managed
}

// scanAllRepoPaths runs the walk over every root under ONE shared deadline and returns
// path → the root it was found under (the root is needed for display). Deduped by path.
//
// HARD abandon, not just the cooperative deadline: the per-directory time check inside the walk
// only runs BETWEEN stats, and a single stat on a dead network mount can block for minutes —
// which is exactly how `ptln login` hung on the mac-mini (the same machine whose home walk once
// ran >10 minutes and killed two release builds). The walk runs in a goroutine that owns all its
// state and delivers the finished map over a channel; if it overruns the budget plus grace, we
// return empty and let it leak until the kernel frees the stuck stat. An empty offer list for one
// heartbeat beats a login nobody can finish.
func scanAllRepoPaths(roots []string, managed string) map[string]string {
	deadline := time.Now().Add(localRepoScanBudget)
	ch := make(chan map[string]string, 1) // buffered: a late finisher must not block forever
	go func() {
		found := map[string]string{}
		for _, root := range roots {
			for _, p := range scanLocalRepoPathsUntil(root, managed, deadline) {
				if _, dup := found[p]; !dup {
					found[p] = root
				}
			}
		}
		ch <- found
	}()
	select {
	case found := <-ch:
		return found
	case <-time.After(localRepoScanBudget + 2*time.Second):
		return map[string]string{}
	}
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
	roots, managed := localRepoRoots()
	if len(roots) == 0 {
		return nil
	}
	home := roots[0]
	byPath := scanAllRepoPaths(roots, managed)
	paths := make([]string, 0, len(byPath))
	for p := range byPath {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := make([]api.LocalRepo, 0, len(paths))
	for _, p := range paths {
		parent := displayParent(p, home)
		if root := byPath[p]; root != home {
			// A scan-root repo: render the parent relative to the mount's OWN parent ("data",
			// "data/org") — decidable by the human who added the root, still never an absolute
			// path on the wire.
			if rel, err := filepath.Rel(filepath.Dir(root), filepath.Dir(p)); err == nil && rel != "." {
				parent = rel
			}
		}
		out = append(out, api.LocalRepo{
			Handle: localRepoHandle(p),
			Name:   filepath.Base(p),
			Parent: parent,
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
	roots, managed := localRepoRoots()
	return resolveLocalRepoHandleIn(roots, managed, handle)
}

// resolveLocalRepoHandleIn is the resolution rule with its roots injected — the testable form.
// The v0.38.0 release died on a test calling the wrapper above: it walked the RUNNER's real home
// for 10 minutes. A test that does real filesystem I/O against whatever machine it runs on isn't
// testing the rule, it's testing the machine.
func resolveLocalRepoHandleIn(roots []string, managed, handle string) string {
	handle = strings.TrimSpace(handle)
	if handle == "" || len(roots) == 0 {
		return ""
	}
	for p := range scanAllRepoPaths(roots, managed) {
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
		roots, _ := localRepoRoots()
		fmt.Println("no local git repositories found under your home directory")
		fmt.Println("(looked ≤3 levels deep, skipping hidden dirs, node_modules, worktrees and managed clones)")
		if len(roots) <= 1 {
			fmt.Println("\nRepos on a data mount? Advertise extra directories:")
			fmt.Println("  ptln daemon scan-root add /mnt/data")
		}
		return
	}
	fmt.Printf("%d local repositor%s this machine offers to the web:\n\n", len(repos), plural(len(repos), "y", "ies"))
	for _, r := range repos {
		fmt.Printf("  %-28s %s\n", r.Name, r.Parent)
	}
	fmt.Println("\nPick one in Settings → Integrations → Repository access → Local directory,")
	fmt.Println("or bind it here: ptln daemon add-project <label> <dir>")
	if extra := loadDaemonRegistry().ScanRoots; len(extra) > 0 {
		fmt.Printf("(also scanning: %s)\n", strings.Join(extra, ", "))
	}
}

// addScanRoot validates and registers one extra advertised directory, returning the cleaned path
// and how many repos a scan of it finds right now. Shared by the `scan-root add` verb and the
// `ptln setup` code-locations question — one rule, two doors. Registering a root already covered
// is a no-op success (n counts what's there), not an error: setup re-runs must stay idempotent.
func addScanRoot(dir string) (abs string, repoCount int, err error) {
	abs, err = filepath.Abs(dir)
	if err != nil {
		return "", 0, err
	}
	abs = filepath.Clean(abs)
	if fi, statErr := os.Stat(abs); statErr != nil || !fi.IsDir() {
		return "", 0, fmt.Errorf("%s is not a directory", abs)
	}
	n := len(scanLocalRepoPaths(abs, ""))
	if home, _ := localRepoHome(); home != "" && (abs == home || strings.HasPrefix(abs, home+string(filepath.Separator))) {
		return abs, n, nil // under home — already scanned, nothing to register
	}
	reg := loadDaemonRegistry()
	for _, r := range reg.ScanRoots {
		if r == abs {
			return abs, n, nil // already registered
		}
	}
	if len(reg.ScanRoots) >= localRepoScanRootsMax {
		return "", 0, fmt.Errorf("scan-root limit reached (%d) — remove one first (`ptln daemon scan-root ls`)", localRepoScanRootsMax)
	}
	reg.ScanRoots = append(reg.ScanRoots, abs)
	if err := saveDaemonRegistry(reg); err != nil {
		return "", 0, err
	}
	invalidateLocalRepoCache()
	return abs, n, nil
}

// daemonScanRoot manages the extra advertised directories: `ptln daemon scan-root add|rm|ls`.
// Owner-typed local config, exactly like add-project — the web can never add a root.
func daemonScanRoot(args []string) {
	reg := loadDaemonRegistry()
	verb := ""
	if len(args) > 0 {
		verb = args[0]
	}
	switch verb {
	case "add":
		if len(args) < 2 {
			fatal(fmt.Errorf("usage: ptln daemon scan-root add <dir>"))
		}
		abs, n, err := addScanRoot(args[1])
		if err != nil {
			fatal(err)
		}
		fmt.Printf("✓ scanning %s — %d repositor%s found there now\n", abs, n, plural(n, "y", "ies"))
		fmt.Println("  the running daemon advertises them on its next heartbeat (≤1 min after restart);")
		fmt.Println("  `ptln daemon restart` to pick the change up immediately")
	case "rm", "remove":
		if len(args) < 2 {
			fatal(fmt.Errorf("usage: ptln daemon scan-root rm <dir>"))
		}
		abs, _ := filepath.Abs(args[1])
		abs = filepath.Clean(abs)
		kept := reg.ScanRoots[:0]
		removed := false
		for _, r := range reg.ScanRoots {
			if r == abs {
				removed = true
				continue
			}
			kept = append(kept, r)
		}
		if !removed {
			fatal(fmt.Errorf("%s is not a scan root (see `ptln daemon scan-root ls`)", abs))
		}
		reg.ScanRoots = kept
		if err := saveDaemonRegistry(reg); err != nil {
			fatal(err)
		}
		invalidateLocalRepoCache()
		fmt.Printf("✓ no longer scanning %s (already-bound projects there are untouched)\n", abs)
	case "ls", "list", "":
		if len(reg.ScanRoots) == 0 {
			fmt.Println("no extra scan roots — only your home directory is scanned")
			fmt.Println("add one: ptln daemon scan-root add /mnt/data")
			return
		}
		fmt.Println("advertising repositories from your home directory, plus:")
		for _, r := range reg.ScanRoots {
			fmt.Printf("  %s\n", r)
		}
	default:
		fatal(fmt.Errorf("usage: ptln daemon scan-root add|rm|ls [dir]"))
	}
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
