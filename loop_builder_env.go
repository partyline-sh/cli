package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// A builder runs like the person's own session: the project's and the person's instructions, their
// engine config, every tool, their MCP servers and credentials (the person chose this on 2026-10-29:
// the restrictions made the work worse than a session's). Two things it cannot do, because they are
// partyline's promises rather than security: it cannot write partyline's state, where approvals and
// rulings live (agents never approve their own work), and it cannot push or merge (partyline
// commits each step for the person; nothing leaves the machine).

// builderEnv is environ for a builder: the person's, less the session identity, with gh logged out
// (an empty config directory: the person's tokens are in the keychain, so dropping GH_TOKEN alone
// would not do) and git told that every push URL is unreachable. Fetching is untouched.
func builderEnv(environ []string, ghConfig string) []string {
	var out []string
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case strings.HasPrefix(k, "PARTYLINE_SESSION"), strings.HasPrefix(k, "TMUX"),
			k == "GH_TOKEN", k == "GITHUB_TOKEN", k == "GH_ENTERPRISE_TOKEN", k == "GITHUB_ENTERPRISE_TOKEN",
			k == "GH_CONFIG_DIR", strings.HasPrefix(k, "GIT_CONFIG_"):
			continue
		}
		out = append(out, kv)
	}
	out = append(out, "GH_CONFIG_DIR="+ghConfig, "GIT_TERMINAL_PROMPT=0",
		"PARTYLINE=1", "PARTYLINE_MEMORY_CAPTURE=off", "PARTYLINE_MEMORY_REFRESH=off")
	// url.<base>.pushInsteadOf rewrites matching push URLs only: every remote form is sent to a
	// scheme git cannot open, so `git push` fails while fetch and pull work.
	prefixes := []string{"https://", "http://", "ssh://", "git@", "git://"}
	out = append(out, fmt.Sprintf("GIT_CONFIG_COUNT=%d", len(prefixes)))
	for i, p := range prefixes {
		out = append(out, fmt.Sprintf("GIT_CONFIG_KEY_%d=url.partyline-builders-do-not-push://.pushInsteadOf", i),
			fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, p))
	}
	return out
}

// builderGHConfig is an empty gh config directory for builders: gh then has no account.
func builderGHConfig() string {
	dir := filepath.Join(os.TempDir(), "partyline-builder-gh")
	_ = os.MkdirAll(dir, 0o700)
	return dir
}
