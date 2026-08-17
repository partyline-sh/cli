package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"partyline.sh/partyline/internal/api"
)

// destinations.go — the machine half of web-driven project provisioning: "put this repo on that
// box." localrepos.go advertises the checkouts this machine ALREADY has; this file advertises the
// PARENT DIRECTORIES it could put a new one in. Executing the assignment that comes back is
// assignproject.go.
//
// SECURITY POSTURE — identical to localrepos.go, and the reason this is a second file rather than
// two more fields in that one:
//
//   - The only two values the server ever supplies are an opaque HANDLE and a git URL. Neither is
//     ever used as a path. The handle is COMPARED against a freshly re-derived candidate list, so
//     it can only ever name a directory this machine itself offered; an unknown or stale handle is
//     rejected outright and nothing is written.
//   - The target directory is built HERE — filepath.Join(<resolved parent>, <label>) — from a label
//     that has already passed labelRe. No component of it comes from the server as a path.
//   - The git URL is validated against an allow-list of remote forms before it can reach argv, and
//     is passed after `--` so it can never be read as a flag.
//   - Nothing is ever overwritten. An occupied destination is a REFUSAL, not a merge.
//   - Auth is the machine's OWN git (ssh key / gh auth / credential helper). No credential travels
//     from the server for this path, so "it can't reach the remote" is a fact about this machine
//     and the reported reason says exactly that.
//
// The two handle namespaces (local_repos vs destinations) are deliberately disjoint: merging them
// would let the web ask a machine to clone on top of an existing working tree.

// destinationMax bounds the advertised list. Every candidate is a real directory a human has to
// choose between, and the list rides a heartbeat.
const destinationMax = 24

// defaultWorkspacePath is the daemon's OWN workspace — the always-present fallback destination, so
// a freshly enrolled machine with no repos and no scan roots never shows an empty picker. It is
// created LAZILY: advertised whether or not it exists, made only when an assignment actually lands
// there, so merely running the daemon never litters a home directory.
func defaultWorkspacePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "partyline")
}

// dirWritable reports whether this process could create an entry in dir. Access(2) asks the kernel
// instead of guessing from mode bits + uid (which gets ACLs, group membership and root wrong), and
// unlike a probe file it writes NOTHING — advertising must never touch the user's disk.
func dirWritable(dir string) bool {
	if dir == "" {
		return false
	}
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return false
	}
	return syscall.Access(dir, 0x2) == nil // W_OK
}

// destinationPaths derives the candidate parent directories, given an already-scanned repo list.
// Pure apart from the writability stat, and roots/workspace are injected — the whole rule is
// testable against a temp tree instead of whatever machine the tests run on.
//
// The candidates are, in order: the daemon's default workspace (ALWAYS, existing or not), every
// registered scan root that is not $HOME, and the parent directory of every repo already found
// (that is where this user actually keeps code, so it is where they will want the next one).
// $HOME itself is deliberately NOT offered — cloning into the top of a home directory is the
// mistake `ptln setup` already warns about, and every useful subdirectory of it is covered here.
func destinationPaths(roots []string, home, workspace string, repoPaths []string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string, requireWritable bool) {
		p = filepath.Clean(p)
		if p == "" || p == "." || seen[p] {
			return
		}
		if home != "" && p == home {
			return
		}
		if requireWritable && !dirWritable(p) {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	if workspace != "" {
		add(workspace, false) // the fallback: advertised before it exists
	}
	for _, r := range roots {
		if r != home {
			add(r, true)
		}
	}
	parents := make([]string, 0, len(repoPaths))
	for _, p := range repoPaths {
		parents = append(parents, filepath.Dir(p))
	}
	sort.Strings(parents)
	for _, p := range parents {
		if len(out) >= destinationMax {
			break
		}
		add(p, true)
	}
	return out
}

// displayDir renders a directory for the picker: "~/dev" under home, otherwise relative to the
// scan root it came from ("data/projects"). Display metadata only — an assignment never carries it
// back, and the absolute path is not reconstructible from it by the server.
func displayDir(dir, home string, roots []string) string {
	if home != "" && (dir == home || strings.HasPrefix(dir, home+string(filepath.Separator))) {
		rel := strings.TrimPrefix(dir, home)
		if rel == "" {
			return "~"
		}
		return "~" + rel
	}
	for _, r := range roots {
		if r == home || r == "" {
			continue
		}
		if dir == r || strings.HasPrefix(dir, r+string(filepath.Separator)) {
			if rel, err := filepath.Rel(filepath.Dir(r), dir); err == nil {
				return rel
			}
		}
	}
	return filepath.Base(dir)
}

// destinationsFrom turns candidate paths into the advertised records: handle + display only.
func destinationsFrom(paths []string, home, workspace string, roots []string) []api.Destination {
	out := make([]api.Destination, 0, len(paths))
	for _, p := range paths {
		label := "existing code lives here"
		switch {
		case p == workspace:
			label = "default workspace"
		case isRoot(p, roots):
			label = "scan root"
		}
		out = append(out, api.Destination{
			Handle: localRepoHandle(p), // same hash as a repo handle, but a SEPARATE namespace (see api.DaemonConfig)
			Parent: displayDir(p, home, roots),
			Label:  label,
		})
	}
	return out
}

func isRoot(p string, roots []string) bool {
	for _, r := range roots {
		if r == p {
			return true
		}
	}
	return false
}

// destinationsFromScan renders one walk as the destination list — the twin of localReposFrom, so a
// heartbeat derives BOTH advertised lists from a single scan instead of walking the disk twice.
func destinationsFromScan(s repoScan) []api.Destination {
	ws := defaultWorkspacePath()
	return destinationsFrom(destinationPaths(s.roots, s.home, ws, s.paths), s.home, ws, s.roots)
}

// cachedDestinations is the heartbeat's view: the SAME cached walk cachedLocalRepos renders, so the
// two lists are always consistent with each other and cost one scan between them. The ASSIGNMENT
// path never reads this cache (resolveDestinationHandle re-derives), so a stale cache can never
// widen what a handle resolves to.
func cachedDestinations() []api.Destination { return destinationsFromScan(cachedRepoScan()) }

// scanDestinations is a FRESH list — what `ptln daemon destinations` and `ptln settings` show.
func scanDestinations() []api.Destination { return destinationsFromScan(scanRepoTree()) }

// resolveDestinationHandle maps a destination handle the server sent back to an absolute path on
// THIS machine, re-deriving the candidate list first. "" means "we do not offer that" — unknown
// handle, stale handle, or a directory that has since gone away — and the caller REFUSES.
func resolveDestinationHandle(handle string) string {
	return resolveDestinationHandleIn(scanDestinationPaths(), handle)
}

// scanDestinationPaths is the handles' PREIMAGES: the same candidate rule, kept at path level so
// resolution never has to reconstruct a path from anything the server sent.
func scanDestinationPaths() []string {
	s := scanRepoTree()
	return destinationPaths(s.roots, s.home, defaultWorkspacePath(), s.paths)
}

// resolveDestinationHandleIn is the matching rule with its candidate list injected — the testable
// form, and the one place the reference-not-command guarantee is enforced for destinations.
func resolveDestinationHandleIn(paths []string, handle string) string {
	handle = strings.TrimSpace(handle)
	if handle == "" {
		return ""
	}
	for _, p := range paths {
		if localRepoHandle(p) == handle {
			return p
		}
	}
	return ""
}

// daemonDestinations is the human view of exactly what this machine offers to clone into —
// the `ptln daemon repos` companion, so "why can't I pick that directory?" is answerable here
// rather than by staring at a browser.
func daemonDestinations() {
	dests := scanDestinations()
	fmt.Printf("%d director%s this machine will clone a project into:\n\n", len(dests), plural(len(dests), "y", "ies"))
	for _, dst := range dests {
		fmt.Printf("  %-28s %s\n", dst.Parent, dst.Label)
	}
	fmt.Println("\nAdd more by registering where your code lives: ptln daemon scan-root add /mnt/data")
}
