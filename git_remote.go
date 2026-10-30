package main

// Canonicalising a git remote.
//
// A repo cloned through an ssh host alias ("github-acr:org/repo") and the same repo cloned by its
// real host are the same repository, and anything that identifies a repo by its remote has to
// agree about that — otherwise one checkout resolves to a project and its twin does not.
//
// This lived beside the context-threads MCP server, which is gone; it is kept because identifying
// a repo by remote is still how a legacy side-repo project finds its members.

import (
	"os/exec"
	"regexp"
	"strings"
)

var (
	// scp-style only when there is no scheme: "https://host/path" would otherwise parse as host
	// "https" with the rest as its path.
	aliasScpRe    = regexp.MustCompile(`^([A-Za-z0-9._-]+@)?([^:/]+):(.+)$`)
	aliasSSHURLRe = regexp.MustCompile(`^(ssh://(?:[^@/]+@)?)([^:/]+)((?::\d+)?/.+)$`)
)

// canonicalRemote rewrites an ssh host ALIAS to the real host it resolves to, leaving every other
// form untouched.
func canonicalRemote(raw string, resolve func(host string) string) string {
	host, rewrite := "", func(string) string { return raw }
	if m := aliasScpRe.FindStringSubmatch(raw); m != nil && !strings.Contains(raw, "://") {
		host = m[2]
		rewrite = func(real string) string { return m[1] + real + ":" + m[3] }
	} else if m := aliasSSHURLRe.FindStringSubmatch(raw); m != nil {
		host = m[2]
		rewrite = func(real string) string { return m[1] + real + m[3] }
	}
	// Only a host that cannot be a real one (no dot) is worth an ssh lookup; github.com is
	// already an identity, and rewriting hosts that work would be pure risk.
	if host == "" || strings.Contains(host, ".") || resolve == nil {
		return raw
	}
	real := strings.TrimSpace(resolve(host))
	if real == "" || real == host || !strings.Contains(real, ".") {
		return raw
	}
	return rewrite(real)
}

// sshResolvedHost asks ssh what a host alias actually resolves to.
func sshResolvedHost(host string) string {
	out, err := exec.Command("ssh", "-G", host).Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(line, "hostname "); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// gitOriginURL is a repo's canonical origin, or "" when it has none.
func gitOriginURL(dir string) string {
	out, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	return canonicalRemote(strings.TrimSpace(string(out)), sshResolvedHost)
}
