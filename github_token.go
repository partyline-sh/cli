package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// github_token.go — the daemon's LOCAL GitHub token for opening PRs (merge_policy pr/auto).
//
// WHY LOCAL, never central: the daemon runs on the operator's own machine, which already pushes via
// their SSH key. But `gh pr create` needs an API token, and a background service does NOT inherit the
// login shell's env (direnv, keyring selection) — so gh silently falls back to the wrong/absent
// account and PR creation dies with "Could not resolve to a Repository." partyline must NOT store
// repo-write tokens in the control plane (a breach honeypot, and it contradicts the self-hosted
// "your machine, your keys" model). So the operator stashes a token on THEIR machine once, and the
// daemon uses it — nothing GitHub-write-scoped ever leaves the box.

func githubTokenPath() string { return filepath.Join(stateDir(), "github-token") }

// readStoredGitHubToken returns the operator-set token, or "" if none. Trimmed — a trailing newline
// in a token is a classic silent auth failure.
func readStoredGitHubToken() string {
	b, err := os.ReadFile(githubTokenPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// resolveGitHubToken finds a usable token for gh, most-authoritative first: the operator's explicitly
// stored token wins (they chose it for THIS machine), then the ambient env, then whatever `gh auth
// token` yields (the login shell's active account, which a service may not share). "" means nothing
// found — the caller surfaces a setup hint instead of letting gh fail opaquely.
func resolveGitHubToken() string {
	if t := readStoredGitHubToken(); t != "" {
		return t
	}
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if t := strings.TrimSpace(os.Getenv(k)); t != "" {
			return t
		}
	}
	if out, err := exec.Command("gh", "auth", "token").Output(); err == nil {
		if t := strings.TrimSpace(string(out)); t != "" {
			return t
		}
	}
	return ""
}

// daemonSetGitHubToken stores a GitHub token locally (0600) for PR creation. The token is read from
// STDIN so it never lands in shell history or argv — pipe it (`gh auth token | ptln daemon
// set-github-token`) or paste it and press Ctrl-D. `--from-gh` copies the current `gh auth token`
// directly. `--clear` removes the stored token (falls back to env / gh).
func daemonSetGitHubToken(args []string) {
	if len(args) > 0 && args[0] == "--clear" {
		if err := os.Remove(githubTokenPath()); err != nil && !os.IsNotExist(err) {
			fatal(fmt.Errorf("clear github token: %w", err))
		}
		fmt.Println("✓ cleared the stored GitHub token — the daemon will fall back to the env / `gh auth token`.")
		return
	}

	var tok string
	if len(args) > 0 && args[0] == "--from-gh" {
		out, err := exec.Command("gh", "auth", "token").Output()
		if err != nil {
			fatal(fmt.Errorf("`gh auth token` failed — run `gh auth login` first: %w", err))
		}
		tok = strings.TrimSpace(string(out))
	} else {
		b, _ := io.ReadAll(os.Stdin)
		tok = strings.TrimSpace(string(b))
	}
	if tok == "" {
		fatal(fmt.Errorf("no token provided. Pipe one in: `gh auth token | ptln daemon set-github-token`, or use `--from-gh`"))
	}

	if err := os.MkdirAll(stateDir(), 0o700); err != nil {
		fatal(fmt.Errorf("state dir: %w", err))
	}
	if err := os.WriteFile(githubTokenPath(), []byte(tok+"\n"), 0o600); err != nil {
		fatal(fmt.Errorf("store github token: %w", err))
	}
	fmt.Printf("✓ Stored a GitHub token for PR creation (%s, 0600). The daemon uses it for `gh pr create`.\n", githubTokenPath())
}
