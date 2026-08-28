package main

// repoidentity.go — deciding whether two git remote strings name the SAME repository.
//
// A port of web/src/lib/api/repo-identity.ts, deliberately line-for-line with it. The server owns
// this question for /threads/resolve, and that stays true: create_project asks the server FIRST
// (ResolveRepo) and only falls back to this when the answer was "no match", to decide whether a
// project it found under the same LABEL is this repo or somebody else's.
//
// It is needed because those two strings are routinely different spellings of one repo:
//
//	git@github.com:partyline-sh/partyline.git
//	https://github.com/partyline-sh/partyline.git
//	https://github.com/partyline-sh/partyline
//	ssh://git@github.com/partyline-sh/partyline.git
//
// A literal comparison fails on every pair above, and the failure here is the one the reviewer
// caught: a project that IS this repo, refused as "a different repo", quoting the repo's own remote
// back at the user. Kept in step with the TS by mirroring its test cases (repo-identity.test.ts).

import (
	"regexp"
	"strings"
)

var (
	// scp-style — git@host:owner/name.git — which is not a URL and so parses as nothing useful.
	scpRemoteRe    = regexp.MustCompile(`^[A-Za-z0-9._-]+@([^:/]+):(.+)$`)
	remoteSchemeRe = regexp.MustCompile(`(?i)^[a-z+]+://`)
	remoteUserRe   = regexp.MustCompile(`^[^@/]+(:[^@/]*)?@`)
	remoteGitRe    = regexp.MustCompile(`(?i)\.git$`)
	remotePortRe   = regexp.MustCompile(`^([^/]+):\d+`)
)

// normalizeRemote reduces a git remote URL to `host/owner/name`, lowercased.
//
// Returns "" for anything unusable, which callers must treat as "no match" rather than as a
// wildcard — an empty normalization matching an empty stored value would link a repo to whichever
// project happened to have a blank repo_url.
func normalizeRemote(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if m := scpRemoteRe.FindStringSubmatch(s); m != nil {
		s = m[1] + "/" + m[2]
	} else {
		s = remoteSchemeRe.ReplaceAllString(s, "") // ssh:// https:// git:// git+ssh://
		s = remoteUserRe.ReplaceAllString(s, "")   // strip any userinfo left over
	}

	s = remoteGitRe.ReplaceAllString(s, "")
	s = strings.TrimRight(s, "/")
	// A port is a transport detail, not part of the repo's identity: the same repo reached on :22 and
	// on :2222 is one repo.
	s = remotePortRe.ReplaceAllString(s, "$1")
	s = strings.ToLower(s)

	// Require host/something, where the host actually looks like a host.
	//
	// Containing a "/" is not enough: `./local/path` and `../sibling/repo` are valid git remotes but
	// NOT stable shared identities — they mean different repositories on different machines, which is
	// the one thing this function exists to get right. A bare word is rejected for the same reason a
	// typo must not silently match.
	host, rest, ok := strings.Cut(s, "/")
	if !ok || rest == "" {
		return ""
	}
	if !strings.Contains(host, ".") || strings.HasPrefix(host, ".") {
		return ""
	}
	return s
}

// sameRemote is normalizeRemote's whole point, named for what callers actually ask. Two remotes
// neither of which normalizes are never "the same": unusable is not a match.
func sameRemote(a, b string) bool {
	na := normalizeRemote(a)
	return na != "" && na == normalizeRemote(b)
}
