package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoSourceFileIsGitIgnored — the ratchet for a failure mode that has now cost three shipped
// features and one broken release.
//
// .gitignore carried bare artifact names — `partyline`, `ptln-tray`, `partyline-relay` — each meant
// for a compiled binary at the repo root. Without a leading slash, a gitignore pattern matches EVERY
// path segment of that name anywhere in the tree, including DIRECTORIES. So:
//
//	partyline   swallowed  web/src/app/.well-known/partyline/   (the instance identity endpoint)
//	            and        deploy/stack/keycloak-theme/partyline/ (the login theme)
//	ptln-tray   swallowed  cmd/ptln-tray/daemon_line.go          (the tray's connection light)
//
// Every one was written, built, tested locally and merged in a PR that did not contain it.
//
// WHY IT KEPT GETTING THROUGH. `git add -A` reports nothing for an ignored file. Files already
// tracked in those directories kept working, because gitignore does not apply to tracked paths — so
// the directory looked fine and only NEW files vanished. The local build passed because the file was
// on disk. CI passed because it never saw a reference to something that did not exist. The first
// signal was a 404 in production, and later a release failing on `undefined: daemonLine`.
//
// A comment on the .gitignore line is not enough; the previous fix carried one and the next bare
// pattern bit anyway. This makes it a test failure at the moment the file is created.
func TestNoSourceFileIsGitIgnored(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Skipf("not a git checkout: %v", err)
	}

	// Every file git would consider ignored, one per line, NUL-terminated so paths with spaces or
	// newlines survive. --others --ignored lists untracked-and-ignored; --directory collapses a
	// fully ignored directory to its name, which is exactly the shape that hid cmd/ptln-tray/.
	out, err := exec.Command("git", "-C", root,
		"ls-files", "--others", "--ignored", "--exclude-standard", "-z").Output()
	if err != nil {
		t.Skipf("git ls-files unavailable: %v", err)
	}

	var bad []string
	for _, p := range strings.Split(string(out), "\x00") {
		p = strings.TrimSpace(p)
		if p == "" || !isSourcePath(p) {
			continue
		}
		bad = append(bad, p)
	}
	if len(bad) > 0 {
		t.Fatalf("these source files are git-ignored and would never leave this machine:\n  %s\n\n"+
			"Almost always a bare artifact name in .gitignore matching a DIRECTORY: anchor it with a\n"+
			"leading slash (`/ptln-tray`, not `ptln-tray`) so it only matches the built binary at the\n"+
			"repo root. Check with: git check-ignore -v <path>",
			strings.Join(bad, "\n  "))
	}
}

// isSourcePath reports whether an ignored path is something that should have been committed.
//
// Deliberately narrow: build output, dependencies and caches are ignored ON PURPOSE and listing them
// would make this test noise. It asks only about hand-written source in directories this repo owns.
func isSourcePath(p string) bool {
	// Vendored, generated, or genuinely-ignored trees. node_modules holds .go files in the wild and
	// .next holds generated .ts — neither is ours.
	for _, skip := range []string{"node_modules/", ".next/", "dist/", "vendor/", ".git/", "coverage/", "playwright-report/", "test-results/"} {
		if strings.Contains(p, skip) {
			return false
		}
	}
	// Generated files that are ignored ON PURPOSE, named individually rather than by a loose
	// pattern: an exception list you have to add to deliberately keeps the guard honest, whereas
	// skipping all of `*.d.ts` would quietly re-open the hole for hand-written declarations.
	for _, generated := range []string{"web/next-env.d.ts"} {
		if p == generated {
			return false
		}
	}
	switch filepath.Ext(p) {
	case ".go", ".ts", ".tsx", ".sql", ".properties":
		return true
	case ".css":
		// The Keycloak login theme is CSS shipped inside the embedded stack, and it was one of the
		// files this pattern ate. Scoped to deploy/ so a stray ignored stylesheet elsewhere (a build
		// artifact) does not fail the suite.
		return strings.HasPrefix(p, "deploy/")
	}
	return false
}

func repoRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
