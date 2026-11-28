package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// citations.go — dispatch-time citation verification. Task descriptions rot: in a repo shipping
// many PRs a day, a two-week-old task citing `web/src/app/fleet/page.tsx:44` is archaeology — the
// file may be gone, moved, or rewritten. The graded reviews showed both failure modes: a reviewer
// docking an otherwise-A run for acting on a stale route, and a spec contradicting its own ticket's
// citations. This is the cheap pre-flight: extract path-shaped citations from the task, check them
// against the EXACT tree the worker is about to see (its freshly forked worktree), and put any
// misses in front of the worker — locate the current equivalent, don't recreate removed files.

// citedPathRe matches repo-relative path citations with a code/document extension, optionally with
// a :line suffix. Requires a "/" so bare filenames ("main.go") don't false-positive on prose.
// NOTE: Go regexp alternation is leftmost (not longest) — longer extensions MUST precede their
// prefixes (tsx before ts, jsx before js, mdx before md, scss before css) or "app.tsx" matches as
// "app.ts".
var citedPathRe = regexp.MustCompile(`[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.\[\]()-]+)+\.(?:go|tsx|ts|jsx|js|mjs|py|rb|rs|sql|mdx|md|scss|css|json|yaml|yml|toml|sh|swift|kt|java|cpp|c|h)(?::\d+)?`)

const maxCitations = 24 // a task citing more than this is a spec dump; checking a sample is enough

// citedPaths extracts the distinct repo-relative paths a task cites (":line" stripped), in order.
func citedPaths(task string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range citedPathRe.FindAllString(task, -1) {
		p := m
		if i := strings.LastIndex(p, ":"); i > 0 {
			p = p[:i]
		}
		p = strings.TrimPrefix(p, "./")
		if p == "" || seen[p] || strings.Contains(p, "..") {
			continue
		}
		seen[p] = true
		out = append(out, p)
		if len(out) >= maxCitations {
			break
		}
	}
	return out
}

// staleCitations returns the task's cited paths that do NOT exist under root — evidence the task
// text has drifted behind the code it describes.
func staleCitations(root, task string) []string {
	var stale []string
	for _, p := range citedPaths(task) {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(p))); err != nil {
			stale = append(stale, p)
		}
	}
	return stale
}

// staleCitationNote renders the worker-prompt warning for stale citations ("" when none). Prepended
// to the prompt — the task text itself stays pristine (it still names the branch/PR).
func staleCitationNote(stale []string) string {
	if len(stale) == 0 {
		return ""
	}
	return "⚠ STALE CITATIONS — this task references paths that do NOT exist in the current code:\n  " +
		strings.Join(stale, "\n  ") +
		"\nThe repo has moved on since the task was written. Locate the current equivalents first (search for the symbols/features named), do NOT recreate removed files, and note any citation you had to re-map in your summary.\n\n"
}
