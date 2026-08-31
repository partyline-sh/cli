// G.4 — a check has a NAME, and a name can carry policy.
//
// `.partyline/verify` is one shell command per line, and every line is blocking, always-run, and
// anonymous. That flattening costs real accuracy in both directions:
//
//   - A check that cannot pass yet cannot be listed. partyline's own `npm run lint` has 38
//     pre-existing errors, so adding it to the gate would reject every clean diff forever — and
//     leaving it out means new lint errors are never caught either.
//   - A Go-only change pays for a full `next build`, and a web-only change pays for `go test`,
//     because neither can say which paths it cares about.
//
// Naming a check is what lets policy attach to it. The policy itself lives in the control plane
// (project_checks) so it can be changed from a settings page; the COMMAND stays here, in the repo,
// because a command supplied by the server would be remote code execution on every machine in the
// fleet — the same line drawn for visual verify and for daemon updates.
//
// The file stays backwards compatible: a bare command line is still a valid check. It gets a name
// derived from its command and the old policy (blocking, always).
package main

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// checkSpec is one acceptance check as the REPO declares it.
type checkSpec struct {
	Name string // stable identifier the control plane attaches policy to
	Cmd  string // the shell command — repo-authored, never server-supplied
	// Named reports whether the repo gave this check a name. An auto-named check cannot be
	// addressed by policy with any confidence, because its name changes when its command changes.
	Named bool
}

// A name is a short identifier, not a sentence: it becomes a settings-page label and a policy key.
var checkNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// parseChecks reads `.partyline/verify` into named checks.
//
// Two forms, and the ambiguity between them is why the name pattern is strict:
//
//	build: npm --prefix web run build     → named "build"
//	npm --prefix web run build            → auto-named from the command
//
// A colon appears in plenty of commands (`sh -c "a: b"`, a URL, a Windows path), so a line only
// counts as named when everything before the FIRST colon matches checkNameRe. That way an existing
// unnamed command containing a colon keeps working rather than silently becoming a check called
// "https".
func parseChecks(body string) []checkSpec {
	var out []checkSpec
	for _, ln := range strings.Split(body, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		if name, cmd, ok := strings.Cut(ln, ":"); ok {
			n := strings.TrimSpace(name)
			c := strings.TrimSpace(cmd)
			if c != "" && checkNameRe.MatchString(n) {
				out = append(out, checkSpec{Name: n, Cmd: c, Named: true})
				continue
			}
		}
		out = append(out, checkSpec{Name: autoCheckName(ln, out), Cmd: ln})
	}
	return out
}

// autoCheckName derives a stable-ish label for an unnamed command so the report has something to
// show. Deliberately NOT a policy key: it changes when the command changes, which is exactly what
// you do not want a settings toggle keyed on — hence checkSpec.Named.
func autoCheckName(cmd string, existing []checkSpec) string {
	fields := strings.Fields(cmd)
	base := "check"
	for _, f := range fields {
		// Skip the runner and its flags to reach the verb: `npm --prefix web run build` → "build".
		if strings.HasPrefix(f, "-") {
			continue
		}
		base = filepath.Base(f)
	}
	base = strings.ToLower(strings.Trim(base, "\"'"))
	if !checkNameRe.MatchString(base) {
		base = "check"
	}
	name, n := base, 2
	for taken(existing, name) {
		name = base + "-" + strconv.Itoa(n)
		n++
	}
	return name
}

func taken(specs []checkSpec, name string) bool {
	for _, s := range specs {
		if s.Name == name {
			return true
		}
	}
	return false
}

// checkPolicy is what the CONTROL PLANE says about a named check. Nothing here is a command; a
// server-supplied command would be remote code execution on every machine in the fleet.
type checkPolicy struct {
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	Blocking bool   `json:"blocking"`
	// PathGlob limits the check to tasks that touched matching files. Empty = always run.
	PathGlob string `json:"path_glob,omitempty"`
}

// applyPolicy merges repo-declared checks with control-plane policy.
//
// The resolution rules, and the reasoning for each:
//
//   - A policy naming a check the repo does not have is IGNORED. The repo is authoritative about
//     what exists; a stale settings row must not invent a check.
//   - A repo check with no policy runs BLOCKING and ALWAYS — the pre-G.4 behaviour, so a project
//     that never opens the settings page sees no change at all.
//   - Policy only ever reaches a NAMED check. An auto-named check's name moves when its command
//     changes, so honouring policy against it would silently apply someone's old decision to a
//     different command.
func applyPolicy(specs []checkSpec, policies []checkPolicy, changed []string) []resolvedCheck {
	byName := map[string]checkPolicy{}
	for _, p := range policies {
		byName[p.Name] = p
	}
	var out []resolvedCheck
	for _, s := range specs {
		r := resolvedCheck{checkSpec: s, Blocking: true}
		p, ok := byName[s.Name]
		if ok && s.Named {
			if !p.Enabled {
				continue // switched off in settings
			}
			r.Blocking = p.Blocking
			if p.PathGlob != "" && !touches(changed, p.PathGlob) {
				r.Skipped = "no changed file matches " + p.PathGlob
			}
		}
		out = append(out, r)
	}
	return out
}

// resolvedCheck is a check with its policy applied — what the gate actually runs.
type resolvedCheck struct {
	checkSpec
	Blocking bool
	// Skipped, when non-empty, means the check did not apply to this task's changes. Recorded
	// rather than dropped: "we did not run it" is a fact the report should carry, not a silence.
	Skipped string
}

// touches reports whether any changed path matches the glob. A task with NO known changed files
// runs everything — an unknown change set is not evidence that a check is irrelevant.
func touches(changed []string, glob string) bool {
	if len(changed) == 0 {
		return true
	}
	for _, f := range changed {
		if matchGlob(glob, f) {
			return true
		}
	}
	return false
}

// matchGlob supports the one pattern people actually write for this: a `**` prefix-match like
// `web/**`, plus filepath.Match for everything else. Go's filepath.Match does not cross separators,
// so `web/*` would miss `web/src/app/page.tsx` — which is every file anyone means by "web".
func matchGlob(glob, path string) bool {
	if i := strings.Index(glob, "**"); i >= 0 {
		prefix := glob[:i]
		return strings.HasPrefix(path, prefix)
	}
	ok, err := filepath.Match(glob, path)
	return err == nil && ok
}
